package main

import "testing"

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func baseCardsEnv() map[string]string {
	return map[string]string{
		"XM_CARDS_MODE":                     "fake",
		"XM_CARDS_ACCOUNTS":                 "MAIN",
		"XM_CARDS_MAIN_LIMIT_PER_OPERATION": "unlimited",
		"XM_CARDS_MAIN_LIMIT_PER_DAY":       "unlimited",
	}
}

// 提现额度**不再来自环境变量**（2026-09-05 改成库内配置、后台可调）。
//
// 留这条断言而不是把整个文件删掉：一个还在读 XM_CARDS_*_WITHDRAW_LIMIT_*
// 的进程会让人以为在 .env 里改那两行有用，而实际上改了没有任何效果——
// 「配置看起来生效了其实没有」比「配置项不存在」难查得多。
func TestWithdrawLimitsNoLongerComeFromEnv(t *testing.T) {
	env := baseCardsEnv()
	env["XM_CARDS_MAIN_WITHDRAW_LIMIT_PER_OPERATION"] = "500"
	env["XM_CARDS_MAIN_WITHDRAW_LIMIT_PER_DAY"] = "2000"

	// 既不报错（旧 .env 里留着这两行不该拦住启动），也不生效。
	cfg, err := loadCardsConfig(envFunc(env))
	if err != nil {
		t.Fatalf("旧的提现额度环境变量不该拦住启动: %v", err)
	}
	if len(cfg.Accounts) != 1 {
		t.Fatalf("账号解析不对: %+v", cfg.Accounts)
	}
}
