package sms

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 内部告警（ADR-022，XM-SMS2 #8）。
//
// **一律不外发。** 条件只落成事件与页面红条；投递（企业微信 / Webhook）等另一条
// 线的通知规范定稿后再适配——现在自己发明一套投递，等规范来了就是两套。
//
// 三个条件都是「钱或号已经出问题、但没人会主动去看」的那类事实：
//
//	余额低于阈值   → 再买就会失败，而失败发生在有人等着一个号的时候
//	unknown 超时   → 不知道钱花没花出去，越久越难查
//	租用号快到期   → 租用按小时计费，过期就白花了
const (
	AlertBalanceLow   = "balance_low"
	AlertUnknownStale = "operation_unknown_stale"
	AlertRentExpiring = "rent_expiring"
)

// 严重度只有两档：要不要**现在**去做点什么。
const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

const (
	// DefaultUnknownGrace 是 unknown 的宽限期。
	//
	// 与卡片的不确定态同一个数（30 分钟）：刚发生的 unknown 常常几分钟内就被
	// 下一轮同步收敛掉了，更短的宽限只会把「上游慢了一点」变成一次人工介入。
	DefaultUnknownGrace = 30 * time.Minute

	// DefaultRentExpiryLead 是租用号到期的提前量。
	//
	// 一小时：租用按小时计费且可延长，提前一小时提醒，人还来得及决定续不续。
	// 普通激活号 20 分钟就过期，那是常态，不在这条规则里（见 EvaluateAlerts）。
	DefaultRentExpiryLead = time.Hour
)

// ErrAlertConfigInvalid：阈值配置不合法。
var ErrAlertConfigInvalid = errors.New("sms: 告警阈值不合法")

// BalanceThreshold 是某一家的余额下限。
//
// **按家配、按各家自己的币种比较、不折算**：62 是 USD，Hero 是账户币种，
// 一个通用的数字在这里没有意义。没配 = 不判这家的余额（不替运营决定「多少算少」）。
type BalanceThreshold struct {
	Provider string
	// MinAmountText 是十进制文本。低于它（严格小于）才报警。
	MinAmountText string
	UpdatedAt     time.Time
}

// AlertEvent 是一条内部告警。
//
// Fingerprint 是去重键：每轮巡检都会重新评估，同一条件在被处理之前会被算出
// 很多次；每次插一行会让页面变成一串同样的红条，然后人开始忽略它们。
type AlertEvent struct {
	ID       string
	Kind     string
	Provider string
	// Subject 是这条告警指向的东西：操作 ID、号码 ID，或者供应商自己。
	Subject     string
	Severity    string
	Summary     string
	Fingerprint string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ResolvedAt  time.Time
}

// Open 说明这条告警还没消失。
func (e AlertEvent) Open() bool { return e.ResolvedAt.IsZero() }

// SetBalanceThreshold 配一家的余额下限。**空值 = 清掉**，不是「阈值为 0」——
// 后者会让每一次余额归零都报警，而那时报警已经没用了。
func (s *Service) SetBalanceThreshold(ctx context.Context, provider, minAmount string) (BalanceThreshold, error) {
	provider = strings.TrimSpace(provider)
	if _, ok := s.providers[provider]; !ok {
		return BalanceThreshold{}, fmt.Errorf("%w：供应商 %s 未装配", ErrAlertConfigInvalid, provider)
	}
	minAmount = strings.TrimSpace(minAmount)
	if minAmount == "" {
		if err := s.store.RemoveBalanceThreshold(ctx, provider); err != nil {
			return BalanceThreshold{}, err
		}
		return BalanceThreshold{Provider: provider}, nil
	}
	if !unitPricePattern.MatchString(minAmount) {
		return BalanceThreshold{}, fmt.Errorf("%w：下限要是十进制数字（如 5.00），不带符号与币种；留空表示不判这家", ErrAlertConfigInvalid)
	}
	threshold := BalanceThreshold{Provider: provider, MinAmountText: minAmount, UpdatedAt: s.now()}
	if err := s.store.SaveBalanceThreshold(ctx, threshold); err != nil {
		return BalanceThreshold{}, err
	}
	return threshold, nil
}

// ListBalanceThresholds 列出已配的下限。
func (s *Service) ListBalanceThresholds(ctx context.Context) ([]BalanceThreshold, error) {
	return s.store.ListBalanceThresholds(ctx)
}

// ListOpenAlerts 列出还没消失的告警，给页面的红条。
func (s *Service) ListOpenAlerts(ctx context.Context) ([]AlertEvent, error) {
	return s.store.ListOpenAlertEvents(ctx)
}

// EvaluateAlerts 评估一轮，返回这一轮**正在报**的告警。
//
// 全量评估：算出当前该报的全部指纹，逐条 upsert（已有的只更新 last_seen_at），
// 然后把不在这一批里的开着的事件收敛掉。条件消失就自动关，不用人手动点——
// 一个要人手动关的红条，最后总会留着一堆没人关的旧条。
//
// **不外发**：这里没有 notifier 的调用，也不该有。
func (s *Service) EvaluateAlerts(ctx context.Context) ([]AlertEvent, error) {
	now := s.now()
	firing := make([]AlertEvent, 0, 4)

	balanceEvents, err := s.evaluateBalances(ctx, now)
	if err != nil {
		return nil, err
	}
	firing = append(firing, balanceEvents...)

	staleOps, err := s.store.ListStaleUnknownOperations(ctx, now.Add(-DefaultUnknownGrace))
	if err != nil {
		return nil, err
	}
	for _, op := range staleOps {
		firing = append(firing, AlertEvent{
			Kind: AlertUnknownStale, Provider: op.Provider, Subject: op.ID,
			// critical：它的含义是「不知道钱花没花出去」，而那是唯一需要人
			// 立刻做点什么的状态。
			Severity: SeverityCritical,
			Summary: fmt.Sprintf("%s 的 %s 操作结果未知已超过 %s，需要到供应商侧核对（上游引用 %s）",
				Label(op.Provider), op.Kind, DefaultUnknownGrace, fallbackDash(op.ProviderRef)),
			Fingerprint: alertFingerprint(AlertUnknownStale, op.Provider, op.ID),
		})
	}

	expiring, err := s.store.ListExpiringRentals(ctx, now, now.Add(DefaultRentExpiryLead))
	if err != nil {
		return nil, err
	}
	for _, r := range expiring {
		firing = append(firing, AlertEvent{
			Kind: AlertRentExpiring, Provider: r.Provider, Subject: r.ID,
			Severity: SeverityWarning,
			Summary: fmt.Sprintf("%s 的租用号 %s 将在 %s 到期，续租或放弃都要现在决定",
				Label(r.Provider), fallbackDash(r.PhoneMask), r.ExpiresAt.UTC().Format(time.RFC3339)),
			Fingerprint: alertFingerprint(AlertRentExpiring, r.Provider, r.ID),
		})
	}

	sort.Slice(firing, func(i, j int) bool { return firing[i].Fingerprint < firing[j].Fingerprint })
	fingerprints := make([]string, 0, len(firing))
	for i := range firing {
		firing[i].FirstSeenAt = now
		firing[i].LastSeenAt = now
		id, err := s.store.UpsertAlertEvent(ctx, firing[i])
		if err != nil {
			return nil, err
		}
		firing[i].ID = id
		fingerprints = append(fingerprints, firing[i].Fingerprint)
	}
	if _, err := s.store.ResolveAlertEventsNotIn(ctx, fingerprints, now); err != nil {
		return nil, err
	}
	return firing, nil
}

func (s *Service) evaluateBalances(ctx context.Context, now time.Time) ([]AlertEvent, error) {
	thresholds, err := s.store.ListBalanceThresholds(ctx)
	if err != nil {
		return nil, err
	}
	if len(thresholds) == 0 {
		return nil, nil
	}
	snapshots, err := s.store.LatestBalanceSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]BalanceSnapshot, len(snapshots))
	for _, snap := range snapshots {
		latest[snap.Provider] = snap
	}

	out := make([]AlertEvent, 0, len(thresholds))
	for _, threshold := range thresholds {
		snap, ok := latest[threshold.Provider]
		if !ok {
			// 还没巡到过这家：没有余额可比。**不报警**——「没有数据」不是
			// 「没有钱」，把它报成余额不足会让人去充一笔不需要的钱。
			continue
		}
		amount, okAmount := parseDecimal(snap.AmountText)
		min, okMin := parseDecimal(threshold.MinAmountText)
		if !okAmount || !okMin {
			continue
		}
		// 十进制比较，不过 float：0.1 + 0.2 那类误差在阈值边界上会变成
		// 「报了又不报」的抖动。
		if amount.Cmp(min) >= 0 {
			continue
		}
		out = append(out, AlertEvent{
			Kind: AlertBalanceLow, Provider: threshold.Provider, Subject: threshold.Provider,
			Severity: SeverityWarning,
			Summary: fmt.Sprintf("%s 余额 %s%s 低于阈值 %s（抓取于 %s）",
				Label(threshold.Provider), snap.AmountText, currencySuffix(snap.Currency),
				threshold.MinAmountText, snap.TakenAt.UTC().Format(time.RFC3339)),
			Fingerprint: alertFingerprint(AlertBalanceLow, threshold.Provider, threshold.Provider),
		})
	}
	return out, nil
}

// alertFingerprint 是去重键：同一条件在被处理之前反复评估只更新同一行。
func alertFingerprint(kind, provider, subject string) string {
	return kind + "|" + provider + "|" + subject
}

func currencySuffix(currency string) string {
	if strings.TrimSpace(currency) == "" {
		return ""
	}
	return " (" + currency + ")"
}

func fallbackDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}
