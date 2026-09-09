package cards

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 卡片用途登记：绑定账号、订阅服务、下次续费日期。这些上游一个都不知道，
// 全是平台自己的登记数据。

func TestSetUsageRejectsUnknownAccount(t *testing.T) {
	svc, _, _ := twoAccountService(newMemStore())

	err := svc.SetCardUsage(context.Background(), CardUsage{
		Account: "TYPO", CardID: "card_1", BoundAccount: "a@b.com", BoundAccountKind: "email",
	})
	if !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("错误 = %v, want ErrUnknownAccount", err)
	}
}

// 标识类型是枚举：显式记录而不是靠「长得像邮箱就是邮箱」猜——
// 有些服务的账号是用户名或手机号，猜错了以后按账号检索会漏。
func TestSetUsageValidatesBoundAccountKind(t *testing.T) {
	svc, _, _ := twoAccountService(newMemStore())

	for _, kind := range []string{"", "email", "username", "phone", "other"} {
		if err := svc.SetCardUsage(context.Background(), CardUsage{
			Account: "main", CardID: "card_1", BoundAccountKind: kind,
		}); err != nil {
			t.Fatalf("%q 应被接受: %v", kind, err)
		}
	}
	if err := svc.SetCardUsage(context.Background(), CardUsage{
		Account: "main", CardID: "card_1", BoundAccountKind: "e-mail",
	}); err == nil {
		t.Fatal("拼错的标识类型必须被拒")
	}
}

// 续费日期必须是 YYYY-MM-DD。拼错的日期不能静默丢掉——那会让一张
// 「本以为设了提醒」的卡在续费日当天悄悄扣不上。
func TestSetUsageValidatesRenewalDate(t *testing.T) {
	svc, _, _ := twoAccountService(newMemStore())

	if err := svc.SetCardUsage(context.Background(), CardUsage{
		Account: "main", CardID: "card_1", NextRenewalOn: "2026-10-01",
	}); err != nil {
		t.Fatalf("合法日期应被接受: %v", err)
	}
	if err := svc.SetCardUsage(context.Background(), CardUsage{
		Account: "main", CardID: "card_1", NextRenewalOn: "",
	}); err != nil {
		t.Fatalf("空日期表示「不是订阅」，应被接受: %v", err)
	}
	for _, bad := range []string{"2026/10/01", "10-01-2026", "2026-13-01", "下个月"} {
		if err := svc.SetCardUsage(context.Background(), CardUsage{
			Account: "main", CardID: "card_1", NextRenewalOn: bad,
		}); err == nil {
			t.Fatalf("非法日期 %q 必须被拒", bad)
		}
	}
}

// 续费风险：续费日快到了而卡上没钱，订阅会直接掉。这是这个功能真正的
// 用处——不是记个日期好看，是提前看见「这张卡续不上」。
func TestRenewalRisk(t *testing.T) {
	today := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		renewal   string
		balance   int64
		wantLevel RenewalRiskLevel
	}{
		{"没登记续费日期", "", 0, RenewalRiskNone},
		{"还早，余额也够", "2026-12-01", 5000, RenewalRiskNone},
		{"还早，但没钱", "2026-12-01", 0, RenewalRiskNone},
		{"七天内，余额够", "2026-09-08", 5000, RenewalRiskSoon},
		{"七天内且没钱——最要紧的一档", "2026-09-08", 0, RenewalRiskUnfunded},
		{"就是今天且没钱", "2026-09-04", 0, RenewalRiskUnfunded},
		{"已经过期了", "2026-09-01", 0, RenewalRiskOverdue},
		{"过期但有钱（可能已经扣过了）", "2026-09-01", 5000, RenewalRiskOverdue},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenewalRisk(tc.renewal, tc.balance, today)
			if got != tc.wantLevel {
				t.Fatalf("RenewalRisk(%q, %d) = %q, want %q", tc.renewal, tc.balance, got, tc.wantLevel)
			}
		})
	}
}

// 日期解析不了时按「没登记」处理而不是报错：这个函数在渲染路径上，
// 一条脏数据不该让整页崩掉。校验在写入那一侧做。
func TestRenewalRiskTreatsUnparseableDateAsNone(t *testing.T) {
	today := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.UTC)

	if got := RenewalRisk("下个月", 0, today); got != RenewalRiskNone {
		t.Fatalf("脏数据应按没登记处理, got %q", got)
	}
}
