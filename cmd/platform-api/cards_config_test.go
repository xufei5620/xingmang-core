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

// 提现额度是**可选**的，不配就是这个账号不能提现。
//
// 不做成必填，是因为必填会逼着「根本不用提现」的部署去编两个数字，
// 而为了通过校验编出来的数字不构成任何真实约束。不配时留空，
// CheckWithdraw 落到 ErrLimitsUnconfigured，也就是 fail closed。
func TestWithdrawLimitsAreOptionalAndNeverInheritCardLimits(t *testing.T) {
	cfg, err := loadCardsConfig(envFunc(baseCardsEnv()))
	if err != nil {
		t.Fatalf("没配提现额度不该拦住启动: %v", err)
	}
	acct := cfg.Accounts[0]
	// 关键断言：卡片额度是 unlimited，提现额度必须**留空**而不是继承过来。
	// 继承会让提现直接抛 ErrWithdrawLimitsUnbounded，那是个能启动、
	// 但一按就报「额度不许 unlimited」的功能。
	if acct.WithdrawPerOperationLimit != "" || acct.WithdrawPerDayLimit != "" {
		t.Fatalf("提现额度不该有任何默认值, got %q/%q",
			acct.WithdrawPerOperationLimit, acct.WithdrawPerDayLimit)
	}
}

// 配了就读进来，且只读本账号的那一份。
func TestWithdrawLimitsAreReadPerAccount(t *testing.T) {
	env := baseCardsEnv()
	env["XM_CARDS_ACCOUNTS"] = "MAIN,BACKUP"
	env["XM_CARDS_BACKUP_LIMIT_PER_OPERATION"] = "unlimited"
	env["XM_CARDS_BACKUP_LIMIT_PER_DAY"] = "unlimited"
	env["XM_CARDS_MAIN_WITHDRAW_LIMIT_PER_OPERATION"] = "500"
	env["XM_CARDS_MAIN_WITHDRAW_LIMIT_PER_DAY"] = "2000"

	cfg, err := loadCardsConfig(envFunc(env))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].WithdrawPerOperationLimit; got != "500" {
		t.Fatalf("MAIN 单次提现上限 = %q, want 500", got)
	}
	// BACKUP 没配，就不能提现——不许借用 MAIN 的额度。
	if got := cfg.Accounts[1].WithdrawPerDayLimit; got != "" {
		t.Fatalf("BACKUP 没配提现额度却拿到了 %q", got)
	}
}

// 只配一半是配置错误，必须在启动期炸。
//
// 单笔配了、单日留空，会让「单日」落到 ErrLimitsUnconfigured 而整体被拒——
// 行为上是安全的，但表现成「配了却用不了」，人会以为是 bug 去查代码。
func TestWithdrawLimitsRejectHalfConfigured(t *testing.T) {
	env := baseCardsEnv()
	env["XM_CARDS_MAIN_WITHDRAW_LIMIT_PER_OPERATION"] = "500"

	if _, err := loadCardsConfig(envFunc(env)); err == nil {
		t.Fatal("提现额度只配了一半，必须在启动期拒绝")
	}
}

// 提现额度不接受 unlimited——这是与卡片额度最要紧的一处不同。
//
// 卡片那边 unlimited 是产品负责人的明确裁定（余额本身是硬顶）；提现的目的
// 就是把余额搬空。领域层的 CheckWithdraw 已经拒了它，这里再拒一次是为了
// 让人在**部署时**就看见，而不是等到点下提现按钮。
func TestWithdrawLimitsRejectUnlimitedAtStartup(t *testing.T) {
	env := baseCardsEnv()
	env["XM_CARDS_MAIN_WITHDRAW_LIMIT_PER_OPERATION"] = "unlimited"
	env["XM_CARDS_MAIN_WITHDRAW_LIMIT_PER_DAY"] = "2000"

	if _, err := loadCardsConfig(envFunc(env)); err == nil {
		t.Fatal("提现额度填 unlimited 必须在启动期拒绝")
	}
}
