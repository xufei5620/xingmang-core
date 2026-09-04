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
