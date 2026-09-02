package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// XM-OPS-TAILS0：「这个周期任务今天是不是真的开着」的部署配置读数。
//
// httpapi（cmd/platform-api）与 worker（cmd/platform-worker）是两个独立
// 进程，httpapi 进程内存里从来没有 worker 那份 jobs.Config——这一点在
// XM-JOBS0 已经如实记录（见 query_store.go 文件头注释），本文件不假装
// 解决了那个进程边界问题。本文件做的是另一件更小、但同样诚实的事：
// httpapi 自己的容器与 worker 拿的是**同一份 .env / compose 环境变量**
// （deploy/compose/launch.yaml 与 server-prod.yaml 对两个服务透传相同的
// XM_*_ENABLED / XM_*_INTERVAL / XM_*_MODE 变量），于是 httpapi 按
// worker **同一套解析规则**（cmd/platform-worker/config.go 的
// configFromEnv）自己读一遍这些变量，就能得到「这套部署配置声明的
// 应然状态」——这是"部署声明"，不是"worker 进程确认"，两者在两个进程
// env 一致的正常部署下应当相符，但本文件不作出"worker 确实在跑"这个
// 更强的断言。ScheduleStatus.ConfiguredSource 会诚实标出这一点。
//
// 刻意只读 Enabled / Interval / RunOnStart 三类字段：凭据、端点、白名单、
// 请求超时、实例 ID 等字段与"这个任务是否注册"无关，httpaph 没有理由
// 解析它们（最小权限：能不读的秘密相关配置就不读）。
//
// XM-OPS-TAILS1：per-job 的 enabled/interval 解析现在经由
// EffectiveJobSchedules（effective_manifest.go）——也就是 BuildEffectiveManifest
// 给 R210 信号化车队清单用的那同一段已校验核心，只是跳过了车队身份字段
// （见 EffectiveJobSchedules 的文档注释：本仓库里从没有任何进程真正设置过
// WorkerClusterID/RiverSchema，编一个假值只为了满足校验，本身就是宪法反对的
// 那种伪造事实）。以前这里直接调 effectiveJobConfig，跳过了「注册目录里的
// ScheduleConfig 是否与解析规则一致」这层交叉校验；现在两条路径共用同一段
// 校验代码，不会再各自维护一份可能悄悄分叉的判断。

// DeployedJobSchedule 是某个周期任务从部署环境变量解析出的"应然"调度。
type DeployedJobSchedule struct {
	// Enabled 与 jobs.EffectiveJobSpec.Enabled 同一个布尔量，只是来源
	// 是 httpapi 自己解析的环境变量，不是从 worker 进程读到的。
	Enabled bool
	// IntervalSeconds 是部署配置声明的周期，用于与 ScheduleStatus 里
	// 观测出来的 ObservedIntervalSeconds 对照——配置说 5 分钟、观测却是
	// 45 分钟，本身就是一个值得注意的信号（配置改了但没生效，或者任务
	// 卡住了）。
	IntervalSeconds int64
	// EnabledSource 是决定 Enabled 取值的那个环境变量名，heartbeat 这种
	// 没有开关、恒为 true 的任务填 "always"。前端据此告诉运营"这个结论
	// 是从哪个变量读出来的"，而不是凭空一个是/否。
	EnabledSource string
}

// DeployedSchedulesFromEnv 按 cmd/platform-worker/config.go 的同一套变量名
// 与解析规则（Sub2API/NewAPI/FinanceCollect/Retention/AlertEvaluate 五个
// _ENABLED+_INTERVAL 对、ReqlogMetrics 与 CPA 的 mode 驱动、
// ConnectorProbe 的 _ENABLED+_INTERVAL 对、heartbeat 恒为 true），
// 解析出 RegisteredPeriodicJobSpecs() 里每一个任务的部署配置。
//
// 返回的 map 恒含全部已注册任务的 key（不会漏任何一个）；env 解析失败
// （某个 _INTERVAL / _ENABLED 的值不是法值）时返回该字段的错误，调用方
// （cmd/platform-api/main.go）应把它当启动期配置错误处理，而不是吞掉——
// 这与 worker 侧同一个变量解析失败时的处理级别一致（configFromEnv 同样
// 返回 error 而不是回落缺省）。
func DeployedSchedulesFromEnv(getenv func(string) string) (map[string]DeployedJobSchedule, error) {
	cfg := DefaultConfig()

	if v := getenv("HEARTBEAT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("heartbeat interval: %w", err)
		}
		cfg.HeartbeatInterval = d
	}

	if err := applyEnabledInterval(getenv, "XM_SUB2API_SYNC_ENABLED", "XM_SUB2API_SYNC_INTERVAL",
		&cfg.Sub2APISyncEnabled, &cfg.Sub2APISyncInterval); err != nil {
		return nil, err
	}
	if err := applyEnabledInterval(getenv, "XM_NEWAPI_SYNC_ENABLED", "XM_NEWAPI_SYNC_INTERVAL",
		&cfg.NewAPISyncEnabled, &cfg.NewAPISyncInterval); err != nil {
		return nil, err
	}
	if err := applyEnabledInterval(getenv, "XM_FINANCE_COLLECT_ENABLED", "XM_FINANCE_COLLECT_INTERVAL",
		&cfg.FinanceCollectEnabled, &cfg.FinanceCollectInterval); err != nil {
		return nil, err
	}
	if err := applyEnabledInterval(getenv, "XM_RETENTION_ENABLED", "XM_RETENTION_INTERVAL",
		&cfg.RetentionEnabled, &cfg.RetentionInterval); err != nil {
		return nil, err
	}
	if err := applyEnabledInterval(getenv, "XM_ALERT_EVALUATE_ENABLED", "XM_ALERT_EVALUATE_INTERVAL",
		&cfg.AlertEvaluateEnabled, &cfg.AlertEvaluateInterval); err != nil {
		return nil, err
	}
	if err := applyEnabledInterval(getenv, "XM_CONNECTOR_PROBE_ENABLED", "XM_CONNECTOR_PROBE_INTERVAL",
		&cfg.ConnectorProbeEnabled, &cfg.ConnectorProbeInterval); err != nil {
		return nil, err
	}

	// reqlog_metrics：与 worker 同一条规则——只认 off/file，fake/real/拼写
	// 错误一律不报错、退化 off（服务的是"请求详情"另一条链路，platform-api
	// 自己已经在用同一个 XM_REQLOG_MODE 变量做那件事，这里只是照抄 worker
	// 那一侧"这个变量对本任务意味着什么"的判断，不改变量本身的含义）。
	reqlogMode, _ := ParseReqlogMetricsMode(getenv("XM_REQLOG_MODE"))
	cfg.ReqlogMetricsMode = reqlogMode
	if v := getenv("XM_REQLOG_METRICS_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("reqlog metrics interval: %w", err)
		}
		cfg.ReqlogMetricsInterval = d
	}

	// cpa_sync：mode 决定默认 enabled（file→true，off→false），
	// XM_CPA_SYNC_ENABLED 只能在 file 模式下覆盖（与 worker 侧
	// jobs.Config.validate() 拒绝"enabled 但 mode=off"同一条纪律；这里不
	// 重新校验矛盾配置，httpapi 只负责如实转述，真正的拒绝仍然只发生在
	// worker 启动那一刻）。
	cpaMode, err := ParseCPAMode(getenv("XM_CPA_MODE"))
	if err != nil {
		return nil, err
	}
	cfg.CPAMode = cpaMode
	if v := getenv("XM_CPA_SYNC_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("cpa sync enabled: %w", err)
		}
		cfg.CPASyncEnabled = enabled
	} else {
		cfg.CPASyncEnabled = cpaMode == CPAModeFile
	}
	if v := getenv("XM_CPA_SYNC_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("cpa sync interval: %w", err)
		}
		cfg.CPASyncInterval = d
	}

	sources := map[string]string{
		HeartbeatJobKind:      "always",
		Sub2APISyncJobKind:    "XM_SUB2API_SYNC_ENABLED",
		NewAPISyncJobKind:     "XM_NEWAPI_SYNC_ENABLED",
		FinanceCollectJobKind: "XM_FINANCE_COLLECT_ENABLED",
		RetentionJobKind:      "XM_RETENTION_ENABLED",
		AlertEvaluateJobKind:  "XM_ALERT_EVALUATE_ENABLED",
		ReqlogMetricsJobKind:  "XM_REQLOG_MODE=file",
		ConnectorProbeJobKind: "XM_CONNECTOR_PROBE_ENABLED",
		CPASyncJobKind:        "XM_CPA_MODE=file (or XM_CPA_SYNC_ENABLED override)",
	}

	// EffectiveJobSchedules (effective_manifest.go) is the same validated
	// per-job core BuildEffectiveManifest uses for the not-yet-wired R210
	// fleet manifest — it additionally cross-checks that each registered
	// job's declared ScheduleConfig source still matches what this package's
	// switch statement (effectiveJobConfig) expects for that job ID, which a
	// direct per-job call here previously did not. It needs no fleet
	// identity (see EffectiveJobSchedules' doc comment).
	effective, err := EffectiveJobSchedules(cfg)
	if err != nil {
		return nil, fmt.Errorf("deployed schedule: %w", err)
	}
	out := make(map[string]DeployedJobSchedule, len(effective))
	for _, row := range effective {
		out[row.ID] = DeployedJobSchedule{
			Enabled:         row.Enabled,
			IntervalSeconds: row.IntervalSeconds,
			EnabledSource:   sources[row.ID],
		}
	}
	return out, nil
}

// applyEnabledInterval 是 5 个 "_ENABLED + _INTERVAL" 对共用的解析样板
// （sub2api_sync / newapi_sync / finance_cost_sync / retention_prune /
// alert_evaluate / connector_probe 六个任务里的前五个走这条路径，
// connector_probe 也是；reqlog_metrics 与 cpa_sync 因为由 mode 驱动
// enabled，各自在上面单独处理）。空字符串保留 cfg 已有的缺省值——与
// cmd/platform-worker/config.go 的同款样板同一条规则：没有显式配置就
// 沿用 jobs.DefaultConfig()。
func applyEnabledInterval(
	getenv func(string) string, enabledVar, intervalVar string,
	enabled *bool, interval *time.Duration,
) error {
	if v := getenv(enabledVar); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s: %w", enabledVar, err)
		}
		*enabled = parsed
	}
	if v := getenv(intervalVar); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: %w", intervalVar, err)
		}
		if strings.TrimSpace(v) != "" {
			*interval = parsed
		}
	}
	return nil
}
