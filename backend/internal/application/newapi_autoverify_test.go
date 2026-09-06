package application

import (
	"testing"
	"time"
)

// XM-INV-NEWAPI-AUTOVERIFY：自动核验的判据本身。
//
// 这一组调的是**处理器真正用的那个函数**，不是它的副本——判据决定的是"这笔
// 钱算不算数"，写错一个条件的后果是给没到账的钱开发票，而那是开出去收不回的
// 东西。测副本的话，改了实现测试照样绿。

func TestNewAPICandidateSettledCriteria(t *testing.T) {
	completed := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		sourceStatus string
		completedAt  time.Time
		payMinor     int64
		want         bool
		why          string
	}{
		{
			name: "成功且证据齐全", sourceStatus: "success", completedAt: completed, payMinor: 30_000, want: true,
			why: "正常的网关支付：状态成功、有完成时刻、金额为正",
		},
		{
			name: "待支付", sourceStatus: "pending", completedAt: time.Time{}, payMinor: 30_000, want: false,
			why: "钱还没到，绝不能算数",
		},
		{
			name: "支付失败", sourceStatus: "failed", completedAt: completed, payMinor: 30_000, want: false,
			why: "失败的订单即使带了完成时刻也不是收入",
		},
		{
			name: "已过期", sourceStatus: "expired", completedAt: time.Time{}, payMinor: 30_000, want: false,
			why: "过期订单同上",
		},
		{
			name: "成功但没有完成时刻", sourceStatus: "success", completedAt: time.Time{}, payMinor: 30_000, want: false,
			why: "没有完成时刻说明这条记录还没走完，不足以证明结算",
		},
		{
			name: "成功但金额为零", sourceStatus: "success", completedAt: completed, payMinor: 0, want: false,
			why: "零元充值没有可开票的钱；放过去会造出一张 0 元的可开票批次",
		},
		{
			name: "成功但金额为负", sourceStatus: "success", completedAt: completed, payMinor: -1, want: false,
			why: "负数只可能来自上游异常，不能当成收入",
		},
		{
			name: "大小写不同的成功", sourceStatus: "SUCCESS", completedAt: completed, payMinor: 30_000, want: false,
			why: "New API 的常量是小写 success（common/constants.go 只有 " +
				"pending/success/failed/expired 四个）；放宽大小写等于替上游猜它没说过的话",
		},
		{
			name: "空状态", sourceStatus: "", completedAt: completed, payMinor: 30_000, want: false,
			why: "上游没给状态时保持待核验，不猜",
		},
	} {
		if got := newAPICandidateSettled(tc.sourceStatus, tc.completedAt, tc.payMinor); got != tc.want {
			t.Errorf("%s：判定 %v，应为 %v —— %s", tc.name, got, tc.want, tc.why)
		}
	}
}
