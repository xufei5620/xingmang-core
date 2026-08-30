package main

import (
	"path/filepath"
	"testing"
)

func TestParseReqlogModeAcceptsFile(t *testing.T) {
	mode, err := parseReqlogMode("file")
	if err != nil || mode != reqlogModeFile {
		t.Fatalf("parseReqlogMode(\"file\") = %q, %v", mode, err)
	}
	if _, err := parseReqlogMode(" FILE "); err != nil {
		t.Errorf("大小写/空白应被容忍: %v", err)
	}
}

func TestReqlogConfigFromEnvFileMode(t *testing.T) {
	cfg, err := reqlogConfigFromEnv(envFrom(map[string]string{
		"XM_REQLOG_MODE": "file",
	}))
	if err != nil {
		t.Fatalf("reqlogConfigFromEnv: %v", err)
	}
	if cfg.Mode != reqlogModeFile {
		t.Fatalf("Mode = %q", cfg.Mode)
	}
	if cfg.DataDir != defaultReqlogDataDir {
		t.Fatalf("DataDir 默认值 = %q, want %q", cfg.DataDir, defaultReqlogDataDir)
	}
	if cfg.TokenMapPath != "" {
		t.Fatalf("TokenMapPath 默认应为空串（可空）, got %q", cfg.TokenMapPath)
	}

	cfg2, err := reqlogConfigFromEnv(envFrom(map[string]string{
		"XM_REQLOG_MODE":     "file",
		"XM_REQLOG_DATA_DIR": "/custom/data/dir",
		"XM_REQLOG_TOKENMAP": "/custom/tokenmap.json",
	}))
	if err != nil {
		t.Fatalf("reqlogConfigFromEnv: %v", err)
	}
	if cfg2.DataDir != "/custom/data/dir" {
		t.Fatalf("DataDir = %q", cfg2.DataDir)
	}
	if cfg2.TokenMapPath != "/custom/tokenmap.json" {
		t.Fatalf("TokenMapPath = %q", cfg2.TokenMapPath)
	}
}

func TestNewReqlogFileClientRequiresDataDir(t *testing.T) {
	if _, err := newReqlogFileClient(reqlogConfig{Mode: reqlogModeFile, DataDir: ""}, quietLogger()); err == nil {
		t.Fatal("DataDir 为空应在启动时就被拒绝，而不是留到第一次请求才发现")
	}
	if _, err := newReqlogFileClient(reqlogConfig{Mode: reqlogModeFile, DataDir: "   "}, quietLogger()); err == nil {
		t.Fatal("只有空白的 DataDir 同样应被拒绝")
	}
}

func TestNewRequestLogServiceFileMode(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "reqlog-data")
	svc, err := newRequestLogService(
		reqlogConfig{Mode: reqlogModeFile, DataDir: dataDir},
		"production", nil, quietLogger())
	if err != nil {
		t.Fatalf("file 模式配置齐备应构造成功: %v", err)
	}
	if svc == nil {
		t.Fatal("配置齐备时不该返回 nil")
	}
}

func TestFileModeIsAllowedInProduction(t *testing.T) {
	// 与 fake 不同：file 读的是记录代理落盘的真实数据，不是编造的样本，
	// 因此**不在**"生产禁用"名单里——这条测试钉住这个区别，防止有人
	// 顺手把 file 也加进 fake 的生产拒绝分支。
	dataDir := filepath.Join(t.TempDir(), "reqlog-data")
	if _, err := newRequestLogService(
		reqlogConfig{Mode: reqlogModeFile, DataDir: dataDir},
		"production", nil, quietLogger()); err != nil {
		t.Fatalf("file 模式在生产环境应该被允许: %v", err)
	}
}

func TestNewRequestLogServiceFileModeMissingDataDirFails(t *testing.T) {
	if _, err := newRequestLogService(
		reqlogConfig{Mode: reqlogModeFile, DataDir: ""},
		"staging", nil, quietLogger()); err == nil {
		t.Fatal("缺少 DataDir 应该启动即拒")
	}
}
