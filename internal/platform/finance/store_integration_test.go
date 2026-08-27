package finance_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 本文件跑在真库上。登记簿有一半不变量在 SQL 里（三条倍率 CHECK、
// credential_ref 的正则、业务日偏移的正则、重复登记的部分唯一索引），
// 用内存假货复刻只会测到假货。
//
// 最要紧的一条只有真库测得出来：**倍率经 NUMERIC 往返是否逐位不变**。
// 那是影子对比（§9，分粒度 0 差异）的地基——倍率是除数，尾数差一点点，
// 逐日折算下来就是对不上的那一分钱。

const intEnv = "production"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// token_map 有 ON DELETE CASCADE，但显式列出每一张表：
	// TRUNCATE 的级联行为与外键的不是同一回事，写全了不必去记这个差别。
	//
	// profit_daily（XM-0037b）**必须**列进来：它对 upstream_account 的外键是
	// ON DELETE RESTRICT（历史台账不该随账号一起消失），而 TRUNCATE 要求
	// 一次列全所有引用方，漏掉它这条语句会直接报错。
	//
	// XM-0037c 的三张表同样要列全：amortization_loss 引用批次与代理，
	// 批次引用代理与登记簿——TRUNCATE 要求一次列全所有引用方，
	// 漏掉任何一张这条语句会直接报错。
	if _, err := pool.Exec(ctx,
		"TRUNCATE finance.balance_history, finance.amortization_loss, "+
			"finance.subscription_cost_batch, "+
			"finance.proxy_asset, finance.profit_daily, finance.token_map, "+
			"finance.upstream_account"); err != nil {
		t.Fatalf("清空登记簿失败: %v", err)
	}
	return pool
}

func testStore(t *testing.T) *finance.Store {
	t.Helper()
	return finance.NewStore(testPool(t))
}

func mustCreate(t *testing.T, s *finance.Store, in finance.UpstreamAccount) finance.UpstreamAccount {
	t.Helper()
	out, err := s.CreateAccount(context.Background(), in)
	if err != nil {
		t.Fatalf("登记账号失败: %v", err)
	}
	return out
}

func integrationAccount() finance.UpstreamAccount {
	a := meteredAccount()
	a.ID = uuid.Nil // 让 Store 自己发号
	a.Environment = intEnv
	return a
}

// TestRechargeRatioSurvivesNumericRoundTrip 是本包最要紧的一条断言。
//
// 倍率是**除数**（§3.4）：它经 NUMERIC 往返后必须逐位不变，否则影子对比
// （§9 要求分粒度 0 差异）从第一天起就没有地基。特别要挡住的是
// pgtype.Numeric.Float64Value() 那条路——1.15 走一遍 float64 会变成
// 1.1499999999999999，而那个差异不会报错，只会让成本静静地偏一点。
func TestRechargeRatioSurvivesNumericRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, raw := range []string{
		"1.5", "1", "2.00", "0.85",
		"1.15", // float64 表示不了：1.1499999999999999
		"0.1",  // float64 表示不了：0.1000000000000000055511151231257827
		"3.14159",
		"0.000000001", // maxRatioScale 边界
		"1000000",     // 大整数倍率
	} {
		in := integrationAccount()
		in.BaseURL = "https://ratio-" + strings.ReplaceAll(raw, ".", "-") + ".example.test"
		in.RechargeRatio = money.MustParseRatio(raw)

		created := mustCreate(t, s, in)
		if got := created.RechargeRatio.String(); got != raw {
			t.Fatalf("写入即返回的倍率 = %q, want %q", got, raw)
		}

		// 真正的判据是**重新读一次**：上一步的返回值可能来自 RETURNING 的
		// 内存值，只有再查一遍才证明它在库里也是这个数。
		reloaded, err := s.GetAccount(ctx, created.ID)
		if err != nil {
			t.Fatalf("重读账号失败: %v", err)
		}
		if got := reloaded.RechargeRatio.String(); got != raw {
			t.Fatalf("从库读回的倍率 = %q, want %q", got, raw)
		}
		if reloaded.RechargeRatio != created.RechargeRatio {
			t.Fatalf("往返后倍率不等：%v vs %v", reloaded.RechargeRatio, created.RechargeRatio)
		}

		// 折算结果也必须一致——这才是倍率精度真正影响的东西
		costBefore, err := money.Divide(5_813_729, created.RechargeRatio)
		if err != nil {
			t.Fatalf("折算失败: %v", err)
		}
		costAfter, err := money.Divide(5_813_729, reloaded.RechargeRatio)
		if err != nil {
			t.Fatalf("折算失败: %v", err)
		}
		if costBefore != costAfter {
			t.Fatalf("倍率 %s 往返后折算结果变了：%d → %d", raw, costBefore, costAfter)
		}
	}
}

// TestSubscriptionAccountStoresNullRatio 验证「未配置」在库里落成 NULL。
//
// 落成 0 的话，读回来会变成「配置成 0」，而那是个非法值——
// 一张自洽的表不该在读回时产出自己拒绝写入的东西。
func TestSubscriptionAccountStoresNullRatio(t *testing.T) {
	s := testStore(t)

	in := integrationAccount()
	in.AccessMethod = finance.AccessSubscriptionAccount
	in.RechargeRatio = money.Ratio{}
	in.BaseURL = ""

	created := mustCreate(t, s, in)
	if !created.RechargeRatio.IsZero() {
		t.Fatalf("订阅型账号读回的倍率应为未配置，got %v", created.RechargeRatio)
	}
	reloaded, err := s.GetAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if !reloaded.RechargeRatio.IsZero() {
		t.Fatalf("重读后倍率应为未配置，got %v", reloaded.RechargeRatio)
	}
	if reloaded.RechargeCostRate() != "" {
		t.Fatalf("未配倍率时不该有充值成本率投影，got %q", reloaded.RechargeCostRate())
	}
}

// TestDatabaseRejectsWhatDomainRejects 验证库层 CHECK 与领域校验是同一套规则。
//
// 绕过领域层（直接构造一条越过 Validate 的写入）时，库必须照样拦住——
// 那是「任何写入路径都绕不过去」这句话的实际含义。
func TestDatabaseRejectsWhatDomainRejects(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	const insert = `
INSERT INTO finance.upstream_account
  (id, system_type, access_method, base_url, credential_ref, recharge_ratio,
   currency, business_day_tz, status, environment)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', $9)`

	for name, row := range map[string][]any{
		"计量型缺倍率": {uuid.New(), "sub2api", "upstream_key",
			"https://a.example.test", testCredentialRef, nil, "USD", "+08:00", intEnv},
		"订阅型带倍率": {uuid.New(), "sub2api", "subscription_account",
			"https://b.example.test", testCredentialRef, "1.5", "USD", "+08:00", intEnv},
		"零倍率": {uuid.New(), "sub2api", "upstream_key",
			"https://c.example.test", testCredentialRef, "0", "USD", "+08:00", intEnv},
		"负倍率": {uuid.New(), "sub2api", "upstream_key",
			"https://d.example.test", testCredentialRef, "-1.5", "USD", "+08:00", intEnv},
		"明文凭据": {uuid.New(), "sub2api", "upstream_key",
			"https://e.example.test", "plaintext-not-a-ref", "1.5", "USD", "+08:00", intEnv},
		"base_url 非 https": {uuid.New(), "sub2api", "upstream_key",
			"http://f.example.test", testCredentialRef, "1.5", "USD", "+08:00", intEnv},
		"base_url 含凭证段": {uuid.New(), "sub2api", "upstream_key",
			"https://user:pass@g.example.test", testCredentialRef, "1.5", "USD", "+08:00", intEnv},
		"IANA 时区": {uuid.New(), "sub2api", "upstream_key",
			"https://h.example.test", testCredentialRef, "1.5", "USD", "Asia/Shanghai", intEnv},
		"未知接入方式": {uuid.New(), "sub2api", "direct",
			"https://i.example.test", testCredentialRef, "1.5", "USD", "+08:00", intEnv},
		"不存在的环境": {uuid.New(), "sub2api", "upstream_key",
			"https://j.example.test", testCredentialRef, "1.5", "USD", "+08:00", "prod"},
	} {
		if _, err := pool.Exec(ctx, insert, row...); err == nil {
			t.Fatalf("%s：库层必须拒绝这一行", name)
		}
	}
}

// TestDuplicateRegistrationIsRejected 验证本迁移**有意收紧**的那条唯一索引。
//
// 没有它时，重复登记不会报错，只会让同一笔上游实扣被两行各算一遍——
// 成本翻倍，而两行看起来都很正常。
func TestDuplicateRegistrationIsRejected(t *testing.T) {
	s := testStore(t)

	in := integrationAccount()
	in.BaseURL = "https://dup.example.test"
	mustCreate(t, s, in)

	again := integrationAccount()
	again.BaseURL = in.BaseURL
	if _, err := s.CreateAccount(context.Background(), again); err == nil {
		t.Fatal("同环境同系统同 base_url 重复登记必须被拒")
	}

	// 换一套系统类型就不算重复：同一个域名后面可能真的挂着两套系统
	other := integrationAccount()
	other.SystemType = finance.SystemNewAPI
	other.BaseURL = in.BaseURL
	if _, err := s.CreateAccount(context.Background(), other); err != nil {
		t.Fatalf("不同 system_type 不算重复，应允许: %v", err)
	}
}

func TestUpdateAndSetRechargeRatio(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	created := mustCreate(t, s, integrationAccount())

	// 倍率专用入口：只动倍率，其余字段不受影响
	updated, err := s.SetRechargeRatio(ctx, created.ID, money.MustParseRatio("1.15"))
	if err != nil {
		t.Fatalf("改倍率失败: %v", err)
	}
	if updated.RechargeRatio.String() != "1.15" {
		t.Fatalf("倍率 = %q, want 1.15", updated.RechargeRatio)
	}
	if updated.CredentialRef != created.CredentialRef || updated.Status != created.Status {
		t.Fatal("改倍率不该动到其他字段")
	}

	// 非正倍率在领域层就被拒，不必等库
	if _, err := s.SetRechargeRatio(ctx, created.ID, money.MustParseRatio("0")); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("零倍率必须被拒，got %v", err)
	}

	// 停用 = 采集侧 Kill Switch（宪法 26 条）
	created.Status = finance.StatusDisabled
	created.RechargeRatio = money.MustParseRatio("1.15")
	disabled, err := s.UpdateAccount(ctx, created)
	if err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if disabled.Status != finance.StatusDisabled {
		t.Fatalf("状态 = %q, want disabled", disabled.Status)
	}

	active, err := s.ListActiveAccountsByAccessMethod(ctx, intEnv, finance.AccessUpstreamKey)
	if err != nil {
		t.Fatalf("列出在采集的账号失败: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("停用后不该再出现在采集清单里，got %d 条", len(active))
	}
}

func TestSetRechargeRatioRejectsSubscriptionAccount(t *testing.T) {
	s := testStore(t)

	in := integrationAccount()
	in.AccessMethod = finance.AccessSubscriptionAccount
	in.RechargeRatio = money.Ratio{}
	in.BaseURL = ""
	created := mustCreate(t, s, in)

	_, err := s.SetRechargeRatio(context.Background(), created.ID, money.MustParseRatio("1.5"))
	if !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("订阅型不该有倍率，应报 ErrInconsistent，got %v", err)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	s := testStore(t)
	if _, err := s.GetAccount(context.Background(), uuid.New()); !errors.Is(err, finance.ErrNotFound) {
		t.Fatalf("不存在的账号应报 ErrNotFound，got %v", err)
	}
}

func TestTokenMappingLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	account := mustCreate(t, s, integrationAccount())

	mapping := finance.TokenMapping{
		UpstreamAccountID: account.ID,
		UpstreamTokenID:   "tok-1",
		OwnAccountID:      "258",
		CredentialRef:     testCredentialRef,
	}
	saved, err := s.PutTokenMapping(ctx, mapping)
	if err != nil {
		t.Fatalf("登记映射失败: %v", err)
	}
	if saved.OwnAccountID != "258" {
		t.Fatalf("own_account_id = %q", saved.OwnAccountID)
	}

	// 重复登记是**纠正**，不是冲突：同一个令牌改挂到另一个自营账号
	mapping.OwnAccountID = "301"
	corrected, err := s.PutTokenMapping(ctx, mapping)
	if err != nil {
		t.Fatalf("改写映射失败: %v", err)
	}
	if corrected.OwnAccountID != "301" {
		t.Fatalf("改写后 own_account_id = %q, want 301", corrected.OwnAccountID)
	}

	// 一个自营账号可由多把上游 key 供给——刻意不建唯一索引（见迁移注释）
	second := mapping
	second.UpstreamTokenID = "tok-2"
	if _, err := s.PutTokenMapping(ctx, second); err != nil {
		t.Fatalf("同一自营账号挂第二把 key 应被允许: %v", err)
	}

	byAccount, err := s.ListTokenMappingsByAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("列出映射失败: %v", err)
	}
	if len(byAccount) != 2 {
		t.Fatalf("映射数 = %d, want 2", len(byAccount))
	}

	byEnv, err := s.ListTokenMappingsByEnvironment(ctx, intEnv)
	if err != nil {
		t.Fatalf("按环境列出映射失败: %v", err)
	}
	if len(byEnv[account.ID]) != 2 {
		t.Fatalf("按环境分组后该账号有 %d 条映射, want 2", len(byEnv[account.ID]))
	}

	// 删不到要报错，不能静默成功：一个「以为删掉了」的错误映射
	// 会继续把成本记到别的渠道上
	if err := s.DeleteTokenMapping(ctx, account.ID, "tok-missing"); !errors.Is(err, finance.ErrNotFound) {
		t.Fatalf("删除不存在的映射应报 ErrNotFound，got %v", err)
	}
	if err := s.DeleteTokenMapping(ctx, account.ID, "tok-1"); err != nil {
		t.Fatalf("删除映射失败: %v", err)
	}
	if _, err := s.GetTokenMapping(ctx, account.ID, "tok-1"); !errors.Is(err, finance.ErrNotFound) {
		t.Fatalf("删除后应查不到，got %v", err)
	}
}

// TestListAccountsIsScopedToEnvironment：跨环境读取在 HTTP 层已经被
// resolveEnvironment 挡住，仓储这一层也不该把别的环境的行混进来（宪法 15 条）。
func TestListAccountsIsScopedToEnvironment(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	prod := integrationAccount()
	prod.BaseURL = "https://prod.example.test"
	mustCreate(t, s, prod)

	staging := integrationAccount()
	staging.Environment = "staging"
	staging.BaseURL = "https://staging.example.test"
	mustCreate(t, s, staging)

	got, err := s.ListAccountsByEnvironment(ctx, intEnv)
	if err != nil {
		t.Fatalf("列出登记簿失败: %v", err)
	}
	if len(got) != 1 || got[0].BaseURL != prod.BaseURL {
		t.Fatalf("按环境过滤失败，got %d 条: %+v", len(got), got)
	}
}
