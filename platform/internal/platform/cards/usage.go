package cards

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// CardUsage 是一张卡的业务用途登记。
//
// **这些字段上游一个都不知道**：它只回卡本身的状态与余额。这张卡开给谁用、
// 绑在哪个外部服务账号上、订了什么、什么时候续费，全是平台自己记的。
type CardUsage struct {
	Account string
	CardID  string
	// BoundAccount 是这张卡绑定的外部服务账号标识，如 chris@example.com。
	BoundAccount string
	// BoundAccountKind 说明上面那个标识是什么：email / username / phone / other。
	//
	// 显式记录而不是靠「长得像邮箱就是邮箱」猜——有些服务的账号是用户名或
	// 手机号，猜错了以后按账号检索时会漏掉。
	BoundAccountKind string
	// ServiceName 是订阅的服务名，如 "OpenAI Plus"。
	ServiceName string
	// NextRenewalOn 是下次续费日期，YYYY-MM-DD；空串表示不是订阅或没登记。
	//
	// **人填的，不是算的。** 从流水推断周期看着聪明，但试用转正、年付转月付、
	// 涨价都会让推断悄悄错掉，而错了的提醒比没有提醒更糟——人会信它。
	NextRenewalOn string
	// SubscriptionAmount 是每期扣款金额（十进制文本），空串表示没登记。
	//
	// **人填的，不是从流水算的**，与 NextRenewalOn 同一条纪律：试用价、
	// 首月折扣、年付摊月、汇率波动都会让推断悄悄错掉，而一个错了的
	// 「每月支出」比没有这个数更糟——人会拿它做预算。
	SubscriptionAmount string
	// SubscriptionCycle 是扣款周期：monthly / yearly / weekly / other。
	SubscriptionCycle string
	Note              string
}

// boundAccountKinds 是允许的标识类型，与迁移 000028 的 CHECK 约束一致。
var boundAccountKinds = []string{"", "email", "username", "phone", "other"}

// renewalDateLayout 是续费日期的格式。业务日不带时刻：续费发生在某一天，
// 精确到秒既没有意义，也会让「今天要续费吗」这种判断被时区搅乱。
const renewalDateLayout = "2006-01-02"

// Validate 校验登记内容。
func (u CardUsage) Validate() error {
	if !slices.Contains(boundAccountKinds, u.BoundAccountKind) {
		return fmt.Errorf("绑定账号类型 %q 非法，只接受 %v", u.BoundAccountKind, boundAccountKinds[1:])
	}
	if u.NextRenewalOn != "" {
		if _, err := time.Parse(renewalDateLayout, u.NextRenewalOn); err != nil {
			// 拼错的日期不能静默丢掉：那会让一张「本以为设了提醒」的卡
			// 在续费日当天悄悄扣不上。
			return fmt.Errorf("续费日期 %q 非法，需形如 2026-10-01", u.NextRenewalOn)
		}
	}
	return nil
}

// SetCardUsage 登记一张卡的用途。
//
// 账号未配置一律拒绝，与所有其他卡操作同一条纪律——即便这一步不碰上游、
// 不花钱，往一个不存在的账号下写登记也只会产生一条永远对不上的数据。
func (s *Service) SetCardUsage(ctx context.Context, usage CardUsage) error {
	if _, err := s.account(usage.Account); err != nil {
		return err
	}
	if err := usage.Validate(); err != nil {
		return err
	}
	return s.store.SetCardUsage(ctx, usage)
}

// RenewalRiskLevel 是续费风险等级。
type RenewalRiskLevel string

const (
	// RenewalRiskNone：没登记续费日期，或还早。
	RenewalRiskNone RenewalRiskLevel = "none"
	// RenewalRiskSoon：七天内要续费，卡上有钱。
	RenewalRiskSoon RenewalRiskLevel = "soon"
	// RenewalRiskUnfunded：七天内要续费但卡上没钱——**这是这个功能真正的用处**。
	// 订阅续不上通常不会有人提前发现，等发现时服务已经掉了。
	RenewalRiskUnfunded RenewalRiskLevel = "unfunded"
	// RenewalRiskOverdue：续费日已经过了。可能已经扣过（该更新日期），
	// 也可能没扣上（该查）——两种都需要人看一眼。
	RenewalRiskOverdue RenewalRiskLevel = "overdue"
)

// renewalSoonWindow 是「快到了」的窗口。
//
// 七天：足够在工作日里被看见并处理（充值到账也要时间），又不至于长到
// 让看板上常年挂着一片提醒——常年亮着的提醒等于没有提醒。
const renewalSoonWindow = 7 * 24 * time.Hour

// RenewalRisk 判定一张卡的续费风险。
//
// 日期解析不了时按「没登记」处理而不是报错：这个函数在渲染路径上，
// 一条脏数据不该让整页崩掉。校验在写入那一侧做（CardUsage.Validate）。
func RenewalRisk(nextRenewalOn string, balanceMinor int64, today time.Time) RenewalRiskLevel {
	if nextRenewalOn == "" {
		return RenewalRiskNone
	}
	renewal, err := time.Parse(renewalDateLayout, nextRenewalOn)
	if err != nil {
		return RenewalRiskNone
	}

	day := today.UTC().Truncate(24 * time.Hour)
	switch {
	case renewal.Before(day):
		return RenewalRiskOverdue
	case renewal.Sub(day) <= renewalSoonWindow:
		if balanceMinor <= 0 {
			return RenewalRiskUnfunded
		}
		return RenewalRiskSoon
	default:
		return RenewalRiskNone
	}
}
