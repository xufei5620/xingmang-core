package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

func TestConfigFromEnv(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":            "staging",
		"HEARTBEAT_INTERVAL":     "2s",
		"HEARTBEAT_RUN_ON_START": "false",
		"HEARTBEAT_FAILURES":     "1",
	}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "staging" || cfg.HeartbeatInterval != 2*time.Second || cfg.HeartbeatRunOnStart || cfg.HeartbeatFailures != 1 {
		t.Fatalf("configFromEnv = %+v", cfg)
	}
}

func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	for key, value := range map[string]string{
		"HEARTBEAT_INTERVAL":     "not-a-duration",
		"HEARTBEAT_RUN_ON_START": "maybe",
		"HEARTBEAT_FAILURES":     "not-an-int",
	} {
		values := map[string]string{"ENVIRONMENT": "test", key: value}
		if _, err := configFromEnv(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("%s=%q should fail", key, value)
		}
	}
}

func TestConfigFromEnvRequiresExplicitEnvironment(t *testing.T) {
	if _, err := configFromEnv(func(string) string { return "" }); err == nil {
		t.Fatal("missing ENVIRONMENT should fail closed")
	}
}

// TestConfigFromEnvDefaultsSub2APIToFake：真实凭据要一个个环境去开，
// 默认必须是 fake；来源标识也必须一眼可辨，不能伪装成真实来源。
func TestConfigFromEnvDefaultsSub2APIToFake(t *testing.T) {
	values := map[string]string{"ENVIRONMENT": "staging"}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sub2APISyncEnabled {
		t.Fatal("默认应开启 Sub2API 周期同步——看板要的是持续更新的数据")
	}
	if cfg.Sub2APIMode != jobs.Sub2APIModeFake {
		t.Fatalf("默认模式 = %q, want fake", cfg.Sub2APIMode)
	}
	if cfg.Sub2APIInstanceID != jobs.DefaultSub2APIInstanceID {
		t.Fatalf("默认来源 = %q, want %q", cfg.Sub2APIInstanceID, jobs.DefaultSub2APIInstanceID)
	}
	if cfg.Sub2APISyncInterval != jobs.DefaultSub2APISyncInterval {
		t.Fatalf("默认周期 = %s, want %s", cfg.Sub2APISyncInterval, jobs.DefaultSub2APISyncInterval)
	}
	if cfg.Sub2APICredentialRef != "" {
		t.Fatalf("未配置时不该凭空造出凭据引用: %q", cfg.Sub2APICredentialRef)
	}
}

func TestConfigFromEnvReadsSub2APISettings(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":               "staging",
		"XM_SUB2API_MODE":           "real",
		"XM_SUB2API_INSTANCE_ID":    "sub2api-acceptance",
		"XM_SUB2API_SYNC_INTERVAL":  "60s",
		"XM_SUB2API_SYNC_ENABLED":   "false",
		"XM_SUB2API_CREDENTIAL_REF": "secret://sub2api/readonly-token",
	}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sub2APIMode != jobs.Sub2APIModeReal {
		t.Fatalf("mode = %q, want real", cfg.Sub2APIMode)
	}
	if cfg.Sub2APIInstanceID != "sub2api-acceptance" {
		t.Fatalf("source = %q, want sub2api-acceptance", cfg.Sub2APIInstanceID)
	}
	if cfg.Sub2APISyncEnabled || cfg.Sub2APISyncInterval != time.Minute {
		t.Fatalf("停用开关与周期未生效: %+v", cfg)
	}
	// 引用只被接住、不被解析出明文（ADR-014）。
	if cfg.Sub2APICredentialRef != "secret://sub2api/readonly-token" {
		t.Fatalf("credential ref = %q", cfg.Sub2APICredentialRef)
	}
}

func TestConfigFromEnvRejectsInvalidSub2APIValues(t *testing.T) {
	for key, value := range map[string]string{
		"XM_SUB2API_MODE":          "production",
		"XM_SUB2API_SYNC_INTERVAL": "not-a-duration",
		"XM_SUB2API_SYNC_ENABLED":  "maybe",
	} {
		values := map[string]string{"ENVIRONMENT": "staging", key: value}
		if _, err := configFromEnv(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("%s=%q should fail", key, value)
		}
	}
}

// TestConfigFromEnvReadsSub2APIConnection：XM-0017 的连接配置。
//
// 缺配置**不在启动时报错**：一个配错的采集通道不该把心跳和别的任务一起
// 拖垮，缺什么会在每轮同步写成一条说得清的 SyncFailed 观测。
func TestConfigFromEnvReadsSub2APIConnection(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":                 "staging",
		"XM_SUB2API_MODE":             "real",
		"XM_SUB2API_ENDPOINT":         "https://api.solov.cc",
		"XM_SUB2API_TARGET_ALLOWLIST": " api.solov.cc , API.Backup.Solov.CC ,, ",
		"XM_SUB2API_CREDENTIAL_REF":   "secret://sub2api/readonly-token",
	}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sub2APIEndpoint != "https://api.solov.cc" {
		t.Fatalf("endpoint = %q", cfg.Sub2APIEndpoint)
	}
	// 拆分只做去空白与转小写，不做补全：allowlist 的全部价值就在于
	// 它是人显式写下的那一份。
	want := []string{"api.solov.cc", "api.backup.solov.cc"}
	if len(cfg.Sub2APITargetAllowlist) != len(want) {
		t.Fatalf("allowlist = %v, want %v", cfg.Sub2APITargetAllowlist, want)
	}
	for i := range want {
		if cfg.Sub2APITargetAllowlist[i] != want[i] {
			t.Fatalf("allowlist = %v, want %v", cfg.Sub2APITargetAllowlist, want)
		}
	}
	if cfg.Sub2APIRequestTimeout <= 0 {
		t.Fatal("单次请求必须有超时（规格 §18.1-4）")
	}

	// 一项都没配时也不报错：mode 还可能是 fake
	bare := map[string]string{"ENVIRONMENT": "staging", "XM_SUB2API_MODE": "real"}
	cfg, err = configFromEnv(func(key string) string { return bare[key] })
	if err != nil {
		t.Fatalf("缺连接配置不该让 worker 起不来: %v", err)
	}
	if cfg.Sub2APIEndpoint != "" || len(cfg.Sub2APITargetAllowlist) != 0 {
		t.Fatalf("没配的东西不该被凭空造出来: %+v", cfg)
	}
}

func TestSub2APISecretsFromEnv(t *testing.T) {
	values := map[string]string{"XM_SUB2API_TOKEN": "placeholder-placeholder"}
	getenv := func(key string) string { return values[key] }

	// 没配引用 = 没有 Provider，但**不是错误**：fake 模式根本用不到它。
	provider, err := sub2apiSecretsFromEnv(getenv, nil, "staging", "")
	if provider != nil || err != nil {
		t.Fatalf("没配引用时应返回 (nil, nil), got %v %v", provider, err)
	}

	// 引用拼错了要在启动时就炸：这是配置错误，等到采集那天才发现更贵
	if _, err := sub2apiSecretsFromEnv(getenv, nil, "staging", "not-a-ref"); err == nil {
		t.Fatal("非法 CredentialRef 必须被拒")
	}

	provider, err = sub2apiSecretsFromEnv(getenv, nil, "staging", "secret://sub2api/readonly-token")
	if err != nil || provider == nil {
		t.Fatalf("装配失败: %v", err)
	}
	ref := secrets.MustCredentialRef("secret://sub2api/readonly-token")
	value, err := provider.Resolve(t.Context(), ref, "test")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Reveal() != "placeholder-placeholder" {
		t.Fatal("解析出的值不对")
	}
	// 打印/日志一律脱敏（宪法 7 条）——这条纪律由 SecretValue 的类型保证，
	// 这里再钉一次是因为装配处最容易有人顺手把它 fmt 出来。
	if got := fmt.Sprintf("%v/%s", value, value); strings.Contains(got, "placeholder") {
		t.Fatalf("SecretValue 不该被打印出明文: %s", got)
	}

	// 登记表之外的引用解析不出来：禁止静默回退到别的数据源（规格 §18.1-5）
	other := secrets.MustCredentialRef("secret://sub2api/another-token")
	if _, err := provider.Resolve(t.Context(), other, "test"); err == nil {
		t.Fatal("未登记的引用必须解析失败，不能回退去读别的变量")
	}
}

// TestConfigFromEnvReadsNewAPIConnection：XM-0038 的连接配置。
//
// 与 Sub2API 同一条纪律：缺配置**不在启动时报错**——一个配错的采集通道不该
// 把心跳和别的任务一起拖垮，缺什么会在每轮同步写成一条说得清的 SyncFailed
// 观测（规格 §9.1）。
func TestConfigFromEnvReadsNewAPIConnection(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":                "staging",
		"XM_NEWAPI_MODE":             "real",
		"XM_NEWAPI_ENDPOINT":         "https://xm.solov.cc",
		"XM_NEWAPI_TARGET_ALLOWLIST": " xm.solov.cc , XM.Backup.Solov.CC ,, ",
		"XM_NEWAPI_CREDENTIAL_REF":   "secret://newapi/readonly-token",
		"XM_NEWAPI_USER_ID":          " 1 ",
	}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NewAPIMode != jobs.NewAPIModeReal {
		t.Fatalf("mode = %q, want real", cfg.NewAPIMode)
	}
	if cfg.NewAPIEndpoint != "https://xm.solov.cc" {
		t.Fatalf("endpoint = %q", cfg.NewAPIEndpoint)
	}
	if cfg.NewAPICredentialRef != "secret://newapi/readonly-token" {
		t.Fatalf("credential ref = %q", cfg.NewAPICredentialRef)
	}
	// user id 是可选的普通配置，不是凭据——去空白即可。
	if cfg.NewAPIUserID != "1" {
		t.Fatalf("user id = %q, want 1", cfg.NewAPIUserID)
	}
	// 拆分只做去空白与转小写，不做补全：allowlist 的全部价值就在于
	// 它是人显式写下的那一份。
	want := []string{"xm.solov.cc", "xm.backup.solov.cc"}
	if len(cfg.NewAPITargetAllowlist) != len(want) {
		t.Fatalf("allowlist = %v, want %v", cfg.NewAPITargetAllowlist, want)
	}
	for i := range want {
		if cfg.NewAPITargetAllowlist[i] != want[i] {
			t.Fatalf("allowlist = %v, want %v", cfg.NewAPITargetAllowlist, want)
		}
	}

	// 一项都没配时也不报错：mode 还可能是 fake
	bare := map[string]string{"ENVIRONMENT": "staging", "XM_NEWAPI_MODE": "real"}
	cfg, err = configFromEnv(func(key string) string { return bare[key] })
	if err != nil {
		t.Fatalf("缺连接配置不该让 worker 起不来: %v", err)
	}
	if cfg.NewAPIEndpoint != "" || len(cfg.NewAPITargetAllowlist) != 0 || cfg.NewAPIUserID != "" {
		t.Fatalf("没配的东西不该被凭空造出来: %+v", cfg)
	}
}

func TestNewAPISecretsFromEnv(t *testing.T) {
	values := map[string]string{"XM_NEWAPI_TOKEN": "placeholder-placeholder"}
	getenv := func(key string) string { return values[key] }

	// 没配引用 = 没有 Provider，但**不是错误**：fake 模式根本用不到它。
	provider, err := newapiSecretsFromEnv(getenv, nil, "staging", "")
	if provider != nil || err != nil {
		t.Fatalf("没配引用时应返回 (nil, nil), got %v %v", provider, err)
	}

	// 引用拼错了要在启动时就炸：这是配置错误，等到采集那天才发现更贵
	if _, err := newapiSecretsFromEnv(getenv, nil, "staging", "not-a-ref"); err == nil {
		t.Fatal("非法 CredentialRef 必须被拒")
	}

	provider, err = newapiSecretsFromEnv(getenv, nil, "staging", "secret://newapi/readonly-token")
	if err != nil || provider == nil {
		t.Fatalf("装配失败: %v", err)
	}
	ref := secrets.MustCredentialRef("secret://newapi/readonly-token")
	value, err := provider.Resolve(t.Context(), ref, "test")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Reveal() != "placeholder-placeholder" {
		t.Fatal("解析出的值不对")
	}
	// 打印/日志一律脱敏（宪法 7 条）
	if got := fmt.Sprintf("%v/%s", value, value); strings.Contains(got, "placeholder") {
		t.Fatalf("SecretValue 不该被打印出明文: %s", got)
	}

	// 两条采集链路各用各的登记表：NewAPI 的 Provider 绝不能解析出
	// Sub2API 的引用，否则一次凭据轮换会静默影响到另一条链路。
	other := secrets.MustCredentialRef("secret://sub2api/readonly-token")
	if _, err := provider.Resolve(t.Context(), other, "test"); err == nil {
		t.Fatal("未登记的引用必须解析失败，不能回退去读别的变量")
	}
}

// TestNewAPIRevenueFromEnvNotConfigured：没配 DSN = 这条能力没启用，**不是错误**。
//
// 这条锁住 XM-0044 最重要的兼容性承诺：没配的部署行为与本任务之前逐字相同
// （收入侧 not_supported、台账写 NULL）。
func TestNewAPIRevenueFromEnvNotConfigured(t *testing.T) {
	for _, dsn := range []string{"", "   "} {
		values := map[string]string{"XM_NEWAPI_REVENUE_DSN": dsn}
		src, closer, err := newapiRevenueFromEnv(
			t.Context(), func(k string) string { return values[k] }, nil, "staging")
		if src != nil || closer != nil || err != nil {
			t.Fatalf("没配 DSN 应返回 (nil, nil, nil), got (src=%v, closer!=nil=%v, err=%v)",
				src, closer != nil, err)
		}
	}
}

// TestNewAPIRevenueFromEnvDegradesLoudly：配了但立不起来时，**不能伪装成没配**。
//
// 两者在台账里都写 NULL（收入未知），但对运维是完全不同的两件事：
// 没配 → not_supported（这条链路还没接通，正常）；
// 配错 → unavailable（你配了但它不通，要去修）。
// 把 source 留成 nil 会让后者显示成前者，然后一个拼错的 DSN 可以安静躺几个星期。
func TestNewAPIRevenueFromEnvDegradesLoudly(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
		hint   string
	}{
		{
			name: "配了 DSN 没配口令引用",
			values: map[string]string{
				"XM_NEWAPI_REVENUE_DSN": "postgres://reader@db.example.test/newapi",
			},
			hint: "XM_NEWAPI_REVENUE_PASSWORD_REF",
		},
		{
			name: "口令引用拼错",
			values: map[string]string{
				"XM_NEWAPI_REVENUE_DSN":          "postgres://reader@db.example.test/newapi",
				"XM_NEWAPI_REVENUE_PASSWORD_REF": "not-a-ref",
			},
			hint: "XM_NEWAPI_REVENUE_PASSWORD_REF",
		},
		{
			// 连接串里带内联口令：pgdsn 会拒（宪法 7 条）。
			name: "DSN 里带内联口令",
			values: map[string]string{
				"XM_NEWAPI_REVENUE_DSN":          "postgres://reader:inline@db.example.test/newapi",
				"XM_NEWAPI_REVENUE_PASSWORD_REF": "secret://newapi/revenue-db",
				"XM_NEWAPI_REVENUE_PASSWORD":     "placeholder-placeholder",
			},
			hint: "CredentialRef",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, closer, err := newapiRevenueFromEnv(
				t.Context(), func(k string) string { return tc.values[k] }, nil, "staging")
			if err == nil {
				t.Fatal("配错了必须回一个错误供启动日志用")
			}
			if !strings.Contains(err.Error(), tc.hint) {
				t.Fatalf("错误应说清问题出在哪（含 %q）: %v", tc.hint, err)
			}
			if closer != nil {
				t.Fatal("没建起池就不该回 closer")
			}
			if src == nil {
				t.Fatal("配错了也必须挂一个降级通道——留 nil 会让「配错」伪装成「没配」")
			}
			// 降级通道必须报 unavailable，**不是** not_supported。
			_, readErr := src.AccountRevenue(t.Context(), "1", "2026-08-28")
			if got := connector.KindOf(readErr); got != connector.KindUnavailable {
				t.Fatalf("降级通道的分类 = %q, want unavailable（「配了但不通」≠「没配」）", got)
			}
		})
	}
}

// TestNewAPIRevenueDegradedNeverLeaksPassword：降级通道带着的那个错误会进启动日志。
func TestNewAPIRevenueDegradedNeverLeaksPassword(t *testing.T) {
	values := map[string]string{
		"XM_NEWAPI_REVENUE_DSN":          "postgres://reader:hunter2@db.example.test/newapi",
		"XM_NEWAPI_REVENUE_PASSWORD_REF": "secret://newapi/revenue-db",
	}
	src, _, err := newapiRevenueFromEnv(
		t.Context(), func(k string) string { return values[k] }, nil, "staging")
	if err == nil {
		t.Fatal("内联口令必须被拒")
	}
	_, readErr := src.AccountRevenue(t.Context(), "1", "2026-08-28")
	for name, dump := range map[string]string{
		"startup_error": err.Error(),
		"read_error":    fmt.Sprintf("%v/%+v", readErr, readErr),
	} {
		if strings.Contains(dump, "hunter2") {
			t.Fatalf("%s 泄漏了数据库口令: %s", name, dump)
		}
	}
}

// TestConfigFromEnvReadsRetentionSettings：XM-R012 保留期可配。
func TestConfigFromEnvReadsRetentionSettings(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":                     "staging",
		"XM_RETENTION_ENABLED":            "true",
		"XM_RETENTION_INTERVAL":           "6h",
		"XM_METRIC_SAMPLE_RETENTION_DAYS": "30",
		"XM_ALERT_RETENTION_DAYS":         "365",
	}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RetentionEnabled || cfg.RetentionInterval != 6*time.Hour {
		t.Fatalf("清理开关/周期 = %v / %s", cfg.RetentionEnabled, cfg.RetentionInterval)
	}
	if cfg.MetricSampleRetentionDays != 30 || cfg.AlertRetentionDays != 365 {
		t.Fatalf("保留天数 = %d / %d", cfg.MetricSampleRetentionDays, cfg.AlertRetentionDays)
	}
}

// TestConfigFromEnvRejectsNonPositiveRetentionDays：0 天不是「不清理」。
//
// 0 天最自然的读法是「不保留」，也就是**把整张表删空**；而想表达「不清理」的人
// 该去关 XM_RETENTION_ENABLED。两种意图差得太远，不能让一个手滑的 0 去猜——
// 猜错的那一次没有撤销键。
func TestConfigFromEnvRejectsNonPositiveRetentionDays(t *testing.T) {
	// 用切片而不是 map：同一个变量要试多个坏值。
	for _, bad := range []struct{ key, value string }{
		{"XM_METRIC_SAMPLE_RETENTION_DAYS", "0"},
		{"XM_METRIC_SAMPLE_RETENTION_DAYS", "-1"},
		{"XM_ALERT_RETENTION_DAYS", "0"},
		{"XM_ALERT_RETENTION_DAYS", "-30"},
		{"XM_RETENTION_INTERVAL", "not-a-duration"},
		{"XM_RETENTION_ENABLED", "maybe"},
	} {
		values := map[string]string{"ENVIRONMENT": "test", bad.key: bad.value}
		if _, err := configFromEnv(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("%s=%q 应当被拒绝", bad.key, bad.value)
		}
	}
}

// TestConfigFromEnvHasNoAuditRetentionKnob：审计没有保留天数这个旋钮。
//
// 审计链一条都不删（宪法 11 条 append-only，理由见 jobs/retention.go 文件头）。
// 给一个删不掉东西的旋钮比不给更误导：填了之后审计表照涨，而填的人以为自己
// 已经配好了清理。
func TestConfigFromEnvHasNoAuditRetentionKnob(t *testing.T) {
	var asked []string
	values := map[string]string{"ENVIRONMENT": "test"}
	if _, err := configFromEnv(func(name string) string {
		asked = append(asked, name)
		return values[name]
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range asked {
		if strings.Contains(name, "AUDIT") && strings.Contains(name, "RETENTION") {
			t.Fatalf("不该存在审计保留期变量，却读了 %s", name)
		}
	}
}
