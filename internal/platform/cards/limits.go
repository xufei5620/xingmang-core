// Package cards 是 Infini 卡服务的领域层。
//
// 边界：connectors/infini 只管说 HTTP，本包管「什么时候可以说、说完怎么记」——
// 限额、幂等台账、投影、Action。花真钱的操作全部经这里，不允许调用方
// 直接拿连接器发写请求。
package cards

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

var (
	// ErrPerOperationExceeded：单笔金额超过上限。
	ErrPerOperationExceeded = errors.New("cards: 单笔金额超过上限")
	// ErrDailyExceeded：加上今天已花的金额会超过单日上限。
	ErrDailyExceeded = errors.New("cards: 单日金额超过上限")
	// ErrLimitsUnconfigured：上限没配齐。
	ErrLimitsUnconfigured = errors.New("cards: 金额上限未配置")
	// ErrAmountInvalid：金额非正或无法解析。
	ErrAmountInvalid = errors.New("cards: 金额非法")
)

// limitScale 是限额比较使用的标度。
//
// 配置值与请求值用**同一个**标度解析，所以比较是自洽的。这个数字不主张
// 上游的金额单位是什么——那件事尚未验证（见 contracts/connectors/
// infini.card.v1.md 的验证清单），而限额比较也不需要知道：两边同标度即可。
//
// 同理，限额以申请金额本身的单位（token，USDT/USDC）计，**不做汇率换算**。
// 「1 USDT = 1 USD」这种假设一旦写进代码就再也没人会去质疑它。
const limitScale = 6

// LimitUnlimited 是显式声明「这一档不设限」的配置值。
//
// 为什么要一个关键字，而不是把「没配」当成不限：**忘了配和故意不限必须
// 是两件事**。空配置继续 fail closed，否则哪天配置掉了，护栏会安静地消失，
// 而看代码的人以为它还在。
//
// 产品负责人 2026-09-04 的判断：内部自用场景下 Infini 账户余额本身就是硬顶，
// 充值还可以 redeem 拉回来，单日累计上限意义不大。这个关键字让那个判断
// 变成配置里写着的一句话，而不是一个大到看不出意图的数字。
const LimitUnlimited = "unlimited"

// Limits 是花钱操作的金额护栏。
//
// 两个字段都是十进制文本，与请求金额同源同标度；也可以是 LimitUnlimited，
// 表示这一档显式不设限。两档独立——常见配法是留住单笔上限当手滑挡板，
// 同时不限单日累计。
type Limits struct {
	// PerOperation 是单笔上限。
	PerOperation string
	// PerDay 是单日累计上限。
	PerDay string
}

// unlimited 判断一个配置值是不是显式的「不设限」。
//
// 容忍大小写与空白，因为它是人手填进管理端/环境变量的；但不容忍拼写错误——
// 「unlimted」解析不出数字，会落到 ErrLimitsUnconfigured，也就是 fail closed。
func unlimited(v string) bool {
	return strings.EqualFold(strings.TrimSpace(v), LimitUnlimited)
}

// Check 校验一笔金额是否被放行。
//
// spentToday 是今天已经花掉的累计金额（同单位十进制文本），由调用方从
// 操作台账里算出来——包含处于 unknown 状态的操作，因为那些**可能真的花掉了**，
// 把它们当没花过会让上限在最需要生效的时候失效。
func (l Limits) Check(amount, spentToday string) error {
	// 上限没配 = 没有护栏。空配置绝不能等于「随便花」。
	if l.PerOperation == "" || l.PerDay == "" {
		return fmt.Errorf("%w（单笔=%q 单日=%q）", ErrLimitsUnconfigured, l.PerOperation, l.PerDay)
	}

	perOpUnlimited, perDayUnlimited := unlimited(l.PerOperation), unlimited(l.PerDay)

	var perOp, perDay int64
	if !perOpUnlimited {
		v, err := money.ParseMinorUnits(l.PerOperation, limitScale)
		if err != nil {
			return fmt.Errorf("单笔上限 %q 无法解析: %w", l.PerOperation, ErrLimitsUnconfigured)
		}
		perOp = v
	}
	if !perDayUnlimited {
		v, err := money.ParseMinorUnits(l.PerDay, limitScale)
		if err != nil {
			return fmt.Errorf("单日上限 %q 无法解析: %w", l.PerDay, ErrLimitsUnconfigured)
		}
		perDay = v
	}

	// 精度超出比较标度的金额直接拒绝，**不四舍五入后再比**：
	// 校验的值必须就是发出去的值。舍掉的那几位会让「校验通过的金额」
	// 与「实际发给上游的文本」不是同一个数。
	if err := requirePrecisionWithinScale(amount); err != nil {
		return err
	}

	requested, err := money.ParseMinorUnits(amount, limitScale)
	if err != nil {
		return fmt.Errorf("%w: 金额 %q 无法解析", ErrAmountInvalid, amount)
	}
	if requested <= 0 {
		return fmt.Errorf("%w: 金额 %q 非正", ErrAmountInvalid, amount)
	}

	// 「不设限」只关掉比大小这一步，上面的金额合法性校验照跑：
	// 金额非正或解析不出来是调用方错了，跟限额松紧无关。
	if !perOpUnlimited && requested > perOp {
		return fmt.Errorf("%w：本次 %s，上限 %s", ErrPerOperationExceeded, amount, l.PerOperation)
	}
	if !perDayUnlimited {
		spent, err := money.ParseMinorUnits(spentToday, limitScale)
		if err != nil {
			return fmt.Errorf("%w: 今日累计 %q 无法解析", ErrAmountInvalid, spentToday)
		}
		if spent+requested > perDay {
			return fmt.Errorf("%w：今日已用 %s，本次 %s，上限 %s",
				ErrDailyExceeded, spentToday, amount, l.PerDay)
		}
	}
	return nil
}

// requirePrecisionWithinScale 拒绝小数位多于 limitScale 的金额。
//
// 只做文本判断，不经任何数值转换：多一位小数就是一个我们无法如实校验的值。
// 科学计数法一并拒绝——上游要的是十进制文本，而 1e-8 这种写法既难校验
// 也难在审计里读。
func requirePrecisionWithinScale(amount string) error {
	s := strings.TrimSpace(amount)
	if strings.ContainsAny(s, "eE") {
		return fmt.Errorf("%w: 金额 %q 不接受科学计数法", ErrAmountInvalid, amount)
	}
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return nil
	}
	if decimals := len(s) - dot - 1; decimals > limitScale {
		return fmt.Errorf("%w: 金额 %q 的小数位 %d 超出可校验精度 %d",
			ErrAmountInvalid, amount, decimals, limitScale)
	}
	return nil
}
