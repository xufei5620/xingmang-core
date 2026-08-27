package finance_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 分组倍率（XM-0049，UI 交接 §10.2 + §13 的 groupRate）。
//
// 这个字段最重要的性质是**它什么都不做**：它只被存下来、读回来、展示出去，
// 一次都不参与成本或收入的计算。所以本文件里份量最重的那条用例，
// 断言的是「加了它之后什么都没变」。

// TestGroupRateNeverEntersCostArithmetic 是本片的核心断言。
//
// §10.2 的原话：「分组倍率独立存储 / 展示，**不并入 recharge_ratio**，
// 前端不重复乘算」。把它乘进成本会让每条渠道按各自的分组倍率错一遍——
// 每条都错、比例还各不相同，在报表上完全看不出来。
//
// 判据不是「读一遍代码没找到乘法」，而是**行为**：两个只差一个分组倍率的
// 账号，采集写出来的台账必须逐位相同。哪天有人在折算里顺手乘了它，这条会红。
func TestGroupRateNeverEntersCostArithmetic(t *testing.T) {
	plain, grouped := uuid.New(), uuid.New()

	withGroupRate := collectAccount(grouped)
	// 3.0 是一个**足够扎眼**的倍率：真被乘进去的话，成本会差三倍，
	// 而不是差在某个舍入位上难以察觉。
	withGroupRate.GroupRate = money.MustParseRatio("3")
	withGroupRate.BaseURL = "https://grouped.example.test"

	f := newFixture(collectAccount(plain), withGroupRate)
	f.registry.mappings[plain] = []finance.TokenMapping{mapping(plain, "tok-a", "acct-a")}
	f.registry.mappings[grouped] = []finance.TokenMapping{mapping(grouped, "tok-b", "acct-b")}
	f.clients[plain] = newStub()
	f.clients[grouped] = newStub()

	if _, err := f.collector().CollectOnce(context.Background()); err != nil {
		t.Fatalf("采集失败: %v", err)
	}

	plainRow := f.ledger.rows[fmt.Sprintf("%s|%s|tok-a", plain, collectDay)]
	groupedRow := f.ledger.rows[fmt.Sprintf("%s|%s|tok-b", grouped, collectDay)]
	if plainRow.CostMinor == nil || groupedRow.CostMinor == nil {
		t.Fatalf("两条都该入账: %v / %v", plainRow.CostMinor, groupedRow.CostMinor)
	}
	if *plainRow.CostMinor != *groupedRow.CostMinor {
		t.Fatalf("分组倍率改变了成本：无倍率 %d vs 有倍率 %d。"+
			"§10.2 明确要求它不并入成本折算",
			*plainRow.CostMinor, *groupedRow.CostMinor)
	}
	if *plainRow.RevenueMinor != *groupedRow.RevenueMinor {
		t.Fatal("分组倍率也不该碰收入")
	}
	// 逐行冻结的倍率必须仍然是**充值**倍率，不是分组倍率
	if got := groupedRow.RatioSnapshot.String(); got != "1.5" {
		t.Fatalf("ratio_snapshot = %q, want 1.5（充值倍率，不是分组倍率）", got)
	}
}

// TestGroupRateMustBePositive：0 与负值一律拒绝。
//
// 与 recharge_ratio 同一条理由，差别在于**这里没有算术层的兜底**——
// group_rate 不参与任何计算，所以领域层与库层的两道 CHECK 是它仅有的护栏。
func TestGroupRateMustBePositive(t *testing.T) {
	for _, raw := range []string{"0", "-1", "-0.5"} {
		a := meteredAccount()
		a.GroupRate = money.MustParseRatio(raw)
		if err := a.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
			t.Fatalf("group_rate=%s 应被拒, got %v", raw, err)
		}
	}
	ok := meteredAccount()
	ok.GroupRate = money.MustParseRatio("1.25")
	if err := ok.Validate(); err != nil {
		t.Fatalf("正的分组倍率应通过: %v", err)
	}
}

// TestGroupRateIsOptionalForEveryAccessMethod：三种接入方式都可以不配。
//
// 与 recharge_ratio 刻意不同：那个有三条按接入方式分叉的约束
// （计量型必填、订阅型必空、official 不约束），而分组倍率对谁都是可选的
// ——它是定价分组的标注，与成本口径无关。
func TestGroupRateIsOptionalForEveryAccessMethod(t *testing.T) {
	metered := meteredAccount()
	metered.GroupRate = money.Ratio{}
	if err := metered.Validate(); err != nil {
		t.Fatalf("计量型不配分组倍率应通过: %v", err)
	}

	subscription := meteredAccount()
	subscription.AccessMethod = finance.AccessSubscriptionAccount
	subscription.RechargeRatio = money.Ratio{}
	subscription.GroupRate = money.MustParseRatio("2")
	if err := subscription.Validate(); err != nil {
		t.Fatalf("订阅型配分组倍率应通过（它不是成本口径的一部分）: %v", err)
	}
}
