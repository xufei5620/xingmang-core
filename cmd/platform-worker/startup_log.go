package main

import "github.com/xufei5620/xingmang-platform/internal/platform/jobs"

// connectorModeStartupAttrs 是 worker_started 里与「接入模式」有关的那几个
// 字段（XM-OPS-TRUTH）。
//
// 单独抽出来只为一件事：让它可测。主体那条日志内联在 main() 里，测试伸不
// 进去，而这几个字段名恰恰是 2026-09-08 排查走偏的直接原因——有人照旧字段
// 名读 worker_started 里的 sub2api_mode，得出「生产在跑假数据」的结论，
// 而生产那时跑的是 real（core.connector_config 两行 08-30 就设成 real 了）。
//
// 字段名带 _default 后缀是这次修复的要点：
//   - *_mode_default 是**环境变量给的缺省**，后台热切换模式不重启容器，
//     它永远不变；本进程启动那一刻还没读过库，这里打不出生效值。
//   - 生效值在第一轮同步（*SyncRunOnStart 默认 true）的
//     connector_config_applied，以及每轮 job_completed 的 *_mode /
//     *_mode_source 里——effective_mode_log_events 把这条指路带进日志本身，
//     不指望有人先读过 runbook。
//
// CPA 不在这里：worker_started 从来没打过 cpa_mode，而且它那个字段本来就是
// 生效值（cpa 不在 credentials.Platforms，没有 core.connector_config 行）。
func connectorModeStartupAttrs(config jobs.Config) []any {
	return []any{
		"connector_config_source", "database",
		"effective_mode_log_events", "connector_config_applied,job_completed",
		"sub2api_sync_enabled", config.Sub2APISyncEnabled,
		"sub2api_mode_default", string(config.Sub2APIMode),
		"sub2api_source", config.Sub2APIInstanceID,
		"sub2api_sync_interval", config.Sub2APISyncInterval.String(),
		"newapi_sync_enabled", config.NewAPISyncEnabled,
		"newapi_mode_default", string(config.NewAPIMode),
		"newapi_source", config.NewAPIInstanceID,
		"newapi_sync_interval", config.NewAPISyncInterval.String(),
	}
}
