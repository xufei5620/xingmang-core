package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
)

// 本文件钉住 XM-OPS-TRUTH 子片 A 第 1 条：作业日志里的 *_mode 必须是**本轮
// 生效**的模式，而不是进程启动时从环境变量拷来的缺省值。
//
// 2026-09-08 生产上 core.connector_config 两行都是 real（08-30 设的），
// worker 却一直打 newapi_mode=fake / sub2api_mode=fake，排查因此得出
// 「生产在跑假数据」的错误结论。

// logLines 把一段 JSON 行日志解析成 map 列表。
//
// 用 encoding/json 逐行解析而不是 strings.Contains：后者会被别处日志里的
// 同名子串蒙混（比如 connector_config_applied 里的 "mode":"real"），
// 而这一组测试问的恰恰是「**哪一条**日志里的**哪一个**字段」。
func logLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("日志行不是 JSON: %q (%v)", line, err)
		}
		out = append(out, row)
	}
	return out
}

// logEvents 挑出某一类事件的所有行。
func logEvents(t *testing.T, raw, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, row := range logLines(t, raw) {
		if row["event"] == event {
			out = append(out, row)
		}
	}
	return out
}

// singleEvent 要求恰好一条该事件的行并返回它。
func singleEvent(t *testing.T, raw, event string) map[string]any {
	t.Helper()
	rows := logEvents(t, raw, event)
	if len(rows) != 1 {
		t.Fatalf("%s 行数 = %d, want 1: %s", event, len(rows), raw)
	}
	return rows[0]
}

func wantField(t *testing.T, row map[string]any, key string, want any) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Fatalf("日志行缺字段 %s: %v", key, row)
	}
	if got != want {
		t.Fatalf("%s = %v, want %v", key, got, want)
	}
}

// TestNewAPISyncLogsEffectiveModeNotEnvDefault 是本片的核心断言。
//
// 每个子例都刻意让「库里的行」与「进程 env 缺省」**相反**：旧实现打的是
// env 缺省，所以每一条都会红；只有真的把工厂本轮解析出来的那一个打出来
// 才能绿。这正是 memory「正向断言要问旧实现下会不会照样绿」要的那种用例——
// 既有的 `"newapi_mode":"fake"` 断言在旧实现下同样绿，它对本片一无所知。
func TestNewAPISyncLogsEffectiveModeNotEnvDefault(t *testing.T) {
	secrets := testSecretProvider(t)
	defaults := func() NewAPIRealConfig {
		return NewAPIRealConfig{
			Environment: "staging", InstanceID: "newapi-test",
			Timeout: DefaultNewAPIRequestTimeout, Secrets: secrets,
		}
	}

	cases := []struct {
		name        string
		row         *ConnectorConfig
		sourceErr   error
		envDefault  NewAPIMode
		wantMode    string
		wantSource  string
		wantVersion float64 // JSON 数字解出来是 float64
	}{
		{
			// A：行 real / env fake —— 生产 2026-09-08 的形态，旧实现在这里说谎。
			name: "行 real 压过 env fake",
			row: &ConnectorConfig{
				Platform: ConnectorPlatformNewAPI, Environment: "staging", Mode: "real", Version: 9,
			},
			envDefault: NewAPIModeFake,
			wantMode:   "real", wantSource: ModeSourceDatabase, wantVersion: 9,
		},
		{
			// B：没有行 / env real —— 「没有行」不等于 fake。
			name:       "没有行时按 env 缺省 real",
			envDefault: NewAPIModeReal,
			wantMode:   "real", wantSource: ModeSourceEnv, wantVersion: 0,
		},
		{
			// C：行 fake / env real —— 行压过 env 的**另一个方向**也要验，
			// 否则「以行为准」可能只在一个方向成立。
			name: "行 fake 压过 env real",
			row: &ConnectorConfig{
				Platform: ConnectorPlatformNewAPI, Environment: "staging", Mode: "fake", Version: 3,
			},
			envDefault: NewAPIModeReal,
			wantMode:   "fake", wantSource: ModeSourceDatabase, wantVersion: 3,
		},
		{
			// D：库读不到 / env real —— 「本轮按 env 兜底」必须在作业日志里
			// 自报，不能只在 resolver 自己的日志里说一句。
			name:       "库读不到时按 env 兜底并自报",
			sourceErr:  errors.New("relation core.connector_config does not exist"),
			envDefault: NewAPIModeReal,
			wantMode:   "real", wantSource: ModeSourceEnv, wantVersion: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := map[string]*ConnectorConfig{}
			if tc.row != nil {
				rows["newapi/staging"] = tc.row
			}
			source := &fakeConnectorConfigSource{rows: rows, err: tc.sourceErr}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			worker := NewNewAPISyncWorker(NewAPISyncOptions{
				Logger:      logger,
				Environment: "staging",
				InstanceID:  "newapi-test",
				Store:       newMemoryStore(),
				NewClient: NewDynamicNewAPIClientFactory(NewAPIDynamicOptions{
					Source:      source,
					Logger:      logger,
					DefaultMode: tc.envDefault,
					Defaults:    defaults(),
				}),
				Now: func() time.Time { return fixedNow },
			})
			if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
				t.Fatalf("Work = %v, want nil", err)
			}

			done := singleEvent(t, logs.String(), "job_completed")
			wantField(t, done, "newapi_mode", tc.wantMode)
			wantField(t, done, "newapi_mode_source", tc.wantSource)
			wantField(t, done, "newapi_config_version", tc.wantVersion)
			// 加字段不许把既有字段挤掉：运维的 grep 与验收脚本都指着它们。
			for _, key := range []string{"source", "business_day", "metrics_total", "metrics_failed"} {
				if _, ok := done[key]; !ok {
					t.Fatalf("job_completed 缺既有字段 %s: %v", key, done)
				}
			}
			// 缺席断言：整份日志里不该出现与生效模式相反的那个值。
			// 变异验证见 TestNewAPISyncLogsEffectiveModeNotEnvDefault 的说明——
			// 把实现改回打 w.mode（env 缺省），子例 A/C 的这一条会红。
			opposite := "fake"
			if tc.wantMode == "fake" {
				opposite = "real"
			}
			for _, row := range logEvents(t, logs.String(), "job_completed") {
				if row["newapi_mode"] == opposite {
					t.Fatalf("job_completed 打出了与生效模式相反的值 %q: %v", opposite, row)
				}
			}
			// 库读不到那一轮还要有一条 connector_config_unavailable。
			if tc.sourceErr != nil && len(logEvents(t, logs.String(), "connector_config_unavailable")) != 1 {
				t.Fatalf("库读不到应记一条 connector_config_unavailable: %s", logs.String())
			}
			// 凭据与完整端点绝不进日志（宪法 7 条）。
			if strings.Contains(logs.String(), "test-value") {
				t.Fatalf("日志泄漏了凭据内容: %s", logs.String())
			}
		})
	}
}

// TestSub2APISyncLogsEffectiveModeNotEnvDefault 是 sub2api 侧的同型用例。
func TestSub2APISyncLogsEffectiveModeNotEnvDefault(t *testing.T) {
	cases := []struct {
		name        string
		row         *ConnectorConfig
		sourceErr   error
		envDefault  Sub2APIMode
		wantMode    string
		wantSource  string
		wantVersion float64
	}{
		{
			name: "行 real 压过 env fake",
			row: &ConnectorConfig{
				Platform: ConnectorPlatformSub2API, Environment: "staging", Mode: "real", Version: 4,
			},
			envDefault: Sub2APIModeFake,
			wantMode:   "real", wantSource: ModeSourceDatabase, wantVersion: 4,
		},
		{
			name:       "没有行时按 env 缺省 real",
			envDefault: Sub2APIModeReal,
			wantMode:   "real", wantSource: ModeSourceEnv, wantVersion: 0,
		},
		{
			name: "行 fake 压过 env real",
			row: &ConnectorConfig{
				Platform: ConnectorPlatformSub2API, Environment: "staging", Mode: "fake", Version: 5,
			},
			envDefault: Sub2APIModeReal,
			wantMode:   "fake", wantSource: ModeSourceDatabase, wantVersion: 5,
		},
		{
			name:       "库读不到时按 env 兜底并自报",
			sourceErr:  errors.New("relation core.connector_config does not exist"),
			envDefault: Sub2APIModeReal,
			wantMode:   "real", wantSource: ModeSourceEnv, wantVersion: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := map[string]*ConnectorConfig{}
			if tc.row != nil {
				rows["sub2api/staging"] = tc.row
			}
			source := &fakeConnectorConfigSource{rows: rows, err: tc.sourceErr}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			worker := NewSub2APISyncWorker(Sub2APISyncOptions{
				Logger:      logger,
				Environment: "staging",
				InstanceID:  "sub2api-test",
				Store:       newMemoryStore(),
				NewClient: NewDynamicSub2APIClientFactory(Sub2APIDynamicOptions{
					Source:      source,
					Logger:      logger,
					DefaultMode: tc.envDefault,
					Defaults:    sub2apiDefaults(t, "staging"),
				}),
				Now: func() time.Time { return fixedNow },
			})
			if err := worker.Work(context.Background(), syncJob()); err != nil {
				t.Fatalf("Work = %v, want nil", err)
			}

			done := singleEvent(t, logs.String(), "job_completed")
			wantField(t, done, "sub2api_mode", tc.wantMode)
			wantField(t, done, "sub2api_mode_source", tc.wantSource)
			wantField(t, done, "sub2api_config_version", tc.wantVersion)
			opposite := "fake"
			if tc.wantMode == "fake" {
				opposite = "real"
			}
			if done["sub2api_mode"] == opposite {
				t.Fatalf("job_completed 打出了与生效模式相反的值 %q: %v", opposite, done)
			}
			if strings.Contains(logs.String(), "test-value") {
				t.Fatalf("日志泄漏了凭据内容: %s", logs.String())
			}
		})
	}
}

// TestCPASyncLogsModeSourceEnv：CPA 这一条**没有说谎**，补的是来源标签。
//
// 它与 sub2api/newapi 不同型：cpa 不在 credentials.Platforms，
// core.connector_config 里没有它的行，XM_CPA_MODE 就是真相源。
// 打出 cpa_mode_source 是为了让运维不必记住「哪个 *_mode 能信」。
func TestCPASyncLogsModeSourceEnv(t *testing.T) {
	var logs bytes.Buffer
	worker := newTestCPAWorker(newMemoryStore(),
		cpaFactory(stubCPAClient{
			usage: successfulUsage(), keys: successfulKeys(), health: successfulHealth(),
		}, nil), &logs)
	if err := worker.Work(context.Background(), cpaSyncJob()); err != nil {
		t.Fatalf("Work = %v, want nil", err)
	}
	done := singleEvent(t, logs.String(), "job_completed")
	wantField(t, done, "cpa_mode", string(CPAModeFile))
	wantField(t, done, "cpa_mode_source", ModeSourceEnv)
}

// TestCPAHasNoConnectorConfigRow 钉住上面那条「今天为什么不需要解析器」的
// 前提（memory「条件恰好为真：先答它靠什么成立」）。
//
// 哪天有人把 cpa 加进 credentials.Platforms，这条会红，提醒
// cpa_sync.go 的 cpa_mode 必须改成每轮从库解析，否则它会安静地退化成
// XM-OPS-TRUTH 刚修掉的那种谎话。
func TestCPAHasNoConnectorConfigRow(t *testing.T) {
	if slices.Contains(credentials.Platforms, "cpa") {
		t.Fatal("cpa 进了 credentials.Platforms：cpa_sync.go 的 cpa_mode 必须改成" +
			"每轮经 jobs.ResolveEffectiveMode 解析，cpa_mode_source 也不再恒为 env")
	}
	// 反过来也钉住：另外两个平台**必须**在名单里，否则动态工厂那条链会断，
	// 而断了之后这条测试本身会变成一句恒真的空话。
	for _, platform := range []string{ConnectorPlatformSub2API, ConnectorPlatformNewAPI} {
		if !slices.Contains(credentials.Platforms, platform) {
			t.Fatalf("%s 不在 credentials.Platforms = %v", platform, credentials.Platforms)
		}
	}
}

// TestResolveEffectiveModeIsTheOnlyJudgment 直接测那个唯一判定。
//
// 它是纯函数，所以这里能把四种入参组合一次性摆开——worker 与
// /ops/overview 走的都是它，两边不许再各写一个 if。
func TestResolveEffectiveModeIsTheOnlyJudgment(t *testing.T) {
	row := &ConnectorConfig{Platform: "sub2api", Mode: "real", Version: 12}
	blank := &ConnectorConfig{Platform: "sub2api", Mode: "  ", Version: 12}

	cases := []struct {
		name           string
		row            *ConnectorConfig
		processDefault string
		want           EffectiveConnectorConfig
	}{
		{"有行：以行为准，带版本", row, "fake",
			EffectiveConnectorConfig{Platform: "sub2api", Mode: "real", Source: ModeSourceDatabase, Version: 12}},
		{"有行但模式留空：落到缺省一侧（与逐字段覆盖口径一致）", blank, "fake",
			EffectiveConnectorConfig{Platform: "sub2api", Mode: "fake", Source: ModeSourceEnv}},
		{"没有行但调用方知道缺省：env", nil, "real",
			EffectiveConnectorConfig{Platform: "sub2api", Mode: "real", Source: ModeSourceEnv}},
		{"没有行且调用方看不到缺省：unknown，不是 fake", nil, "",
			EffectiveConnectorConfig{Platform: "sub2api", Mode: "", Source: ModeSourceUnknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveEffectiveMode("sub2api", tc.row, tc.processDefault); got != tc.want {
				t.Fatalf("ResolveEffectiveMode = %+v, want %+v", got, tc.want)
			}
		})
	}
	// 最后一条单独再说一遍，因为它是 2026-09-08 那个错误答案的正面反例：
	// platform-api 容器没有 XM_*_MODE，它对「没有行」只能回答不知道。
	if got := ResolveEffectiveMode("newapi", nil, "").Mode; got == "fake" {
		t.Fatal("没有行且看不到缺省时不许答 fake——那正是被修掉的第三套猜法")
	}
}

// TestSyncWorkersHaveNoModeField 是一条编译期之外的纪律断言：
// Options 上不许再长回一个 Mode 字段。
//
// 「物理上删掉」是本片的手段（memory「被信任的过期闸最危险：要让它物理上
// 只剩一处」）；靠注释拦不住下一个人。用 encoding/json 的字段名列表来看，
// 是因为结构体里没有 json tag 时反射拿到的就是 Go 字段名。
func TestSyncWorkersHaveNoModeField(t *testing.T) {
	for name, fields := range map[string][]string{
		"NewAPISyncOptions":  structFieldNames(NewAPISyncOptions{}),
		"Sub2APISyncOptions": structFieldNames(Sub2APISyncOptions{}),
	} {
		if slices.Contains(fields, "Mode") {
			t.Fatalf("%s 又长出了 Mode 字段：生效模式由 NewClient 每轮带回，"+
				"这个字段装的只会是 env 缺省（见 XM-OPS-TRUTH）", name)
		}
	}
	// CPA 保留 Mode 是对的（没有库行，env 就是真相源）——顺带钉住，
	// 免得有人「统一」时把它也删了。
	if !slices.Contains(structFieldNames(CPASyncOptions{}), "Mode") {
		t.Fatal("CPASyncOptions.Mode 不该被删：CPA 没有 core.connector_config 行")
	}
}

// TestUpstreamReadDoesNotLeakCredentials 是一条横切的泄漏闸：
// 新增的逐次读取日志绝不许带凭据或完整端点（宪法 7 条）。
func TestUpstreamReadDoesNotLeakCredentials(t *testing.T) {
	var logs bytes.Buffer
	newTestNewAPISyncWorker(newMemoryStore(), newapiFakeFactory(newapi.FakeOptions{}), &logs).
		Work(context.Background(), newapiSyncJob()) //nolint:errcheck // 只看日志
	newTestSyncWorker(newMemoryStore(), fakeFactory(sub2api.FakeOptions{}), &logs).
		Work(context.Background(), syncJob()) //nolint:errcheck // 只看日志
	for _, row := range logEvents(t, logs.String(), "upstream_read") {
		for key, value := range row {
			text, ok := value.(string)
			if !ok {
				continue
			}
			if strings.Contains(text, "secret://") || strings.Contains(text, "://") {
				t.Fatalf("upstream_read 的 %s 带了引用或 URL: %v", key, row)
			}
		}
	}
}
