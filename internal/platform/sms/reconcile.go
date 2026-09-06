package sms

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// 余额对账（ADR-022 决策 5，XM-SMS3 #2）。
//
// 两次余额快照之间，余额应当正好掉了这期间成本事件之和。对不上就是两种情况
// 之一：**上游多扣了**，或者**我们漏记了**。两者都要人去看，而没有对账就没人
// 会发现——账面上每一笔都「成功」了，页面上也一切正常。
//
// 只在能下结论时才报。三种「不下结论」的情形：
//
//	只有一张快照      → 没有窗口
//	窗口里有未知金额  → 我们自己的和就是不完整的，报差额等于报自己的无知
//	窗口里混着别的币种 → 跨币种相减得到的数字看起来像个金额，其实什么都不是
const AlertBalanceDrift = "balance_drift"

// DefaultReconcileTolerance 是容差（按各家自己的币种，不折算）。
//
// 一分两分的差多半是上游的四舍五入或一次性小额调整，不值得半夜叫人；比它大的
// 差额则意味着有一笔真金白银没有对应的账。这个数字如果在实际使用中太吵，
// 下一步是把它做成 Action 配的（与余额阈值同一个形状），而不是悄悄调大。
const DefaultReconcileTolerance = "0.05"

// CostSummary 是一个时间窗内某个币种的成本汇总。
type CostSummary struct {
	Currency string
	// SumText 是十进制文本（退款是负数，所以和可能为负）。
	SumText string
	Count   int
	// UnknownCount 是金额为 NULL 的笔数：上游没说花了多少。
	UnknownCount int
}

// reconcileBalance 对一家做一次对账，返回该报的告警（没有就是空）。
func (s *Service) reconcileBalance(ctx context.Context, provider string) ([]AlertEvent, error) {
	snapshots, err := s.store.ListRecentBalanceSnapshots(ctx, provider, 2)
	if err != nil {
		return nil, err
	}
	if len(snapshots) < 2 {
		return nil, nil
	}
	latest, previous := snapshots[0], snapshots[1]

	summaries, err := s.store.SumCostEventsByCurrency(ctx, provider, previous.TakenAt, latest.TakenAt)
	if err != nil {
		return nil, err
	}

	var comparable *CostSummary
	for i := range summaries {
		row := &summaries[i]
		if row.Count == 0 {
			continue
		}
		if row.Currency != latest.Currency {
			// 跨币种不可比：跳过整个窗口而不是只算「同币种那部分」——
			// 少算的那部分会让差额看起来像上游多扣了。
			return nil, nil
		}
		if row.UnknownCount > 0 {
			return nil, nil
		}
		comparable = row
	}

	spent := new(big.Rat)
	if comparable != nil {
		parsed, ok := parseSignedDecimal(comparable.SumText)
		if !ok {
			return nil, nil
		}
		spent = parsed
	}
	before, okBefore := parseSignedDecimal(previous.AmountText)
	after, okAfter := parseSignedDecimal(latest.AmountText)
	if !okBefore || !okAfter {
		return nil, nil
	}

	// 实际变化 = 后 − 前（花钱是负的）。账本预期的变化 = −花掉的。
	// 漂移 = 实际 − 预期；**只报负方向**：正的那边是充值，不是异常。
	actual := new(big.Rat).Sub(after, before)
	expected := new(big.Rat).Neg(spent)
	drift := new(big.Rat).Sub(actual, expected)

	tolerance, _ := parseSignedDecimal(DefaultReconcileTolerance)
	if drift.Sign() >= 0 || new(big.Rat).Neg(drift).Cmp(tolerance) <= 0 {
		return nil, nil
	}

	missing := new(big.Rat).Neg(drift)
	return []AlertEvent{{
		Kind: AlertBalanceDrift, Provider: provider, Subject: provider,
		Severity: SeverityWarning,
		Summary: fmt.Sprintf("%s 余额少了 %s%s 对不上账：%s 到 %s 之间余额从 %s 掉到 %s，而成本事件只记了 %s。要么上游多扣了，要么我们漏记了",
			Label(provider), ratText(missing), currencySuffix(latest.Currency),
			previous.TakenAt.UTC().Format(time.RFC3339), latest.TakenAt.UTC().Format(time.RFC3339),
			previous.AmountText, latest.AmountText, ratText(spent)),
		Fingerprint: alertFingerprint(AlertBalanceDrift, provider, provider),
	}}, nil
}

// parseSignedDecimal 与 parseDecimal 的区别只有一处：**接受负数**。
// 成本事件里的退款是负的，余额差也可以是负的。
func parseSignedDecimal(text string) (*big.Rat, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(text))
	if !ok {
		return nil, false
	}
	return r, true
}

// ratText 把有理数写成最多六位小数的文本（与库里 numeric 的精度一致），
// 去掉尾随的零——「少了 2」比「少了 2.000000」好读。
func ratText(r *big.Rat) string {
	out := r.FloatString(6)
	if !strings.Contains(out, ".") {
		return out
	}
	return strings.TrimSuffix(strings.TrimRight(out, "0"), ".")
}
