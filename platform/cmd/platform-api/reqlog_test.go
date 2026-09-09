package main

import (
	"errors"

	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
)

func envFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestParseReqlogModeDefaultsToOff(t *testing.T) {
	// 默认 off 而不是 fake：这条通道读的是用户与模型的完整对话，
	// 一个默认开着的 fake 会把编造的问答摆进一个长得像真的详情页
	for _, in := range []string{"", "   "} {
		mode, err := parseReqlogMode(in)
		if err != nil || mode != reqlogModeOff {
			t.Fatalf("parseReqlogMode(%q) = %q, %v；应默认 off", in, mode, err)
		}
	}
	for _, in := range []string{"fake", "FAKE", " real ", "off"} {
		if _, err := parseReqlogMode(in); err != nil {
			t.Errorf("parseReqlogMode(%q) 应通过: %v", in, err)
		}
	}
	for _, in := range []string{"production", "true", "demo"} {
		if _, err := parseReqlogMode(in); err == nil {
			t.Errorf("parseReqlogMode(%q) 应被拒绝", in)
		}
	}
}

func TestReqlogConfigFromEnv(t *testing.T) {
	cfg, err := reqlogConfigFromEnv(envFrom(map[string]string{
		"XM_REQLOG_MODE":             "real",
		"XM_REQLOG_ENDPOINT":         "https://reqlog.internal.example",
		"XM_REQLOG_TARGET_ALLOWLIST": " reqlog.internal.example , Backup.Example ,, ",
		"XM_REQLOG_CREDENTIAL_REF":   "secret://reqlog/console",
		"XM_REQLOG_TIMEOUT":          "45s",
	}))
	if err != nil {
		t.Fatalf("reqlogConfigFromEnv: %v", err)
	}
	if cfg.Mode != reqlogModeReal {
		t.Fatalf("Mode = %q", cfg.Mode)
	}
	// allowlist 只做拆分/去空白/转小写，不补全、不推断
	want := []string{"reqlog.internal.example", "backup.example"}
	if len(cfg.TargetAllowlist) != len(want) {
		t.Fatalf("allowlist = %v, want %v", cfg.TargetAllowlist, want)
	}
	for i, h := range want {
		if cfg.TargetAllowlist[i] != h {
			t.Fatalf("allowlist[%d] = %q, want %q", i, cfg.TargetAllowlist[i], h)
		}
	}
	if cfg.Timeout != 45*time.Second {
		t.Fatalf("Timeout = %v", cfg.Timeout)
	}

	// 缺省超时不能变成「没有超时」（规格 §18.1-4）
	bare, err := reqlogConfigFromEnv(envFrom(map[string]string{"XM_REQLOG_MODE": "fake"}))
	if err != nil {
		t.Fatalf("reqlogConfigFromEnv: %v", err)
	}
	if bare.Timeout != defaultReqlogTimeout {
		t.Fatalf("缺省超时 = %v, want %v", bare.Timeout, defaultReqlogTimeout)
	}

	for name, values := range map[string]map[string]string{
		"超时不是时长": {"XM_REQLOG_TIMEOUT": "soon"},
		"超时非正":   {"XM_REQLOG_TIMEOUT": "0s"},
		"模式认不出":  {"XM_REQLOG_MODE": "yes"},
	} {
		if _, err := reqlogConfigFromEnv(envFrom(values)); err == nil {
			t.Errorf("%s：应被拒绝", name)
		}
	}
}

func TestNewRequestLogServiceOffReturnsNil(t *testing.T) {
	// 没配就不挂端点：reqlog 是外挂系统，没部署它的环境该照常起来
	svc, err := newRequestLogService(
		reqlogConfig{Mode: reqlogModeOff}, "development", nil, quietLogger())
	if err != nil {
		t.Fatalf("off 模式不该报错: %v", err)
	}
	if svc != nil {
		t.Fatal("off 模式应返回 nil")
	}
	// 而 nil 必须能变成一个**真正为 nil 的接口值**，否则路由会挂上端点、
	// 一调就 panic（Go 的 typed-nil 坑）
	if requestLogsOrNil(nil) != nil {
		t.Fatal("requestLogsOrNil(nil) 必须是真正的 nil 接口")
	}
}

func TestFakeModeRejectedInProduction(t *testing.T) {
	// 别处的 fake 让看板上几个数字是假的；这里的 fake 会让一个标着真实用户名的
	// 详情页显示编造的对话——有人会拿它去回复客诉、去做风控判断
	_, err := newRequestLogService(
		reqlogConfig{Mode: reqlogModeFake}, "production", nil, quietLogger())
	if err == nil {
		t.Fatal("生产环境的 fake 模式应被拒绝")
	}
	if !strings.Contains(err.Error(), "fake") {
		t.Fatalf("错误信息应说清是 fake 模式的问题: %v", err)
	}
}

func TestRealModeRequiresCompleteConfig(t *testing.T) {
	full := reqlogConfig{
		Mode:            reqlogModeReal,
		Endpoint:        "https://reqlog.internal.example",
		TargetAllowlist: []string{"reqlog.internal.example"},
		CredentialRef:   "secret://reqlog/console",
		Timeout:         30 * time.Second,
	}
	// 三项缺任意一项都启动即拒：让端点带着半套配置起来，症状会是
	// 「点进去报错，查半天发现 allowlist 是空的」
	for name, mutate := range map[string]func(c *reqlogConfig){
		"缺 endpoint":  func(c *reqlogConfig) { c.Endpoint = "" },
		"缺 allowlist": func(c *reqlogConfig) { c.TargetAllowlist = nil },
		"缺凭据引用":       func(c *reqlogConfig) { c.CredentialRef = "" },
	} {
		cfg := full
		mutate(&cfg)
		if _, err := newReqlogClient(cfg, "staging", quietLogger()); err == nil {
			t.Errorf("%s：应被拒绝", name)
		}
	}
	// 凭据引用格式不对也要在启动时发现，而不是第一次请求时
	bad := full
	bad.CredentialRef = "console-user:password"
	if _, err := newReqlogClient(bad, "staging", quietLogger()); err == nil {
		t.Error("非 CredentialRef 形态的凭据应被拒绝（宪法 7 条）")
	}

	if _, err := newReqlogClient(full, "staging", quietLogger()); err != nil {
		t.Fatalf("配置齐备应构造成功: %v", err)
	}
}

func TestRealModeSurfacesLoopbackConflict(t *testing.T) {
	// reqlog 控制台是 http://127.0.0.1:9300（回环明文），与闸 1 的 https 要求
	// 冲突且尚未拍板。配 real 的人第一眼应该看到那条冲突，而不是一句
	// 泛泛的「endpoint 必须是 https」
	_, err := newReqlogClient(reqlogConfig{
		Mode:            reqlogModeReal,
		Endpoint:        "http://127.0.0.1:9300",
		TargetAllowlist: []string{"127.0.0.1"},
		CredentialRef:   "secret://reqlog/console",
		Timeout:         30 * time.Second,
	}, "staging", quietLogger())
	if err == nil {
		t.Fatal("明文回环 endpoint 应被拒绝")
	}
	if !errors.Is(err, reqlog.ErrLoopbackEndpointNotAllowed) {
		t.Fatalf("错误链里应带上那条未拍板的冲突说明: %v", err)
	}
}

func TestConfigFromEnvCarriesReqlogSection(t *testing.T) {
	cfg, err := configFromEnv(envFrom(map[string]string{
		"ENVIRONMENT":    "development",
		"XM_REQLOG_MODE": "fake",
	}))
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if cfg.Reqlog.Mode != reqlogModeFake {
		t.Fatalf("Reqlog.Mode = %q", cfg.Reqlog.Mode)
	}

	// 模式写错必须让进程起不来，而不是静默退回 off——那会让「请求页签消失了」
	// 变成一个没人知道原因的现象
	if _, err := configFromEnv(envFrom(map[string]string{
		"ENVIRONMENT": "development", "XM_REQLOG_MODE": "ture",
	})); err == nil {
		t.Fatal("拼错的 XM_REQLOG_MODE 应让启动失败")
	}
}
