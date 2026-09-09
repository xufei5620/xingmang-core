package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// fakeConnectorConfigSource 是内存版 ConnectorConfigSource：按 (platform,
// environment) 放行，并可注入读取错误，模拟库不可用。
type fakeConnectorConfigSource struct {
	rows  map[string]*ConnectorConfig
	err   error
	calls atomic.Int32
}

func (f *fakeConnectorConfigSource) Get(_ context.Context, platform, environment string) (*ConnectorConfig, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows[platform+"/"+environment].clone(), nil
}

// testSecretProvider 是一个不绑定引用的 Provider：任何引用都能解析出值。
// 动态工厂要求 Provider 不预绑定引用（行里的引用随时会变），这里模拟那条链。
func testSecretProvider(t *testing.T) secrets.SecretProvider {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{"sub2api-prod/read-token", "newapi/readonly-token", "sub2api/env-token"} {
		writeTestSecret(t, root, rel)
	}
	return secrets.NewFileProvider(root)
}

func sub2apiDefaults(t *testing.T, environment string) Sub2APIRealConfig {
	t.Helper()
	return Sub2APIRealConfig{
		Environment: environment,
		InstanceID:  "sub2api-test",
		Timeout:     DefaultSub2APIRequestTimeout,
		Secrets:     testSecretProvider(t),
	}
}

func TestCachingConnectorConfigSource(t *testing.T) {
	now := fixedNow
	var fetches atomic.Int32
	var fetchErr error
	fetch := func(_ context.Context, platform, environment string) (*ConnectorConfig, error) {
		fetches.Add(1)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if platform != ConnectorPlatformSub2API {
			return nil, nil // newapi 没有行
		}
		return &ConnectorConfig{
			Platform: platform, Environment: environment, Mode: "real",
			Endpoint: "https://api.example.test", TargetAllowlist: []string{"api.example.test"},
			CredentialRef: "secret://sub2api-prod/read-token", Version: 3,
		}, nil
	}
	source := newCachingConnectorConfigSource(fetch, 30*time.Second, func() time.Time { return now })
	ctx := context.Background()

	row, err := source.Get(ctx, ConnectorPlatformSub2API, "staging")
	if err != nil || row == nil || row.Version != 3 {
		t.Fatalf("首次读取应打库并返回行: %+v, %v", row, err)
	}
	// 缓存内不再打库；返回的是副本，改它不会污染缓存。
	row.TargetAllowlist[0] = "evil.example.test"
	row, err = source.Get(ctx, ConnectorPlatformSub2API, "staging")
	if err != nil || row.TargetAllowlist[0] != "api.example.test" {
		t.Fatalf("缓存必须返回副本: %+v, %v", row, err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("30s 内不该再打库, fetches = %d", got)
	}
	// 「没有这一行」同样缓存：env-only 的部署不该每秒打一次空查询。
	if row, err := source.Get(ctx, ConnectorPlatformNewAPI, "staging"); err != nil || row != nil {
		t.Fatalf("newapi 没有行应返回 (nil, nil): %+v, %v", row, err)
	}
	if _, _ = source.Get(ctx, ConnectorPlatformNewAPI, "staging"); fetches.Load() != 2 {
		t.Fatalf("空结果也应缓存, fetches = %d", fetches.Load())
	}
	// 另一个平台的行绝不能串：newapi 缓存的是 nil，不是 sub2api 的那一行。
	if row, _ := source.Get(ctx, ConnectorPlatformNewAPI, "staging"); row != nil {
		t.Fatalf("newapi 不该拿到 sub2api 的行: %+v", row)
	}
	// 过期后重新打库。
	now = now.Add(31 * time.Second)
	if _, err := source.Get(ctx, ConnectorPlatformSub2API, "staging"); err != nil || fetches.Load() != 3 {
		t.Fatalf("过期后应重新打库, fetches = %d, err = %v", fetches.Load(), err)
	}
	// 读库失败原样返回、不写缓存：下一次仍会再试。
	now = now.Add(31 * time.Second)
	fetchErr = errors.New("connection refused")
	if _, err := source.Get(ctx, ConnectorPlatformSub2API, "staging"); err == nil {
		t.Fatal("读库失败必须返回错误，让调用方回落到 env")
	}
	fetchErr = nil
	if row, err := source.Get(ctx, ConnectorPlatformSub2API, "staging"); err != nil || row == nil {
		t.Fatalf("库恢复后应再次读到行: %+v, %v", row, err)
	}
	if fetches.Load() != 5 {
		t.Fatalf("失败不该被缓存, fetches = %d", fetches.Load())
	}
	if _, err := source.Get(ctx, "", "staging"); err == nil {
		t.Fatal("空 platform 必须被拒")
	}
}

// TestDynamicSub2APIFactoryNoRowKeepsEnvBehavior：(a) 没有行 → 与 env 行为逐字相同。
func TestDynamicSub2APIFactoryNoRowKeepsEnvBehavior(t *testing.T) {
	source := &fakeConnectorConfigSource{rows: map[string]*ConnectorConfig{}}
	ctx := context.Background()

	// env 缺省 fake → Fake 客户端。
	client, eff, err := NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source: source, DefaultMode: Sub2APIModeFake, Defaults: sub2apiDefaults(t, "staging"),
	})(ctx)
	// 没有行时生效模式来自 env，而且工厂必须**说出来**是 env——
	// 「没有行」不等于 fake，这个区分是 XM-OPS-TRUTH 的要点。
	if eff.Mode != string(Sub2APIModeFake) || eff.Source != ModeSourceEnv {
		t.Fatalf("没有行时生效配置 = %+v, want mode=fake source=env", eff)
	}
	if err != nil || client == nil {
		t.Fatalf("没有行时应按 env 缺省 fake 构造: client=%v err=%v", client, err)
	}
	// env 缺省 real 且连接配置不全 → not_supported，错误链里仍说得清缺哪个变量。
	_, _, err = NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source: source, DefaultMode: Sub2APIModeReal, Defaults: sub2apiDefaults(t, "staging"),
	})(ctx)
	if connector.KindOf(err) != connector.KindNotSupported || !errors.Is(err, ErrSub2APIRealClientUnavailable) {
		t.Fatalf("没有行且 env real 未就绪应 not_supported: %v", err)
	}
	// env 缺省 real 且配置齐全 → 真实客户端。
	defaults := sub2apiDefaults(t, "staging")
	defaults.Endpoint = "https://env.example.test"
	defaults.TargetAllowlist = []string{"env.example.test"}
	defaults.CredentialRef = "secret://sub2api/env-token"
	client, _, err = NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source: source, DefaultMode: Sub2APIModeReal, Defaults: defaults,
	})(ctx)
	if err != nil || client == nil {
		t.Fatalf("没有行且 env 齐全应造出真实客户端: %v", err)
	}
	// 每次调用都会问一次来源——这正是「无需重启」的机制。
	if source.calls.Load() != 3 {
		t.Fatalf("工厂每次调用都应读取配置来源, calls = %d", source.calls.Load())
	}
}

// TestDynamicSub2APIFactoryRowSwitchesToReal：(b) 行 mode=real → 用行里的
// 端点 / allowlist / 引用构造真实客户端，env 缺省只兜底行里留空的字段。
func TestDynamicSub2APIFactoryRowSwitchesToReal(t *testing.T) {
	row := &ConnectorConfig{
		Platform: ConnectorPlatformSub2API, Environment: "staging", Mode: "real",
		Endpoint:        "https://row.example.test",
		TargetAllowlist: []string{" Row.Example.Test ", ""},
		CredentialRef:   "secret://sub2api-prod/read-token",
		Version:         7,
	}
	source := &fakeConnectorConfigSource{rows: map[string]*ConnectorConfig{"sub2api/staging": row}}
	var logs bytes.Buffer
	opts := Sub2APIDynamicOptions{
		Source:      source,
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		DefaultMode: Sub2APIModeFake, // env 还说 fake：行必须压过它
		Defaults:    sub2apiDefaults(t, "staging"),
	}
	factory := NewDynamicSub2APIClientFactory(opts)
	ctx := context.Background()

	// env 缺省没有端点 / allowlist / 引用：造得出客户端只能是因为用了行里的值。
	client, eff, err := factory(ctx)
	if err != nil || client == nil {
		t.Fatalf("行 mode=real 且齐全应造出真实客户端: client=%v err=%v", client, err)
	}
	// 行压过 env：生效模式必须是 real，来源必须是 database，版本必须是行的版本。
	if eff.Mode != "real" || eff.Source != ModeSourceDatabase || eff.Version != 7 {
		t.Fatalf("生效配置 = %+v, want mode=real source=database version=7", eff)
	}
	out := logs.String()
	for _, want := range []string{
		`"event":"connector_config_applied"`, `"config_source":"database"`, `"config_version":7`,
		`"mode":"real"`, `"endpoint_host":"row.example.test"`, `"credential_ref":"secret://sub2api-prod/read-token"`,
		`"allowlist_size":1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("生效配置变化应打一条 connector_config_applied（缺 %s）: %s", want, out)
		}
	}
	// 同一份配置第二轮不再重复打。
	logs.Reset()
	if _, _, err := factory(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "connector_config_applied") {
		t.Fatalf("配置未变化不该重复打日志: %s", logs.String())
	}

	// 行里的端点写错（http）→ 归 internal，证明确实是行的端点在起作用。
	row.Endpoint = "http://row.example.test"
	row.Version = 8
	if _, _, err := factory(ctx); connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("行里配错的端点应归 internal, got %v (%v)", connector.KindOf(err), err)
	}

	// 行里留空的字段由 env 缺省兜底：只填了 mode 与端点，引用与 allowlist 来自 env。
	partial := &ConnectorConfig{
		Platform: ConnectorPlatformSub2API, Environment: "staging", Mode: "real",
		Endpoint: "https://row.example.test",
	}
	source.rows["sub2api/staging"] = partial
	defaults := sub2apiDefaults(t, "staging")
	defaults.TargetAllowlist = []string{"row.example.test"}
	defaults.CredentialRef = "secret://sub2api/env-token"
	client, _, err = NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source: source, DefaultMode: Sub2APIModeFake, Defaults: defaults,
	})(ctx)
	if err != nil || client == nil {
		t.Fatalf("行里留空的字段应由 env 兜底: %v", err)
	}
	// 行说 fake 就是 fake（staging 允许）：mode 同样以行为准。
	source.rows["sub2api/staging"] = &ConnectorConfig{Platform: ConnectorPlatformSub2API, Environment: "staging", Mode: "fake"}
	envReal := sub2apiDefaults(t, "staging")
	envReal.Endpoint, envReal.TargetAllowlist, envReal.CredentialRef =
		"https://env.example.test", []string{"env.example.test"}, "secret://sub2api/env-token"
	if client, eff, err := NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source: source, DefaultMode: Sub2APIModeReal, Defaults: envReal,
	})(ctx); err != nil || client == nil {
		t.Fatalf("行 fake 在 staging 应造出 Fake 客户端: %v", err)
	} else if eff.Mode != "fake" || eff.Source != ModeSourceDatabase {
		// 行压过 env 的**两个方向**都要验：上面验了 real 压 fake，
		// 这里验 fake 压 real，否则「以行为准」可能只在一个方向成立。
		t.Fatalf("行 fake / env real 时生效配置 = %+v, want mode=fake source=database", eff)
	}
	// 行里的模式非法 → internal（库有 CHECK，这里是最后一道）。
	source.rows["sub2api/staging"] = &ConnectorConfig{Platform: ConnectorPlatformSub2API, Environment: "staging", Mode: "wat"}
	if _, _, err := factory(ctx); connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("非法模式应归 internal, got %v", err)
	}
}

// TestDynamicSub2APIFactoryProductionFakeIsNotSupported：(c) production 下
// 生效模式仍为 fake → not_supported，绝不同步演示数据；同步任务把它诚实落库。
func TestDynamicSub2APIFactoryProductionFakeIsNotSupported(t *testing.T) {
	source := &fakeConnectorConfigSource{rows: map[string]*ConnectorConfig{
		"sub2api/production": {Platform: ConnectorPlatformSub2API, Environment: "production", Mode: "fake"},
	}}
	factory := NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source: source, DefaultMode: Sub2APIModeFake, Defaults: sub2apiDefaults(t, "production"),
	})
	_, eff, err := factory(context.Background())
	if connector.KindOf(err) != connector.KindNotSupported || !errors.Is(err, ErrConnectorProductionFake) {
		t.Fatalf("production + fake 行应 not_supported: %v", err)
	}
	// 客户端造不出来，但模式**是**知道的（正是它导致造不出来）：这一轮的
	// job_completed 仍要说得出生效模式，否则运维只看到一句 not_supported。
	if eff.Mode != "fake" || eff.Source != ModeSourceDatabase {
		t.Fatalf("production + fake 行的生效配置 = %+v, want mode=fake source=database", eff)
	}
	if got := err.Error(); got != "not_supported: sub2api.client.mode" {
		t.Fatalf("对外错误文本 = %q，不该带细节", got)
	}
	// 没有行、env 缺省 fake，在 production 同样不放行——启动闸放行的只是启动。
	source.rows = map[string]*ConnectorConfig{}
	if _, _, err := factory(context.Background()); !errors.Is(err, ErrConnectorProductionFake) {
		t.Fatalf("production 下 env fake 兜底同样应 not_supported: %v", err)
	}

	// 整条同步链路：五条指标全部写成 failed + not_supported，任务本身不失败。
	store := newMemoryStore()
	worker := NewSub2APISyncWorker(Sub2APISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
		Environment: "production",
		Store:       store,
		NewClient:   factory,
		Now:         func() time.Time { return fixedNow },
	})
	if err := worker.Work(context.Background(), syncJob()); err != nil {
		t.Fatalf("Work = %v, want nil（未配置真实接入是事实，不是任务失败）", err)
	}
	if len(store.keys()) != len(contractMetricKeys) {
		t.Fatalf("落库指标 = %v, want 全部 %v", store.keys(), contractMetricKeys)
	}
	for _, key := range contractMetricKeys {
		row, ok := store.rows[store.key(key, "production")]
		if !ok {
			t.Fatalf("指标 %s 没有落库", key)
		}
		if row.Status != ops.SyncFailed || row.LastErrorCode != string(connector.KindNotSupported) {
			t.Fatalf("%s = %s/%s, want failed/not_supported", key, row.Status, row.LastErrorCode)
		}
		if len(row.Value) != 0 {
			t.Fatalf("%s 不该带任何演示数据: %v", key, row.Value)
		}
	}
}

// TestDynamicFactoryFallsBackToEnvWhenSourceFails：库读不到 → 本轮按 env，
// 只在进入故障时记一条 warn，恢复时记一条 info。
func TestDynamicFactoryFallsBackToEnvWhenSourceFails(t *testing.T) {
	source := &fakeConnectorConfigSource{
		rows: map[string]*ConnectorConfig{
			"sub2api/staging": {Platform: ConnectorPlatformSub2API, Environment: "staging", Mode: "real",
				Endpoint: "https://row.example.test", TargetAllowlist: []string{"row.example.test"},
				CredentialRef: "secret://sub2api-prod/read-token"},
		},
		err: errors.New("relation core.connector_config does not exist"),
	}
	var logs bytes.Buffer
	factory := NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
		Source:      source,
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		DefaultMode: Sub2APIModeFake,
		Defaults:    sub2apiDefaults(t, "staging"),
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		client, eff, err := factory(ctx)
		if err != nil || client == nil {
			t.Fatalf("库不可用时应回落到 env 缺省（fake）: %v", err)
		}
		// 「本轮按 env 兜底」必须自报：读不到库不等于「没有配过」，
		// 也不等于「按 fake 跑是对的」，运维要能从来源字段分辨这两种。
		if eff.Mode != "fake" || eff.Source != ModeSourceEnv || eff.Version != 0 {
			t.Fatalf("库不可用时生效配置 = %+v, want mode=fake source=env version=0", eff)
		}
	}
	out := logs.String()
	if got := strings.Count(out, `"event":"connector_config_unavailable"`); got != 1 {
		t.Fatalf("库不可用只该记一次, got %d: %s", got, out)
	}
	if !strings.Contains(out, `"config_source":"env"`) {
		t.Fatalf("回落到 env 应在 connector_config_applied 里可见: %s", out)
	}
	// 库恢复：切回行（real），并记一条 recovered。
	source.err = nil
	logs.Reset()
	if client, _, err := factory(ctx); err != nil || client == nil {
		t.Fatalf("库恢复后应按行构造真实客户端: %v", err)
	}
	out = logs.String()
	if !strings.Contains(out, `"event":"connector_config_recovered"`) || !strings.Contains(out, `"config_source":"database"`) {
		t.Fatalf("库恢复应可见: %s", out)
	}
	if strings.Contains(out, "test-value") {
		t.Fatalf("日志不得含凭据值: %s", out)
	}
}

// TestDynamicNewAPIFactory：NewAPI 侧同款三条断言 (a)(b)(c)。
func TestDynamicNewAPIFactory(t *testing.T) {
	ctx := context.Background()
	defaults := func(environment string) NewAPIRealConfig {
		return NewAPIRealConfig{
			Environment: environment, InstanceID: "newapi-test",
			Timeout: DefaultNewAPIRequestTimeout, Secrets: testSecretProvider(t),
		}
	}
	// (a) 没有行 → env 缺省。
	source := &fakeConnectorConfigSource{rows: map[string]*ConnectorConfig{}}
	if client, eff, err := NewDynamicNewAPIClientFactory(NewAPIDynamicOptions{
		Source: source, DefaultMode: NewAPIModeFake, Defaults: defaults("staging"),
	})(ctx); err != nil || client == nil {
		t.Fatalf("没有行时应按 env 缺省 fake 构造: %v", err)
	} else if eff.Mode != "fake" || eff.Source != ModeSourceEnv {
		t.Fatalf("没有行时生效配置 = %+v, want mode=fake source=env", eff)
	}
	if _, _, err := NewDynamicNewAPIClientFactory(NewAPIDynamicOptions{
		Source: source, DefaultMode: NewAPIModeReal, Defaults: defaults("staging"),
	})(ctx); !errors.Is(err, ErrNewAPIRealClientUnavailable) {
		t.Fatalf("没有行且 env real 未就绪应 not_supported: %v", err)
	}
	// (b) 行 mode=real → 行里的端点 / allowlist / 引用。
	source.rows["newapi/staging"] = &ConnectorConfig{
		Platform: ConnectorPlatformNewAPI, Environment: "staging", Mode: "real",
		Endpoint: "https://xm.example.test", TargetAllowlist: []string{"xm.example.test"},
		CredentialRef: "secret://newapi/readonly-token", Version: 2,
	}
	if client, eff, err := NewDynamicNewAPIClientFactory(NewAPIDynamicOptions{
		Source: source, DefaultMode: NewAPIModeFake, Defaults: defaults("staging"),
	})(ctx); err != nil || client == nil {
		t.Fatalf("行 mode=real 且齐全应造出真实客户端: %v", err)
	} else if eff.Mode != "real" || eff.Source != ModeSourceDatabase || eff.Version != 2 {
		t.Fatalf("生效配置 = %+v, want mode=real source=database version=2", eff)
	}
	// (c) production + fake 行 → not_supported。
	source.rows["newapi/production"] = &ConnectorConfig{Platform: ConnectorPlatformNewAPI, Environment: "production", Mode: "fake"}
	_, _, err := NewDynamicNewAPIClientFactory(NewAPIDynamicOptions{
		Source: source, DefaultMode: NewAPIModeFake, Defaults: defaults("production"),
	})(ctx)
	if connector.KindOf(err) != connector.KindNotSupported || !errors.Is(err, ErrConnectorProductionFake) {
		t.Fatalf("production + fake 行应 not_supported: %v", err)
	}
	// 没有来源时退化为静态工厂（与 XM-CRED0 之前逐字相同）。
	if client, eff, err := NewDynamicNewAPIClientFactory(NewAPIDynamicOptions{
		DefaultMode: NewAPIModeFake, Defaults: defaults("staging"),
	})(ctx); err != nil || client == nil {
		t.Fatalf("没有来源应退化为静态工厂: %v", err)
	} else if eff.Source != ModeSourceEnv {
		// 静态工厂那条路上 env 缺省**就是**生效配置，如实标 env——
		// 不是 unknown（这个进程知道自己的缺省），也不是 database（没有库）。
		t.Fatalf("静态工厂的生效来源 = %q, want env", eff.Source)
	}
}

// TestProductionFakeAllowedAtStartupWithConnectorConfigs：启动闸在有动态配置
// 来源时放行（行随时可能切成 real），没有来源时照旧拒绝。
func TestProductionFakeAllowedAtStartupWithConnectorConfigs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Environment = "production"
	cfg.Sub2APIMode = Sub2APIModeFake
	cfg.NewAPIMode = NewAPIModeFake
	cfg.FinanceCollectEnabled = false // 成本采集的同款闸不在本片范围，隔离掉
	if err := cfg.normalized().validate(); err == nil {
		t.Fatal("没有 ConnectorConfigs 时 production + fake 仍必须被拒")
	}
	cfg.ConnectorConfigs = &fakeConnectorConfigSource{}
	if err := cfg.normalized().validate(); err != nil {
		t.Fatalf("有 ConnectorConfigs 时 production + fake 应放行（模式由库决定）: %v", err)
	}
	// 装配选择：有来源走动态工厂，没有来源走静态工厂；两者都造得出客户端。
	cfg.Environment = "staging"
	if client, _, err := cfg.normalized().sub2apiClientFactory()(context.Background()); err != nil || client == nil {
		t.Fatalf("动态 sub2api 工厂: %v", err)
	}
	if client, _, err := cfg.normalized().newapiClientFactory()(context.Background()); err != nil || client == nil {
		t.Fatalf("动态 newapi 工厂: %v", err)
	}
	cfg.ConnectorConfigs = nil
	if client, _, err := cfg.normalized().sub2apiClientFactory()(context.Background()); err != nil || client == nil {
		t.Fatalf("静态 sub2api 工厂: %v", err)
	}
}

func TestEndpointHostForLogs(t *testing.T) {
	for in, want := range map[string]string{
		"":                                       "",
		"https://api.example.test":               "api.example.test",
		"https://api.example.test/v1/?x=1#frag":  "api.example.test",
		"https://user:pw@api.example.test/path":  "api.example.test",
		"api.example.test:8443/path":             "api.example.test:8443",
		"https://[::1]:8443/api":                 "[::1]:8443",
		"https://api.example.test/../etc/passwd": "api.example.test",
	} {
		if got := endpointHost(in); got != want {
			t.Fatalf("endpointHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeTestSecret 按文件 Provider 的嵌套布局写一份固定内容的测试凭据文件。
func writeTestSecret(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("test-value-"+filepath.Base(rel)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
