package finance

// 上游余额的领域类型（XM-0037d，设计稿 §2.3 + §7）。
//
// 余额在本模块里是**唯一一个不参与成本核算的量**（§2.3 逐字：「仅用于可用天数
// 预警，不参与成本」）。理由值得记住：余额差分会被充值污染——正跳变是充值
// 不是负成本，SoloAI 正因此不用它算成本。它只做 §10.4 的可用天数分子。

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// ErrBalanceUnchanged：本次读数与库里最新那一行相同，因而只刷新了观测时刻。
//
// 它**不是失败**：一个一周没动过的余额每轮都会走到这里，那正是
// §7「仅变化时落一条」要的行为。调用方计一个「已确认」而不是「已写入」。
var ErrBalanceUnchanged = errors.New("finance: 余额未变化，只刷新观测时刻")

// BalanceReading 是余额历史的一行（§2.3）。
//
// 它是一段**游程**而不是一个时点快照：CapturedAt..ObservedAt 是这个余额值
// 持续的区间。两个时刻分开的理由见 000011 迁移里 observed_at 那一列的注释，
// 一句话：只有 CapturedAt 的话，一个健康账号的余额一周没动，
// 看板会把它判成「观测已过期」——把正常状态显示成故障。
type BalanceReading struct {
	ID                int64
	UpstreamAccountID uuid.UUID

	// BalanceMinor 是余额，整数最小单位 @ money.MicroScale。**可以为负**
	// （上游允许透支时），而那正是最该报警的时刻。
	BalanceMinor int64
	Currency     string

	// CapturedAt 是这个值**第一次**被看到的时刻；
	// ObservedAt 是最近一次**确认**它还是这个值的时刻。
	//
	// 新鲜度看 ObservedAt，「余额什么时候变的」看 CapturedAt。
	CapturedAt time.Time
	ObservedAt time.Time

	// Source 是读到它的采集来源标识（同 ProfitRow.Source，宪法 12 条）。
	Source string
}

// Age 返回这条读数距某个时刻有多旧（按 ObservedAt 算）。
//
// 按 ObservedAt 而不是 CapturedAt——这是本类型最容易用错的地方：
// 用 CapturedAt 会把「余额稳定」误判成「数据陈旧」。
func (b BalanceReading) Age(now time.Time) time.Duration {
	return now.Sub(b.ObservedAt)
}

// SameValueAs 报告两条读数是不是同一个余额（值 + 币种）。
//
// 币种也要比：一个从 USD 改成 CNY 的账号，金额数字可能恰好没变，
// 但那绝不是「余额没变化」——它是一次口径变更，必须落成新的一行。
func (b BalanceReading) SameValueAs(other BalanceReading) bool {
	return b.BalanceMinor == other.BalanceMinor && b.Currency == other.Currency
}

// Validate 校验一条余额读数的领域不变量（与 000011 的 CHECK 同一套规则）。
func (b BalanceReading) Validate() error {
	if b.UpstreamAccountID == uuid.Nil {
		return fmt.Errorf("upstream_account_id: %w", ErrMissingField)
	}
	if err := validateAmountCurrency(b.Currency); err != nil {
		return err
	}
	if strings.TrimSpace(b.Source) == "" {
		// 无来源的余额等于一个裸数字（规格 §9.1、宪法 12 条）
		return fmt.Errorf("source: %w", ErrMissingField)
	}
	if b.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at: %w", ErrMissingField)
	}
	if !b.CapturedAt.IsZero() && b.ObservedAt.Before(b.CapturedAt) {
		return fmt.Errorf("observed_at %s 早于 captured_at %s：游程的两端颠倒了: %w",
			b.ObservedAt.Format(time.RFC3339), b.CapturedAt.Format(time.RFC3339),
			ErrInconsistent)
	}
	return nil
}

// 编译期断言：余额的标度就是 money 包声明的那一个（同 ProfitStore 末尾那条）。
var _ = [1]struct{}{}[money.MicroScale-6]
