package main

import (
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestParseConfigDefaultsMatchOriginalHardcodedValues(t *testing.T) {
	// 不传任何 flag/环境变量：落盘位置、监听端口、上游目标、保留期、抓包
	// 上限必须与桌面端原型 reqlogger.go 的硬编码值逐项一致——这是"收编"
	// 而不是"重写"的前提。
	cfg, err := ParseConfig(nil, envFrom(nil))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, DefaultDataDir)
	}
	if cfg.ListenNewAPI != DefaultListenNewAPI || cfg.UpstreamNewAPI != DefaultUpstreamNewAPI {
		t.Errorf("newapi 监听/上游 = %q/%q", cfg.ListenNewAPI, cfg.UpstreamNewAPI)
	}
	if cfg.ListenSub2API != DefaultListenSub2API || cfg.UpstreamSub2API != DefaultUpstreamSub2API {
		t.Errorf("sub2api 监听/上游 = %q/%q", cfg.ListenSub2API, cfg.UpstreamSub2API)
	}
	if cfg.TokenMapPath != DefaultTokenMapPath {
		t.Errorf("TokenMapPath = %q, want %q", cfg.TokenMapPath, DefaultTokenMapPath)
	}
	if cfg.RetentionDays != DefaultRetentionDays {
		t.Errorf("RetentionDays = %d, want %d", cfg.RetentionDays, DefaultRetentionDays)
	}
	if cfg.MaxReqBody != DefaultMaxReqBody || cfg.MaxRespBody != DefaultMaxRespBody {
		t.Errorf("请求/响应上限 = %d/%d", cfg.MaxReqBody, cfg.MaxRespBody)
	}
	if cfg.TokenMapRefresh != DefaultTokenMapRefresh {
		t.Errorf("TokenMapRefresh = %v, want %v", cfg.TokenMapRefresh, DefaultTokenMapRefresh)
	}
	// 权限位是**新增默认**（不是原值 0700/0600），见 config.go 的说明。
	if cfg.DirPerm != DefaultDirPerm {
		t.Errorf("DirPerm = %o, want %o", cfg.DirPerm, DefaultDirPerm)
	}
	if cfg.FilePerm != DefaultFilePerm {
		t.Errorf("FilePerm = %o, want %o", cfg.FilePerm, DefaultFilePerm)
	}
	if cfg.GroupID != DefaultGroupID {
		t.Errorf("GroupID = %d, want %d", cfg.GroupID, DefaultGroupID)
	}
}

func TestParseConfigEnvOverridesDefaults(t *testing.T) {
	cfg, err := ParseConfig(nil, envFrom(map[string]string{
		"XM_REQLOG_RECORDER_DATA_DIR":       "/tmp/reqlog-data",
		"XM_REQLOG_RECORDER_RETENTION_DAYS": "7",
		"XM_REQLOG_GID":                     "0",
		"XM_REQLOG_RECORDER_DIR_PERM":       "0700",
		"XM_REQLOG_RECORDER_FILE_PERM":      "600",
	}))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.DataDir != "/tmp/reqlog-data" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.RetentionDays != 7 {
		t.Errorf("RetentionDays = %d", cfg.RetentionDays)
	}
	if cfg.GroupID != 0 {
		t.Errorf("GroupID = %d", cfg.GroupID)
	}
	if cfg.DirPerm != 0o700 {
		t.Errorf("DirPerm = %o", cfg.DirPerm)
	}
	if cfg.FilePerm != 0o600 {
		t.Errorf("FilePerm = %o（不带前导 0 的八进制字符串也要能解析）", cfg.FilePerm)
	}
}

func TestParseConfigFlagOverridesEnv(t *testing.T) {
	// flag 优先于环境变量：三者链条里 flag 是最后一道
	cfg, err := ParseConfig(
		[]string{"--data-dir=/from/flag", "--retention-days=3"},
		envFrom(map[string]string{
			"XM_REQLOG_RECORDER_DATA_DIR":       "/from/env",
			"XM_REQLOG_RECORDER_RETENTION_DAYS": "14",
		}),
	)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.DataDir != "/from/flag" {
		t.Errorf("DataDir = %q, want /from/flag（flag 应覆盖环境变量）", cfg.DataDir)
	}
	if cfg.RetentionDays != 3 {
		t.Errorf("RetentionDays = %d, want 3", cfg.RetentionDays)
	}
}

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	cases := map[string][]string{
		"data-dir 为空":         {"--data-dir="},
		"retention 非正":        {"--retention-days=0"},
		"max-req-body 非正":     {"--max-req-body=0"},
		"dir-perm 非法":         {"--dir-perm=not-octal"},
		"tokenmap-refresh 非正": {"--tokenmap-refresh=0s"},
	}
	for name, args := range cases {
		if _, err := ParseConfig(args, envFrom(nil)); err == nil {
			t.Errorf("%s：应被拒绝", name)
		}
	}
}

func TestParseFileModeAcceptsCommonNotations(t *testing.T) {
	cases := map[string]uint32{
		"0750": 0o750, "750": 0o750, "0o750": 0o750, "0640": 0o640,
	}
	for in, want := range cases {
		got, err := parseFileMode(in, 0)
		if err != nil {
			t.Errorf("parseFileMode(%q): %v", in, err)
			continue
		}
		if uint32(got) != want {
			t.Errorf("parseFileMode(%q) = %o, want %o", in, got, want)
		}
	}
	if _, err := parseFileMode("999", 0); err == nil {
		t.Error("999 含非八进制数字，应被拒绝")
	}
}

func TestConfigValidateRejectsOutOfRangePerm(t *testing.T) {
	cfg := Config{
		DataDir: "/x", ListenNewAPI: "a", UpstreamNewAPI: "b",
		ListenSub2API: "c", UpstreamSub2API: "d",
		RetentionDays: 1, MaxReqBody: 1, MaxRespBody: 1,
		TokenMapRefresh: time.Second,
		DirPerm:         0o1750, // 超过 0777 的位（比如 setuid 位）不接受
		FilePerm:        0o640,
	}
	if err := cfg.validate(); err == nil {
		t.Error("超出 0..0777 范围的权限位应被拒绝")
	}
}
