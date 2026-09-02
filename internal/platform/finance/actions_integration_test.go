package finance

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 跑在真库上的 Action 用例：跨环境闸门与审计前后镜像都要先有一行真实资源
// 才验得了，用内存假货复刻只会测到假货。
//
// 本文件在**包内**（而不是 finance_test 外部包）：它要调 action.Record*
// 写进上下文之后再读回来，那需要看得见未导出的 Handler 装配。

func actionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testDatabaseURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// profit_daily（XM-0037b）与订阅那三张表（XM-0037c）必须一起列出：
	// 它们对 upstream_account 的外键是 ON DELETE RESTRICT（历史台账与付款
	// 记录不该随账号一起消失），而 TRUNCATE 要求一次列全所有引用方，
	// 漏掉任何一张这条语句会直接报错。
	//
	// platform_channel_binding（XM-C-MAP2）同样是 ON DELETE RESTRICT，
	// migrations/000016 之后新增，同一条道理必须列进来。
	if _, err := pool.Exec(ctx,
		"TRUNCATE finance.balance_history, finance.amortization_loss, "+
			"finance.subscription_cost_batch, "+
			"finance.proxy_asset, finance.profit_daily, finance.token_map, "+
			"finance.platform_channel_binding, "+
			"finance.upstream_account"); err != nil {
		t.Fatalf("清空登记簿失败: %v", err)
	}
	return pool
}

func actionStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(actionPool(t))
}

// registerAccount 走 Action 登记一条账号，返回它的 id。
func registerAccount(t *testing.T, store *Store, ctx context.Context, extra map[string]any) uuid.UUID {
	t.Helper()
	params := map[string]any{
		"system_type":    string(SystemSub2API),
		"access_method":  string(AccessUpstreamKey),
		"credential_ref": actionCredentialRef,
		"base_url":       "https://action.example.test",
		"recharge_ratio": "1.5",
	}
	for k, v := range extra {
		params[k] = v
	}
	out, err := runAction(t, store, ctx, ActionAccountSet, params)
	if err != nil {
		t.Fatalf("登记账号失败: %v", err)
	}
	account, ok := out.(UpstreamAccount)
	if !ok {
		t.Fatalf("返回值类型 = %T, want UpstreamAccount", out)
	}
	return account.ID
}

func runAction(
	t *testing.T, store *Store, ctx context.Context, id string, params map[string]any,
) (any, error) {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, handler, ok := reg.Lookup(id, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", id)
	}
	return handler(ctx, params)
}

// TestAccountSetCreatesThenUpdates 验证「有没有 id」区分新建与修改。
func TestAccountSetCreatesThenUpdates(t *testing.T) {
	store := actionStore(t)
	ctx := humanCtx("production")

	id := registerAccount(t, store, ctx, nil)

	// 带上 id = 改这一条；同时验证倍率能经通用更新改掉
	out, err := runAction(t, store, ctx, ActionAccountSet, map[string]any{
		"upstream_account_id": id.String(),
		"system_type":         string(SystemSub2API),
		"access_method":       string(AccessUpstreamKey),
		"credential_ref":      actionCredentialRef,
		"base_url":            "https://action.example.test",
		"recharge_ratio":      "1.15",
		"status":              string(StatusDisabled),
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	updated := out.(UpstreamAccount)
	if updated.ID != id {
		t.Fatalf("更新产生了新 id: %s vs %s", updated.ID, id)
	}
	if updated.RechargeRatio.String() != "1.15" {
		t.Fatalf("倍率 = %q", updated.RechargeRatio)
	}
	if updated.Status != StatusDisabled {
		t.Fatalf("状态 = %q", updated.Status)
	}

	all, err := store.ListAccountsByEnvironment(context.Background(), "production")
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("更新不该新建行，实际有 %d 行", len(all))
	}
}

// TestAccountSetMetadataDistinguishesMissingFromExplicitEmpty protects the
// backward-compatible v1 extension: an older client does not know the new
// metadata/group_rate keys, so omitting them while updating another field must
// preserve the existing values. Sending the key with an empty string is the
// explicit clear operation and must still land as NULL/zero on readback.
func TestAccountSetMetadataDistinguishesMissingFromExplicitEmpty(t *testing.T) {
	store := actionStore(t)
	ctx := humanCtx("production")
	id := registerAccount(t, store, ctx, nil)
	created, err := store.GetAccount(ctx, id)
	if err != nil {
		t.Fatalf("重读新建账号失败: %v", err)
	}
	if created.UpstreamName != "" || created.UpstreamContact != "" ||
		created.UpstreamGroup != "" || !created.GroupRate.IsZero() {
		t.Fatalf("创建时缺键必须落空值: %+v", created)
	}

	baseUpdate := map[string]any{
		"upstream_account_id": id.String(),
		"system_type":         string(SystemSub2API),
		"access_method":       string(AccessUpstreamKey),
		"credential_ref":      actionCredentialRef,
		"base_url":            "https://action-updated.example.test",
		"recharge_ratio":      "1.15",
		"status":              string(StatusActive),
	}
	setUpdate := make(map[string]any, len(baseUpdate)+4)
	for key, value := range baseUpdate {
		setUpdate[key] = value
	}
	setUpdate["upstream_name"] = "Relay A"
	setUpdate["upstream_contact"] = "运营群 @relay-a"
	setUpdate["upstream_group"] = "gpt-main"
	setUpdate["group_rate"] = "1.25"
	if _, err := runAction(t, store, ctx, ActionAccountSet, setUpdate); err != nil {
		t.Fatalf("设置新字段失败: %v", err)
	}

	out, err := runAction(t, store, ctx, ActionAccountSet, baseUpdate)
	if err != nil {
		t.Fatalf("旧客户端省略新字段的更新失败: %v", err)
	}
	preserved := out.(UpstreamAccount)
	if preserved.UpstreamName != "Relay A" ||
		preserved.UpstreamContact != "运营群 @relay-a" ||
		preserved.UpstreamGroup != "gpt-main" || preserved.GroupRate.String() != "1.25" {
		t.Fatalf("缺键更新必须保留新字段: %+v", preserved)
	}

	clearUpdate := make(map[string]any, len(baseUpdate)+4)
	for key, value := range baseUpdate {
		clearUpdate[key] = value
	}
	clearUpdate["upstream_name"] = ""
	clearUpdate["upstream_contact"] = "  "
	clearUpdate["upstream_group"] = ""
	clearUpdate["group_rate"] = ""
	out, err = runAction(t, store, ctx, ActionAccountSet, clearUpdate)
	if err != nil {
		t.Fatalf("显式清空新字段失败: %v", err)
	}
	cleared := out.(UpstreamAccount)
	if cleared.UpstreamName != "" || cleared.UpstreamContact != "" ||
		cleared.UpstreamGroup != "" || !cleared.GroupRate.IsZero() {
		t.Fatalf("显式空串必须清空新字段: %+v", cleared)
	}
}

// TestAccountSetRefusesAccessMethodChange 是 §2.0 的结构性保护。
//
// access_method 是成本口径的分叉点：改它会让同一个账号的历史成本前后用
// 两套算法算出来，而台账里没有任何痕迹。
func TestAccountSetRefusesAccessMethodChange(t *testing.T) {
	store := actionStore(t)
	ctx := humanCtx("production")
	id := registerAccount(t, store, ctx, nil)

	_, err := runAction(t, store, ctx, ActionAccountSet, map[string]any{
		"upstream_account_id": id.String(),
		"system_type":         string(SystemSub2API),
		"access_method":       string(AccessSubscriptionAccount),
		"credential_ref":      actionCredentialRef,
	})
	if err == nil {
		t.Fatal("改 access_method 必须被拒")
	}
	if !strings.Contains(err.Error(), "新登记一条") {
		t.Fatalf("错误应告诉调用方正确做法, got %v", err)
	}

	// system_type 同理：连接器与取数口径都会变
	_, err = runAction(t, store, ctx, ActionAccountSet, map[string]any{
		"upstream_account_id": id.String(),
		"system_type":         string(SystemNewAPI),
		"access_method":       string(AccessUpstreamKey),
		"credential_ref":      actionCredentialRef,
		"recharge_ratio":      "1.5",
	})
	if err == nil {
		t.Fatal("改 system_type 必须被拒")
	}
}

// TestActionsEnforceCrossEnvironmentGate 是宪法 15 条的落点。
//
// 内核只校验「这个 Action 允许在你的环境执行」，它不认识资源——一个
// staging 身份完全可能拿着生产账号的 UUID 打过来。这道判定必须在
// 读到资源之后，而那需要一行真实的资源。
func TestActionsEnforceCrossEnvironmentGate(t *testing.T) {
	store := actionStore(t)
	prodCtx := humanCtx("production")
	id := registerAccount(t, store, prodCtx, nil)

	stagingCtx := humanCtx("staging")
	for name, call := range map[string]func() (any, error){
		"改登记簿": func() (any, error) {
			return runAction(t, store, stagingCtx, ActionAccountSet, map[string]any{
				"upstream_account_id": id.String(),
				"system_type":         string(SystemSub2API),
				"access_method":       string(AccessUpstreamKey),
				"credential_ref":      actionCredentialRef,
				"recharge_ratio":      "1.5",
			})
		},
		"改倍率": func() (any, error) {
			return runAction(t, store, stagingCtx, ActionRechargeRatioSet, map[string]any{
				"upstream_account_id": id.String(),
				"recharge_ratio":      "2",
				"reason":              "越权尝试",
			})
		},
		"加映射": func() (any, error) {
			return runAction(t, store, stagingCtx, ActionTokenMapSet, map[string]any{
				"upstream_account_id": id.String(),
				"upstream_token_id":   "tok-1",
				"own_account_id":      "258",
				"credential_ref":      actionCredentialRef,
			})
		},
		"删映射": func() (any, error) {
			return runAction(t, store, stagingCtx, ActionTokenMapRemove, map[string]any{
				"upstream_account_id": id.String(),
				"upstream_token_id":   "tok-1",
				"reason":              "越权尝试",
			})
		},
	} {
		_, err := call()
		if err == nil {
			t.Fatalf("%s：staging 身份不该动得了生产资源", name)
		}
		var ae *action.Error
		if !asActionError(err, &ae) || ae.Code != action.CodePermissionDenied {
			t.Fatalf("%s 的错误码 = %v, want PERMISSION_DENIED", name, err)
		}
	}

	// 生产身份照常可用——闸门拦的是跨环境，不是所有人
	if _, err := runAction(t, store, prodCtx, ActionRechargeRatioSet, map[string]any{
		"upstream_account_id": id.String(),
		"recharge_ratio":      "2",
		"reason":              "上游调价",
	}); err != nil {
		t.Fatalf("同环境改倍率应成功: %v", err)
	}
}

// TestRechargeRatioSetIsExactAndAuditable 复核 §6.3：倍率改动可单独审计，
// 且值经 Action → 库 → 读回全程不失真。
func TestRechargeRatioSetIsExactAndAuditable(t *testing.T) {
	store := actionStore(t)
	ctx := humanCtx("production")
	id := registerAccount(t, store, ctx, nil)

	// 1.15 是 float64 表示不了的十进制数：经 Action 参数（字符串）→
	// money.ParseRatio → NUMERIC → 读回，必须逐位不变
	out, err := runAction(t, store, ctx, ActionRechargeRatioSet, map[string]any{
		"upstream_account_id": id.String(),
		"recharge_ratio":      "1.15",
		"reason":              "上游把折扣从 1.5 调到 1.15",
	})
	if err != nil {
		t.Fatalf("改倍率失败: %v", err)
	}
	if got := out.(UpstreamAccount).RechargeRatio.String(); got != "1.15" {
		t.Fatalf("倍率 = %q, want \"1.15\"", got)
	}

	reloaded, err := store.GetAccount(context.Background(), id)
	if err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if reloaded.RechargeRatio != money.MustParseRatio("1.15") {
		t.Fatalf("读回的倍率 = %v", reloaded.RechargeRatio)
	}
}

// TestTokenMapSetRequiresPerTokenCredentialForSub2API 是 §3.1 在登记入口的落点。
//
// sub2api 的成本侧要用**该令牌自己的明文**打 /v1/usage；没有 credential_ref
// 就发不出请求。在登记这一刻拒绝，比让采集任务每轮报一次「凭据缺失」有用得多。
func TestTokenMapSetRequiresPerTokenCredentialForSub2API(t *testing.T) {
	store := actionStore(t)
	ctx := humanCtx("production")
	id := registerAccount(t, store, ctx, nil)

	_, err := runAction(t, store, ctx, ActionTokenMapSet, map[string]any{
		"upstream_account_id": id.String(),
		"upstream_token_id":   "tok-1",
		"own_account_id":      "258",
	})
	if err == nil {
		t.Fatal("sub2api 计量型渠道的映射缺 credential_ref 必须被拒")
	}
	if !strings.Contains(err.Error(), "/v1/usage") {
		t.Fatalf("错误应说清缺的是什么、为什么要, got %v", err)
	}

	// 带上就通过
	if _, err := runAction(t, store, ctx, ActionTokenMapSet, map[string]any{
		"upstream_account_id": id.String(),
		"upstream_token_id":   "tok-1",
		"own_account_id":      "258",
		"credential_ref":      actionCredentialRef,
	}); err != nil {
		t.Fatalf("带凭据的映射应成功: %v", err)
	}
}

// TestNewAPITokenMapNeedsNoPerTokenCredential：newapi 的成本侧走账号级
// New-Api-User + Cookie，没有每令牌凭据——要求它反而是错的。
func TestNewAPITokenMapNeedsNoPerTokenCredential(t *testing.T) {
	store := actionStore(t)
	ctx := humanCtx("production")
	id := registerAccount(t, store, ctx, map[string]any{
		"system_type": string(SystemNewAPI),
		"base_url":    "https://newapi.example.test",
	})

	if _, err := runAction(t, store, ctx, ActionTokenMapSet, map[string]any{
		"upstream_account_id": id.String(),
		"upstream_token_id":   "my-token",
		"own_account_id":      "7",
	}); err != nil {
		t.Fatalf("newapi 映射不带每令牌凭据应成功: %v", err)
	}
}

// TestTokenMapRemoveRefusesMissingMapping：删不到要报错，不能静默成功——
// 一个「以为删掉了」的错误映射会继续把成本记到别的渠道上。
func TestTokenMapRemoveRefusesMissingMapping(t *testing.T) {
	store := actionStore(t)
	ctx := humanCtx("production")
	id := registerAccount(t, store, ctx, nil)

	_, err := runAction(t, store, ctx, ActionTokenMapRemove, map[string]any{
		"upstream_account_id": id.String(),
		"upstream_token_id":   "never-existed",
		"reason":              "清理",
	})
	if err == nil {
		t.Fatal("删除不存在的映射必须报错")
	}
	var ae *action.Error
	if !asActionError(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("错误码 = %v, want INVALID_PARAMS", err)
	}
}

// TestAuditSummaryCarriesRefNotPlaintext 复核进审计链的摘要形状。
//
// credential_ref 进链是**安全的**且必要的（它是引用不是凭据，ADR-014），
// 而「这条渠道的成本用哪个凭据读出来的」正是对账出问题时要问的第一个问题。
// recharge_ratio 必须是**定点字符串**：审计摘要要进哈希链并经 jsonb 往返，
// 一个 float 化的 1.15 会在往返后变成 1.1499999999999999。
func TestAuditSummaryCarriesRefNotPlaintext(t *testing.T) {
	account := UpstreamAccount{
		SystemType:    SystemSub2API,
		AccessMethod:  AccessUpstreamKey,
		BaseURL:       "https://audit.example.test",
		CredentialRef: actionCredentialRef,
		RechargeRatio: money.MustParseRatio("1.15"),
		Currency:      DefaultCurrency,
		BusinessDayTZ: DefaultBusinessDayTZ,
		Status:        StatusActive,
		Environment:   "production",
	}
	summary := accountSummary(account)

	if summary["credential_ref"] != actionCredentialRef {
		t.Fatalf("审计摘要缺少 credential_ref: %+v", summary)
	}
	ratio, ok := summary["recharge_ratio"].(string)
	if !ok {
		t.Fatalf("recharge_ratio 必须是字符串（哈希链要经 jsonb 往返）, got %T",
			summary["recharge_ratio"])
	}
	if ratio != "1.15" {
		t.Fatalf("recharge_ratio = %q", ratio)
	}

	// 未配倍率时**不写这个键**：「没有倍率」（订阅型）与「倍率是某个值」
	// 在审计上是两件事
	account.AccessMethod = AccessSubscriptionAccount
	account.RechargeRatio = money.Ratio{}
	summary = accountSummary(account)
	if _, present := summary["recharge_ratio"]; present {
		t.Fatalf("未配倍率时不该写 recharge_ratio 键: %+v", summary)
	}
}
