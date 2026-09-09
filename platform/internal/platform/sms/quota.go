package sms

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 消费者配额（ADR-022，XM-SMS4 #3）。
//
// 机器身份要号是花真钱：一个循环里的 bug 能在十分钟内买光余额，而且没有人在
// 页面前面点确认。所以机器**必须先被登记**——没有配额行就不许调用，与「新装
// 环境两家默认都是关的」同一条纪律（宪法 26 条：天生不允许花钱，必须显式打开）。
//
// 人不受这套约束：人有自己的权限闸（sms.purchase 不给 admin）与页面上的两步
// 确认，再叠一层日配额只会让运营在最忙的那天被自己的护栏挡住。

var (
	// ErrQuotaNotRegistered：这个机器身份没有配额行（或被停用）。
	ErrQuotaNotRegistered = errors.New("sms: 该消费者未登记配额")
	// ErrQuotaExceeded：今天的请求数或花费已经到顶。
	ErrQuotaExceeded = errors.New("sms: 超出今日配额")
	// ErrQuotaInvalid：配额配置不合法。
	ErrQuotaInvalid = errors.New("sms: 配额配置不合法")
)

// ConsumerQuota 是一个消费者（机器身份）的日限额。
type ConsumerQuota struct {
	// Consumer 是 principal ID，与审计里那一列同源——出事时「谁买的」要能对上。
	Consumer string
	// DailyRequests 是每天最多能要多少个号。**按号数算而不是按调用次数**：
	// 一次要 50 个与 50 次要一个，花的钱一样多。0 = 一次都不许（保留登记但停掉）。
	DailyRequests int
	// DailySpendCapText 是每天的花费上限，十进制文本；空 = 不限。
	//
	// **按币种各自计**：两家的币种不同且不折算，一个跨币种的总额上限是个算不
	// 出来的数。
	DailySpendCapText string
	Enabled           bool
	UpdatedAt         time.Time
}

// SetConsumerQuota 配一个消费者的日限额。
func (s *Service) SetConsumerQuota(ctx context.Context, q ConsumerQuota) (ConsumerQuota, error) {
	q.Consumer = strings.TrimSpace(q.Consumer)
	q.DailySpendCapText = strings.TrimSpace(q.DailySpendCapText)
	switch {
	case q.Consumer == "":
		return ConsumerQuota{}, fmt.Errorf("%w：消费者（principal ID）不能为空", ErrQuotaInvalid)
	case q.DailyRequests < 0:
		return ConsumerQuota{}, fmt.Errorf("%w：日配额不能为负（0 表示一次都不许）", ErrQuotaInvalid)
	}
	if q.DailySpendCapText != "" && !unitPricePattern.MatchString(q.DailySpendCapText) {
		return ConsumerQuota{}, fmt.Errorf("%w：花费上限要是十进制数字（如 5.00），留空表示不限", ErrQuotaInvalid)
	}
	q.UpdatedAt = s.now()
	if err := s.store.SaveConsumerQuota(ctx, q); err != nil {
		return ConsumerQuota{}, err
	}
	return q, nil
}

// RemoveConsumerQuota 取消登记：之后这个消费者一次都调不动。
func (s *Service) RemoveConsumerQuota(ctx context.Context, consumer string) error {
	return s.store.RemoveConsumerQuota(ctx, strings.TrimSpace(consumer))
}

// ListConsumerQuotas 列出已登记的消费者。
func (s *Service) ListConsumerQuotas(ctx context.Context) ([]ConsumerQuota, error) {
	return s.store.ListConsumerQuotas(ctx)
}

// consumerOf 取调用方的机器身份。人返回空串——人不受配额约束。
func consumerOf(ctx context.Context) string {
	p, ok := principal.FromContext(ctx)
	if !ok || !p.IsMachine() {
		return ""
	}
	return p.ID
}

// checkQuota 在**打上游之前**检查配额。人直接放行。
//
// wantNumbers 是这次要的号数：按号数算而不是按调用次数，一次要 50 个与 50 次
// 要一个花的钱一样多。
func (s *Service) checkQuota(ctx context.Context, wantNumbers int) error {
	consumer := consumerOf(ctx)
	if consumer == "" {
		return nil
	}
	quota, ok, err := s.store.GetConsumerQuota(ctx, consumer)
	if err != nil {
		return err
	}
	if !ok || !quota.Enabled {
		return fmt.Errorf("%w：%s（到管理后台「接入配额」登记后才能调用）", ErrQuotaNotRegistered, consumer)
	}

	dayStart := s.now().UTC().Truncate(24 * time.Hour)
	used, err := s.store.ConsumerUsageSince(ctx, consumer, dayStart)
	if err != nil {
		return err
	}
	if used.Numbers+wantNumbers > quota.DailyRequests {
		return fmt.Errorf("%w：%s 今天已要 %d 个号，日配额 %d，这次还要 %d 个",
			ErrQuotaExceeded, consumer, used.Numbers, quota.DailyRequests, wantNumbers)
	}
	if quota.DailySpendCapText == "" {
		return nil
	}
	cap, okCap := parseDecimal(quota.DailySpendCapText)
	if !okCap {
		return nil
	}
	// 花费按币种各自比：不折算，所以任何一个币种超了就算超。
	for _, row := range used.SpendByCurrency {
		spent, okSpent := parseSignedDecimal(row.SumText)
		if !okSpent || spent.Sign() <= 0 {
			continue
		}
		if spent.Cmp(cap) >= 0 {
			return fmt.Errorf("%w：%s 今天已花 %s%s，上限 %s",
				ErrQuotaExceeded, consumer, ratText(spent), currencySuffix(row.Currency), quota.DailySpendCapText)
		}
	}
	return nil
}

// ConsumerUsage 是某个消费者今天已经用掉的量。
type ConsumerUsage struct {
	// Numbers 是已经要到的号数（按操作上记的数量算）。
	Numbers int
	// SpendByCurrency 是按币种分的花费。**不折算**。
	SpendByCurrency []CostSummary
}

var _ = big.NewRat
