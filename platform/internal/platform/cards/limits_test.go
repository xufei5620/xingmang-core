package cards

import (
	"errors"
	"testing"
)

// 限额以**申请金额本身的单位**（token，USDT/USDC）计，不做任何汇率换算。
// 配置值与请求值用同一个标度解析，所以比较是自洽的——这里不主张
// 1 USDT = 1 USD，那种假设一旦写进代码就再也没人会去质疑它。
func TestLimitsRejectsAmountOverPerOperationCap(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: "500"}

	err := l.Check("100.01", "0")
	if err == nil {
		t.Fatal("超过单笔上限必须被拒")
	}
	if !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("错误 = %v, want ErrPerOperationExceeded", err)
	}
}

func TestLimitsAllowsAmountExactlyAtCap(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: "500"}

	if err := l.Check("100", "0"); err != nil {
		t.Fatalf("正好等于上限应放行: %v", err)
	}
}

func TestLimitsRejectsWhenDailyTotalWouldExceed(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: "500"}

	// 今天已花 450，再来 60 就是 510
	err := l.Check("60", "450")
	if err == nil {
		t.Fatal("超过单日上限必须被拒")
	}
	if !errors.Is(err, ErrDailyExceeded) {
		t.Fatalf("错误 = %v, want ErrDailyExceeded", err)
	}
}

func TestLimitsAllowsWhenDailyTotalExactlyAtCap(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: "500"}

	if err := l.Check("50", "450"); err != nil {
		t.Fatalf("正好用满单日上限应放行: %v", err)
	}
}

// 上限没配 = 没有护栏。这里必须 fail closed：一个空配置不该等于「随便花」。
func TestLimitsUnconfiguredFailsClosed(t *testing.T) {
	for _, l := range []Limits{
		{},
		{PerOperation: "100"},
		{PerDay: "500"},
	} {
		if err := l.Check("1", "0"); err == nil {
			t.Fatalf("上限未配齐时必须拒绝，配置 = %+v", l)
		}
	}
}

func TestLimitsRejectsNonPositiveAmount(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: "500"}

	for _, amount := range []string{"0", "-1", "-0.01"} {
		if err := l.Check(amount, "0"); err == nil {
			t.Fatalf("金额 %q 必须被拒", amount)
		}
	}
}

// 金额全程走整数最小单位，不经 float（宪法条款 13）。
func TestLimitsIsExactOnDecimalsThatFloatWouldBreak(t *testing.T) {
	l := Limits{PerOperation: "0.3", PerDay: "1"}

	if err := l.Check("0.3", "0"); err != nil {
		t.Fatalf("0.3 正好等于上限应放行: %v", err)
	}
}

// 精度超出比较标度的金额必须被拒，**不能四舍五入后再比**。
//
// 理由是校验的值必须就是发出去的值：我们把 "0.30000001" 舍成 0.300000
// 去和上限比，放行之后发给上游的却仍是原始文本 "0.30000001"——
// 一个比校验值更大的数。金额小得可以忽略，但「校验对象与执行对象不是
// 同一个值」这件事本身不能接受。
func TestLimitsRejectsAmountWithPrecisionBeyondScale(t *testing.T) {
	l := Limits{PerOperation: "0.3", PerDay: "1"}

	err := l.Check("0.30000001", "0")
	if err == nil {
		t.Fatal("精度超出比较标度的金额必须被拒，而不是四舍五入后放行")
	}
	if !errors.Is(err, ErrAmountInvalid) {
		t.Fatalf("错误 = %v, want ErrAmountInvalid", err)
	}
}

func TestLimitsRejectsUnparseableAmount(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: "500"}

	for _, amount := range []string{"", "abc", "1,000", "10 USD"} {
		if err := l.Check(amount, "0"); err == nil {
			t.Fatalf("金额 %q 无法解析，必须被拒", amount)
		}
	}
}

// 显式声明「不设限」要放行任意大的金额。
//
// 这是内部自用场景下产品负责人的判断（2026-09-04）：Infini 账户余额本身就是
// 硬顶，充值还可以 redeem 拉回来，再加一层单日累计意义不大。
func TestLimitsUnlimitedAllowsAnyAmount(t *testing.T) {
	l := Limits{PerOperation: LimitUnlimited, PerDay: LimitUnlimited}

	if err := l.Check("999999999", "888888888"); err != nil {
		t.Fatalf("显式不设限时任意金额都应放行: %v", err)
	}
}

// 「不设限」必须是显式写出来的，空配置仍然 fail closed。
//
// 这两件事不能合并：忘了配和故意不限的区别，就是哪天配置掉了以后
// 你以为还有护栏、而实际上没有。
func TestLimitsUnlimitedIsExplicitNotTheEmptyDefault(t *testing.T) {
	if err := (Limits{}).Check("1", "0"); !errors.Is(err, ErrLimitsUnconfigured) {
		t.Fatalf("空配置必须 fail closed, got %v", err)
	}
	// 拼错的关键字也算没配好，不能因为「不是个数」就悄悄放行。
	err := Limits{PerOperation: "unlimted", PerDay: LimitUnlimited}.Check("1", "0")
	if !errors.Is(err, ErrLimitsUnconfigured) {
		t.Fatalf("拼错的关键字必须 fail closed, got %v", err)
	}
}

// 两档可以分开设：留住单笔上限当手滑挡板，同时不限单日累计。
func TestLimitsPerOperationStillAppliesWhenDailyIsUnlimited(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: LimitUnlimited}

	if err := l.Check("50", "999999"); err != nil {
		t.Fatalf("单日不设限时，今日已花多少都不该拦: %v", err)
	}
	if err := l.Check("100.01", "0"); !errors.Is(err, ErrPerOperationExceeded) {
		t.Fatalf("单笔上限仍应生效, got %v", err)
	}
}

// 不设限只关掉「比大小」，不关掉「这是不是个合法金额」。
//
// 金额非正或压根解析不出来，说明调用方错了，跟限额松紧无关。
func TestLimitsUnlimitedStillRejectsInvalidAmount(t *testing.T) {
	l := Limits{PerOperation: LimitUnlimited, PerDay: LimitUnlimited}

	for _, amount := range []string{"0", "-1", "abc", ""} {
		if err := l.Check(amount, "0"); !errors.Is(err, ErrAmountInvalid) {
			t.Fatalf("金额 %q 应被判非法, got %v", amount, err)
		}
	}
}
