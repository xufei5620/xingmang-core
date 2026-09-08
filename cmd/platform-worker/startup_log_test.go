package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

// attrMap 把 slog 的变参键值对摊成 map，并顺带验它成对。
func attrMap(t *testing.T, attrs []any) map[string]any {
	t.Helper()
	if len(attrs)%2 != 0 {
		t.Fatalf("键值对不成对: %d 个元素", len(attrs))
	}
	out := map[string]any{}
	for i := 0; i < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok {
			t.Fatalf("第 %d 个键不是字符串: %v", i, attrs[i])
		}
		if _, dup := out[key]; dup {
			t.Fatalf("键 %q 重复", key)
		}
		out[key] = attrs[i+1]
	}
	return out
}

// startupConfig 是这几条测试共用的入参：一个**接了** core.connector_config
// 的部署（生产装配就是这样，见 cmd/platform-worker/main.go 里
// config.ConnectorConfigs = jobs.NewPgConnectorConfigSource(pool)）。
func startupConfig() jobs.Config {
	cfg := jobs.DefaultConfig()
	cfg.Environment = "staging"
	cfg.Sub2APIMode = jobs.Sub2APIModeFake
	cfg.NewAPIMode = jobs.NewAPIModeFake
	cfg.ConnectorConfigs = jobs.ConnectorConfigSourceFunc(
		func(context.Context, string, string) (*jobs.ConnectorConfig, error) { return nil, nil })
	return cfg
}

// TestWorkerStartupLogNamesDefaultsAsDefaults 钉住 XM-OPS-TRUTH 子片 A 第 1
// 条的最后一句：worker_started 里可以保留缺省值，但**字段名要说清是缺省**。
//
// 2026-09-08 有人照旧字段名（sub2api_mode）读启动日志，得出「生产在跑假数据」
// 的结论，而 core.connector_config 两行 08-30 就是 real。带 _default 后缀之后
// 这个误读在字面上就不成立了。
//
// 断言跑在 workerStartupAttrs——也就是 main() 真正打出去的那一整行——而不是
// 只跑在接入模式那一小段上：闸架在半成品上时，有人在 main() 的切片里加回一个
// 裸键 sub2api_mode，全套门禁一条都不会红。
func TestWorkerStartupLogNamesDefaultsAsDefaults(t *testing.T) {
	attrs := attrMap(t, workerStartupAttrs(startupConfig()))

	// 打出去的那一行必须仍然是一条完整的 worker_started：抽函数不许把
	// 事件名、模块、环境、主体、secret_root 这几样落在 main() 里。
	for key, want := range map[string]any{
		"event":       "worker_started",
		"module":      "platform.worker",
		"environment": "staging",
		// 主体固定是 worker:platform（宪法要求每条日志可归因）。
		"principal_id": "worker:platform",
	} {
		if got := attrs[key]; got != want {
			t.Fatalf("worker_started 的 %s = %v, want %v", key, got, want)
		}
	}
	if _, ok := attrs["secret_root"]; !ok {
		t.Fatalf("worker_started 少了 secret_root: %v", attrs)
	}

	// 在场：带 _default 后缀的缺省值。
	for key, want := range map[string]any{
		"sub2api_mode_default": string(jobs.Sub2APIModeFake),
		"newapi_mode_default":  string(jobs.NewAPIModeFake),
		// 生效值来自哪儿，以及去哪条日志里找它——不指望有人先读过 runbook。
		"connector_config_source":   jobs.ModeSourceDatabase,
		"effective_mode_log_events": "connector_config_applied,job_completed",
	} {
		got, ok := attrs[key]
		if !ok {
			t.Fatalf("worker_started 缺字段 %s: %v", key, attrs)
		}
		if got != want {
			t.Fatalf("%s = %v, want %v", key, got, want)
		}
	}

	// 缺席：裸键必须消失。范围是**发现**来的——credentials.Platforms 就是
	// core.connector_config 能覆盖模式的那张名单（与迁移 000020 的 CHECK 同源），
	// 手写 {"sub2api","newapi"} 的话，哪天加了第三个平台这条闸不会红。
	// 它的变异验证在下面 TestStartupDefaultsRenameIsTwoWay：把实现改回裸键时，
	// 这里的缺席断言与那边的在场断言会**同时**红。
	for _, platform := range credentials.Platforms {
		if _, ok := attrs[platform+"_mode"]; ok {
			t.Fatalf("worker_started 又出现了裸键 %s_mode：它装的是 env 缺省，"+
				"叫这个名字会被当成生效模式读（2026-09-08 就是这么读错的）", platform)
		}
	}

	// 别的字段不许被这次改名顺手带走：source / interval / enabled 都还在。
	for _, key := range []string{
		"sub2api_sync_enabled", "sub2api_source", "sub2api_sync_interval",
		"newapi_sync_enabled", "newapi_source", "newapi_sync_interval",
		// 整条抽走时最容易掉队的是尾巴那几组，各留一个哨兵。
		"finance_collect_enabled", "alert_evaluate_enabled",
		"audit_archive_enabled", "reqlog_metrics_mode",
		"assurance_probe_global_enabled",
	} {
		if _, ok := attrs[key]; !ok {
			t.Fatalf("worker_started 少了既有字段 %s: %v", key, attrs)
		}
	}

	// 启动日志里绝不出现凭据材料或完整端点（宪法 7 条）。
	for key, value := range attrs {
		text, ok := value.(string)
		if !ok {
			continue
		}
		if strings.Contains(text, "secret://") || strings.Contains(text, "://") {
			t.Fatalf("worker_started 的 %s 带了引用或 URL: %q", key, text)
		}
	}
}

// TestStartupDefaultsRenameIsTwoWay 让缺席断言不至于恒真：它枚举了实际打出
// 来的键，要求「凡是 core.connector_config 管得着的平台，模式字段必须以
// _mode_default 的名字在场，且不许再有同名的裸键」。
//
// 单纯断言「sub2api_mode 不在」在字段被整个删掉时也会绿；这一条要求那个
// 事实**以某个名字在场**，只是名字必须带 _default。
//
// 反过来，finance_collect_mode / audit_archive_mode / reqlog_metrics_mode
// 这些裸 _mode 键是**对的**，不在这条闸的范围里：它们不在
// credentials.Platforms，core.connector_config 里没有它们的行，环境变量就是
// 它们的真相源，那个字段打的已经是生效值。
func TestStartupDefaultsRenameIsTwoWay(t *testing.T) {
	attrs := attrMap(t, workerStartupAttrs(startupConfig()))

	var bare, defaults []string
	for _, platform := range credentials.Platforms {
		if _, ok := attrs[platform+"_mode"]; ok {
			bare = append(bare, platform+"_mode")
		}
		if _, ok := attrs[platform+"_mode_default"]; ok {
			defaults = append(defaults, platform+"_mode_default")
		}
	}
	if len(bare) != 0 {
		t.Fatalf("worker_started 里还有裸键 %v：启动那一刻打不出生效模式，"+
			"core.connector_config 管得着的平台只能叫 *_mode_default", bare)
	}
	var want []string
	for _, platform := range credentials.Platforms {
		want = append(want, platform+"_mode_default")
	}
	slices.Sort(defaults)
	slices.Sort(want)
	if !slices.Equal(defaults, want) {
		t.Fatalf("缺省模式字段 = %v, want %v（范围取自 credentials.Platforms）", defaults, want)
	}
}

// TestStartupConnectorConfigSourceFollowsAssembly 钉住审稿指出的第二处
// 「写死的副本」：connector_config_source 原先是字面量 "database"，不看
// config.ConnectorConfigs。
//
// ConnectorConfigs 为 nil 的部署走静态工厂，每一轮按 env 解析、job_completed
// 打的是 *_mode_source=env，而 worker_started 仍宣称 database——那是一句
// 「事实变了它不会跟着变、也不会报错」的话。今天它恰好为真，只因为生产装配
// 无条件设了 ConnectorConfigs；靠别处事实成立的判断就是静默债。
func TestStartupConnectorConfigSourceFollowsAssembly(t *testing.T) {
	withSource := startupConfig()
	noSource := startupConfig()
	noSource.ConnectorConfigs = nil

	for _, tc := range []struct {
		name string
		cfg  jobs.Config
		want string
	}{
		{"接了 core.connector_config", withSource, jobs.ModeSourceDatabase},
		{"没接（静态工厂，env 说了算）", noSource, jobs.ModeSourceEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := attrMap(t, workerStartupAttrs(tc.cfg))
			if got := attrs["connector_config_source"]; got != tc.want {
				t.Fatalf("connector_config_source = %v, want %v", got, tc.want)
			}
		})
	}

	// 两种装配必须给出**不同**的答案：写死成任何一个常量都会让上面两个子例
	// 里恰好有一个绿，这一条要求它们不相等，恒真的实现过不去。
	withAttrs := attrMap(t, workerStartupAttrs(withSource))
	withoutAttrs := attrMap(t, workerStartupAttrs(noSource))
	if withAttrs["connector_config_source"] == withoutAttrs["connector_config_source"] {
		t.Fatalf("接与不接 core.connector_config 给出了同一个 connector_config_source = %v："+
			"这个字段没有在看入参", withAttrs["connector_config_source"])
	}
}
