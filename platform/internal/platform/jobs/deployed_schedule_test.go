package jobs

import "testing"

func envMap(overrides map[string]string) func(string) string {
	return func(key string) string { return overrides[key] }
}

func TestDeployedSchedulesFromEnvDefaults(t *testing.T) {
	out, err := DeployedSchedulesFromEnv(envMap(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 与 jobs.DefaultConfig() 的缺省值逐条对齐（client.go 的注释里逐条
	// 解释了每一个默认值背后的理由，本测试只锁定"httpapi 这份独立解析
	// 得出同样的结论"）。
	want := map[string]bool{
		HeartbeatJobKind:      true,
		Sub2APISyncJobKind:    true,
		NewAPISyncJobKind:     true,
		FinanceCollectJobKind: true,
		RetentionJobKind:      true,
		AlertEvaluateJobKind:  true,
		ConnectorProbeJobKind: true,
		// reqlog_metrics 默认 off（原料是外挂系统的真实落盘数据，没有安全
		// 的 fake 可以垫底）；cpa_sync 默认 off（没有 fake 模式）。
		ReqlogMetricsJobKind: false,
		CPASyncJobKind:       false,
	}
	if len(out) != len(RegisteredPeriodicJobSpecs()) {
		t.Fatalf("got %d entries, want %d (one per registered job)", len(out), len(RegisteredPeriodicJobSpecs()))
	}
	for id, wantEnabled := range want {
		got, ok := out[id]
		if !ok {
			t.Fatalf("missing entry for %q", id)
		}
		if got.Enabled != wantEnabled {
			t.Errorf("%q: Enabled = %v, want %v", id, got.Enabled, wantEnabled)
		}
		if got.IntervalSeconds <= 0 {
			t.Errorf("%q: IntervalSeconds = %d, want positive", id, got.IntervalSeconds)
		}
		if got.EnabledSource == "" {
			t.Errorf("%q: EnabledSource must not be empty", id)
		}
	}
	if out[HeartbeatJobKind].EnabledSource != "always" {
		t.Errorf("heartbeat EnabledSource = %q, want always", out[HeartbeatJobKind].EnabledSource)
	}
}

func TestDeployedSchedulesFromEnvHonorsExplicitOverrides(t *testing.T) {
	out, err := DeployedSchedulesFromEnv(envMap(map[string]string{
		"XM_SUB2API_SYNC_ENABLED":  "false",
		"XM_SUB2API_SYNC_INTERVAL": "10m",
		"XM_RETENTION_ENABLED":     "false",
		"XM_REQLOG_MODE":           "file",
		"XM_CPA_MODE":              "file",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[Sub2APISyncJobKind].Enabled {
		t.Error("sub2api_sync should be disabled by XM_SUB2API_SYNC_ENABLED=false")
	}
	if out[Sub2APISyncJobKind].IntervalSeconds != 600 {
		t.Errorf("sub2api_sync interval = %d, want 600 (10m)", out[Sub2APISyncJobKind].IntervalSeconds)
	}
	if out[RetentionJobKind].Enabled {
		t.Error("retention_prune should be disabled by XM_RETENTION_ENABLED=false")
	}
	// XM_REQLOG_MODE=file 打开 reqlog_metrics；同一个变量对 platform-api
	// 自己的"请求详情"链路含义不同（off/fake/real），worker 侧只认
	// off/file——本函数复刻的正是 worker 那一侧的判断。
	if !out[ReqlogMetricsJobKind].Enabled {
		t.Error("reqlog_metrics should be enabled by XM_REQLOG_MODE=file")
	}
	if !out[CPASyncJobKind].Enabled {
		t.Error("cpa_sync should default-follow XM_CPA_MODE=file")
	}
}

func TestDeployedSchedulesFromEnvReqlogModeDegradesSilently(t *testing.T) {
	// fake/real 是"请求详情"链路的合法值，对本任务是拼写错误以外的"合法但
	// 不适用"——与 worker 侧 ParseReqlogMetricsMode 同一条纪律：不报错，
	// 退化成 off。
	for _, mode := range []string{"fake", "real", "not-a-mode", ""} {
		out, err := DeployedSchedulesFromEnv(envMap(map[string]string{"XM_REQLOG_MODE": mode}))
		if err != nil {
			t.Fatalf("XM_REQLOG_MODE=%q: unexpected error: %v", mode, err)
		}
		if out[ReqlogMetricsJobKind].Enabled {
			t.Errorf("XM_REQLOG_MODE=%q: reqlog_metrics should degrade to disabled", mode)
		}
	}
}

func TestDeployedSchedulesFromEnvCPAExplicitOverrideWinsOverMode(t *testing.T) {
	// file 模式默认打开同步，但显式 XM_CPA_SYNC_ENABLED=false 必须能临时
	// 按下暂停键而不必把 XM_CPA_MODE 也改回 off（与 worker 侧
	// cmd/platform-worker/config.go 同一条纪律）。
	out, err := DeployedSchedulesFromEnv(envMap(map[string]string{
		"XM_CPA_MODE":         "file",
		"XM_CPA_SYNC_ENABLED": "false",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[CPASyncJobKind].Enabled {
		t.Error("explicit XM_CPA_SYNC_ENABLED=false must override the file-mode default")
	}
}

func TestDeployedSchedulesFromEnvRejectsInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"bad heartbeat interval":      {"HEARTBEAT_INTERVAL": "not-a-duration"},
		"bad sub2api enabled":         {"XM_SUB2API_SYNC_ENABLED": "maybe"},
		"bad sub2api interval":        {"XM_SUB2API_SYNC_INTERVAL": "not-a-duration"},
		"bad cpa mode":                {"XM_CPA_MODE": "not-off-or-file"},
		"bad cpa sync enabled":        {"XM_CPA_SYNC_ENABLED": "maybe"},
		"bad reqlog metrics interval": {"XM_REQLOG_METRICS_INTERVAL": "not-a-duration"},
	}
	for name, overrides := range cases {
		if _, err := DeployedSchedulesFromEnv(envMap(overrides)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
