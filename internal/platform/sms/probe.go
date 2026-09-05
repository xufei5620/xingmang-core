package sms

import (
	"context"
	"fmt"
	"time"
)

// 定时巡检（ADR-022，XM-SMS2 #7）：每家做一次只读连接测试，有余额能力的再抓
// 一次余额快照。**这条链一分钱都不花**——没有任何购买调用。
//
// 两件事放在同一个作业里，是因为它们的前提相同（这家开着、凭据能用）而且
// 都只读：连接测试把「凭据还能不能用」变成一个有时间戳的事实（页面上的
// 「已验证」读它），余额快照是给阶段 3 对账用的时间序列——「相邻两次快照的
// 差」与「成本事件之和」对不上，就是上游多扣了或者我们漏记了。

// BalanceSnapshot 是某一时刻某家的余额。
type BalanceSnapshot struct {
	ID       string
	Provider string
	// AmountText 是十进制文本（宪法 13：金额不过 float）。
	AmountText string
	// Currency 是币种。**空 = 上游没说**——Hero 的兼容层 getBalance 只回一个
	// 数字，没有币种；分币种不折算（与卡片同口径），所以宁可空着也不猜。
	Currency string
	TakenAt  time.Time
}

// ProbeResult 是一家的巡检结果。
type ProbeResult struct {
	Provider string
	// Skipped：这家没打上游（运营把它关了）。
	Skipped bool
	// Verified：这一轮的连接测试成功。
	Verified bool
	ClientIP string
	// AmountText 空 = 没读余额（这家没有余额能力，或者读失败）。
	AmountText string
	Currency   string
	// Reason 是跳过或失败的原因。成功且读到余额时为空。
	Reason string
}

// BalanceReader 是「能报余额」的适配器能力。
//
// 注册表里的 CapBalance 是**声明**，这个断言是**事实**：两者不一致时按事实走
// 并把原因写进结果，而不是崩溃——一个装配错误不该让整轮巡检停在第一家。
type BalanceReader interface {
	Balance(ctx context.Context) (string, error)
}

// ProbeOnce 跑一轮巡检。
//
// 返回的 error 只表示**基础设施故障**（快照落库失败），那种失败重试有意义，
// 所以抛给 River。上游失败不抛：一家超时或者密钥过期是可预期的，结论已经写进
// provider_status.last_error，页面上看得见；把它变成任务失败只会让告警变成噪声，
// 也会让同一轮里其他家的结果白跑一遍。
func (s *Service) ProbeOnce(ctx context.Context) ([]ProbeResult, error) {
	results := make([]ProbeResult, 0, len(s.order))
	for _, provider := range s.order {
		result := ProbeResult{Provider: provider}

		status, err := s.store.ProviderStatus(ctx, provider)
		if err != nil {
			return nil, fmt.Errorf("读 %s 的状态: %w", provider, err)
		}
		if !status.Enabled {
			// 运营停掉一家的理由通常是「这家出问题了」或者「先不用它」。
			// 继续打它等于把一个已经决定不用的上游变成每轮一次的噪声。
			result.Skipped = true
			result.Reason = "未启用（运营在后台停用了这家）"
			results = append(results, result)
			continue
		}

		// TestConnection 自己会写 provider_status：成功写 verified_at，
		// 失败只写 last_error 而**不清掉上一次的 verified_at**。
		verified, testErr := s.TestConnection(ctx, provider)
		result.ClientIP = verified.ClientIP
		if testErr != nil {
			result.Reason = "连接测试失败：" + testErr.Error()
			results = append(results, result)
			continue
		}
		result.Verified = verified.Verified()

		snapshot, err := s.snapshotBalance(ctx, provider)
		if err != nil {
			var infra *probeInfraError
			if asProbeInfra(err, &infra) {
				return nil, infra.err
			}
			result.Reason = err.Error()
			results = append(results, result)
			continue
		}
		if snapshot != nil {
			result.AmountText = snapshot.AmountText
			result.Currency = snapshot.Currency
		}
		results = append(results, result)
	}
	return results, nil
}

// probeInfraError 把「落库失败」与「上游失败」分开：前者要让整轮失败并重试，
// 后者只写进这一家的结果。
type probeInfraError struct{ err error }

func (e *probeInfraError) Error() string { return e.err.Error() }

func asProbeInfra(err error, target **probeInfraError) bool {
	if e, ok := err.(*probeInfraError); ok {
		*target = e
		return true
	}
	return false
}

// snapshotBalance 抓一次余额。这家没有余额能力时返回 (nil, nil)——那不是失败，
// 62 就是没有余额接口。
func (s *Service) snapshotBalance(ctx context.Context, provider string) (*BalanceSnapshot, error) {
	spec, ok := Spec(provider)
	if !ok || !spec.Has(CapBalance) {
		return nil, nil
	}
	adapter, err := s.adapter(provider)
	if err != nil {
		return nil, err
	}
	reader, ok := adapter.(BalanceReader)
	if !ok {
		// 注册表说有、适配器没有：装配问题，写进结果让人看见。
		return nil, fmt.Errorf("%w：注册表声明了余额能力但适配器没有实现", ErrExtrasNotSupported)
	}
	amount, err := reader.Balance(ctx)
	if err != nil {
		return nil, fmt.Errorf("读余额失败：%w", err)
	}
	snapshot := BalanceSnapshot{
		Provider:   provider,
		AmountText: amount,
		// 币种留空：Hero 的兼容层不回币种，猜一个 USD 会让阶段 3 的分币种
		// 统计出现一列**看起来正确**的假数据。
		TakenAt: s.now(),
	}
	id, err := s.store.SaveBalanceSnapshot(ctx, snapshot)
	if err != nil {
		return nil, &probeInfraError{err: fmt.Errorf("落余额快照: %w", err)}
	}
	snapshot.ID = id
	return &snapshot, nil
}

// LatestBalances 读每家最新一条快照，给页面显示「上次看到的余额」。
func (s *Service) LatestBalances(ctx context.Context) ([]BalanceSnapshot, error) {
	return s.store.LatestBalanceSnapshots(ctx)
}
