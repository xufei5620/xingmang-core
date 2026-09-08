package main

import (
	"slices"
	"strings"
	"testing"

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

// TestWorkerStartupLogNamesDefaultsAsDefaults 钉住 XM-OPS-TRUTH 子片 A 第 1
// 条的最后一句：worker_started 里可以保留缺省值，但**字段名要说清是缺省**。
//
// 2026-09-08 有人照旧字段名（sub2api_mode）读启动日志，得出「生产在跑假数据」
// 的结论，而 core.connector_config 两行 08-30 就是 real。带 _default 后缀之后
// 这个误读在字面上就不成立了。
func TestWorkerStartupLogNamesDefaultsAsDefaults(t *testing.T) {
	cfg := jobs.DefaultConfig()
	cfg.Environment = "staging"
	cfg.Sub2APIMode = jobs.Sub2APIModeFake
	cfg.NewAPIMode = jobs.NewAPIModeFake

	attrs := attrMap(t, connectorModeStartupAttrs(cfg))

	// 在场：带 _default 后缀的缺省值。
	for key, want := range map[string]any{
		"sub2api_mode_default": string(jobs.Sub2APIModeFake),
		"newapi_mode_default":  string(jobs.NewAPIModeFake),
		// 生效值来自哪儿，以及去哪条日志里找它——不指望有人先读过 runbook。
		"connector_config_source":   "database",
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

	// 缺席：裸键必须消失。它的变异验证在下面 TestStartupDefaultsRenameIsTwoWay：
	// 把实现改回裸键时，这里的缺席断言与上面的在场断言会**同时**红。
	for _, key := range []string{"sub2api_mode", "newapi_mode"} {
		if _, ok := attrs[key]; ok {
			t.Fatalf("worker_started 又出现了裸键 %s：它装的是 env 缺省，"+
				"叫这个名字会被当成生效模式读（2026-09-08 就是这么读错的）", key)
		}
	}

	// 别的字段不许被这次改名顺手带走：source / interval / enabled 都还在。
	for _, key := range []string{
		"sub2api_sync_enabled", "sub2api_source", "sub2api_sync_interval",
		"newapi_sync_enabled", "newapi_source", "newapi_sync_interval",
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
// 来的键，要求「凡是以 _mode 结尾的连接器模式字段，必须是 _mode_default」。
//
// 单纯断言「sub2api_mode 不在」在字段被整个删掉时也会绿；这一条要求那个
// 事实**以某个名字在场**，只是名字必须带 _default。
func TestStartupDefaultsRenameIsTwoWay(t *testing.T) {
	attrs := attrMap(t, connectorModeStartupAttrs(jobs.DefaultConfig()))
	var modeKeys []string
	for key := range attrs {
		if strings.HasSuffix(key, "_mode") {
			modeKeys = append(modeKeys, key)
		}
	}
	if len(modeKeys) != 0 {
		t.Fatalf("worker_started 里还有以 _mode 结尾的键 %v：启动那一刻打不出生效模式，"+
			"这些字段只能叫 *_mode_default", modeKeys)
	}
	var defaults []string
	for key := range attrs {
		if strings.HasSuffix(key, "_mode_default") {
			defaults = append(defaults, key)
		}
	}
	slices.Sort(defaults)
	want := []string{"newapi_mode_default", "sub2api_mode_default"}
	if !slices.Equal(defaults, want) {
		t.Fatalf("缺省模式字段 = %v, want %v", defaults, want)
	}
}
