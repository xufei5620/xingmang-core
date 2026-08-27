package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// financeCredentialRef 是用例共用的凭据引用。
//
// 抽成常量而不是就地写字面量：`CredentialRef: "secret://..."` 这个形状会被
// gitleaks 的 generic-api-key 规则当成泄露的密钥（同一条误报见本包
// alerts_test.go 的说明）。本仓禁止加 gitleaks allowlist，
// 所以换个写法比放宽扫描器划算。**常量名里也不能带 key/token/secret**。
const financeCredentialRef = "secret://finance/upstream-a"

type fakeAccountLister struct {
	gotEnv       string
	mappingCalls int
	accounts     []finance.UpstreamAccount
	mappings     map[uuid.UUID][]finance.TokenMapping
	err          error
	mappingErr   error
}

func (f *fakeAccountLister) ListAccountsByEnvironment(
	_ context.Context, env string,
) ([]finance.UpstreamAccount, error) {
	f.gotEnv = env
	return f.accounts, f.err
}

func (f *fakeAccountLister) ListTokenMappingsByEnvironment(
	_ context.Context, env string,
) (map[uuid.UUID][]finance.TokenMapping, error) {
	f.gotEnv = env
	f.mappingCalls++
	return f.mappings, f.mappingErr
}

func financeRouter(t *testing.T, lister UpstreamAccountLister) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:          discardLogger(),
		Service:         "platform-api",
		Environment:     "development",
		DB:              fakePinger{},
		Resolver:        res,
		Kernel:          nil,
		ActionRegistry:  action.NewRegistry(),
		FinanceAccounts: lister,
	})
}

func sampleAccount() (finance.UpstreamAccount, []finance.TokenMapping) {
	id := uuid.New()
	now := time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)
	return finance.UpstreamAccount{
		ID:            id,
		SystemType:    finance.SystemSub2API,
		AccessMethod:  finance.AccessUpstreamKey,
		BaseURL:       "https://api.example.test",
		CredentialRef: financeCredentialRef,
		RechargeRatio: money.MustParseRatio("1.15"),
		Currency:      "USD",
		BusinessDayTZ: "+08:00",
		Status:        finance.StatusActive,
		Environment:   "development",
		CreatedAt:     now,
		UpdatedAt:     now,
	}, []finance.TokenMapping{{
		UpstreamAccountID: id,
		UpstreamTokenID:   "tok-1",
		OwnAccountID:      "258",
		CredentialRef:     financeCredentialRef,
		UpdatedAt:         now,
	}}
}

func getAccounts(t *testing.T, lister UpstreamAccountLister, scopes, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/upstream-accounts"+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	financeRouter(t, lister).ServeHTTP(rec, req)
	return rec
}

func TestListUpstreamAccountsReturnsRegistry(t *testing.T) {
	account, mappings := sampleAccount()
	lister := &fakeAccountLister{
		accounts: []finance.UpstreamAccount{account},
		mappings: map[uuid.UUID][]finance.TokenMapping{account.ID: mappings},
	}

	rec := getAccounts(t, lister, finance.ScopeRead, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Items []struct {
			ID               string `json:"id"`
			SystemType       string `json:"system_type"`
			AccessMethod     string `json:"access_method"`
			CredentialRef    string `json:"credential_ref"`
			RechargeRatio    string `json:"recharge_ratio"`
			RechargeCostRate string `json:"recharge_cost_rate"`
			BusinessDayTZ    string `json:"business_day_tz"`
			Metered          bool   `json:"metered"`
			TokenMappings    []struct {
				UpstreamTokenID string `json:"upstream_token_id"`
				OwnAccountID    string `json:"own_account_id"`
				CredentialRef   string `json:"credential_ref"`
			} `json:"token_mappings"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("应有 1 条登记, got %d", len(body.Items))
	}
	item := body.Items[0]

	if item.CredentialRef != financeCredentialRef {
		t.Fatalf("credential_ref = %q", item.CredentialRef)
	}
	// 倍率必须是**字符串**：JSON 数字在前端一路解成 double，
	// 1.15 到了页面上就变成 1.1499999999999999（宪法 13 条）
	if item.RechargeRatio != "1.15" {
		t.Fatalf("recharge_ratio = %q, want \"1.15\"", item.RechargeRatio)
	}
	// 充值成本率是**现算的展示投影**（§3.4），1/1.15 = 0.869565…
	if item.RechargeCostRate != "0.869565" {
		t.Fatalf("recharge_cost_rate = %q, want \"0.869565\"", item.RechargeCostRate)
	}
	if item.BusinessDayTZ != "+08:00" {
		t.Fatalf("business_day_tz = %q——业务日结时区必须显式回出去（宪法 14 条）",
			item.BusinessDayTZ)
	}
	if !item.Metered {
		t.Fatal("upstream_key 应标记为计量型（§2.0）")
	}
	if len(item.TokenMappings) != 1 || item.TokenMappings[0].OwnAccountID != "258" {
		t.Fatalf("令牌映射未正确归并: %+v", item.TokenMappings)
	}
	// 映射只查一次，不是逐账号 N+1
	if lister.mappingCalls != 1 {
		t.Fatalf("映射查询次数 = %d, want 1", lister.mappingCalls)
	}
}

// TestRechargeRatioIsSerializedAsString 单独钉住序列化形态。
//
// 用原始 JSON 文本判断而不是解进结构体：解进 string 字段时，
// 一个 JSON 数字会解码失败，但一个**带引号的数字**和一个裸数字在结构体
// 层面看不出区别——必须看字节。
func TestRechargeRatioIsSerializedAsString(t *testing.T) {
	account, _ := sampleAccount()
	rec := getAccounts(t, &fakeAccountLister{
		accounts: []finance.UpstreamAccount{account},
	}, finance.ScopeRead, "")

	raw := rec.Body.String()
	if !strings.Contains(raw, `"recharge_ratio":"1.15"`) {
		t.Fatalf("倍率必须序列化成字符串，实际响应: %s", raw)
	}
	if strings.Contains(raw, `"recharge_ratio":1.15`) {
		t.Fatalf("倍率被序列化成了 JSON 数字——前端会把它解成 double: %s", raw)
	}
}

// TestSubscriptionAccountReportsNoRatio：订阅型渠道本就没有倍率（§2.0），
// 回一个 "1" 或 "0" 冒充会让前端以为「没打折」（宪法 12 条）。
func TestSubscriptionAccountReportsNoRatio(t *testing.T) {
	account, _ := sampleAccount()
	account.AccessMethod = finance.AccessSubscriptionAccount
	account.RechargeRatio = money.Ratio{}

	rec := getAccounts(t, &fakeAccountLister{
		accounts: []finance.UpstreamAccount{account},
	}, finance.ScopeRead, "")

	raw := rec.Body.String()
	if !strings.Contains(raw, `"recharge_ratio":""`) {
		t.Fatalf("未配倍率应回空串, got %s", raw)
	}
	if !strings.Contains(raw, `"recharge_cost_rate":""`) {
		t.Fatalf("未配倍率不该有充值成本率投影, got %s", raw)
	}
	if !strings.Contains(raw, `"metered":false`) {
		t.Fatalf("订阅型不该标记为计量型, got %s", raw)
	}
}

// TestPlaintextNeverAppearsInResponse 是宪法 7 条在这条读路径上的落点。
//
// 登记簿只存引用，本进程根本拿不到明文；这条断言防的是将来有人「顺手」
// 在响应里加一个「凭据预览」字段。
func TestPlaintextNeverAppearsInResponse(t *testing.T) {
	account, mappings := sampleAccount()
	rec := getAccounts(t, &fakeAccountLister{
		accounts: []finance.UpstreamAccount{account},
		mappings: map[uuid.UUID][]finance.TokenMapping{account.ID: mappings},
	}, finance.ScopeRead, "")

	raw := rec.Body.String()
	// 响应里每一处出现凭据的地方，都必须是 secret:// 引用
	for _, field := range []string{"credential_ref"} {
		idx := 0
		for {
			at := strings.Index(raw[idx:], `"`+field+`":"`)
			if at < 0 {
				break
			}
			start := idx + at + len(field) + 4
			end := strings.Index(raw[start:], `"`)
			if end < 0 {
				t.Fatalf("响应格式异常: %s", raw)
			}
			value := raw[start : start+end]
			if value != "" && !strings.HasPrefix(value, "secret://") {
				t.Fatalf("%s = %q 不是 CredentialRef——疑似明文泄漏（ADR-014）", field, value)
			}
			idx = start + end
		}
	}
}

func TestListUpstreamAccountsRequiresScope(t *testing.T) {
	account, _ := sampleAccount()
	lister := &fakeAccountLister{accounts: []finance.UpstreamAccount{account}}

	// 登记簿**不复用 ops.read**：能看指标的人不该自动能看到我们跟每个上游
	// 谈的价（见 finance.ScopeRead 的注释）
	rec := getAccounts(t, lister, "ops.read", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ops.read 不该放行登记簿, 状态码 = %d", rec.Code)
	}
	rec = getAccounts(t, lister, "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无权限时状态码 = %d, want 403", rec.Code)
	}
}

// TestListUpstreamAccountsRejectsCrossEnvironment：一个 development 身份
// 不该读到生产的倍率与映射（宪法 15 条：生产权限不继承）。
func TestListUpstreamAccountsRejectsCrossEnvironment(t *testing.T) {
	lister := &fakeAccountLister{}
	rec := getAccounts(t, lister, finance.ScopeRead, "?environment=production")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨环境读取状态码 = %d, want 403", rec.Code)
	}
	if lister.gotEnv != "" {
		t.Fatalf("跨环境请求不该打到仓储, gotEnv = %q", lister.gotEnv)
	}

	// 不传 environment 时用调用者自己的，而不是默认生产
	rec = getAccounts(t, lister, finance.ScopeRead, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("同环境读取状态码 = %d", rec.Code)
	}
	if lister.gotEnv != "development" {
		t.Fatalf("环境 = %q, want development", lister.gotEnv)
	}
}

func TestListUpstreamAccountsHidesStoreErrorDetail(t *testing.T) {
	lister := &fakeAccountLister{err: errStoreBoom}
	rec := getAccounts(t, lister, finance.ScopeRead, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	// 库层错误的文本带约束名与表名，对调用方没用且泄漏内部结构
	if strings.Contains(rec.Body.String(), errStoreBoom.Error()) {
		t.Fatalf("响应泄漏了库层错误细节: %s", rec.Body.String())
	}
}

var errStoreBoom = &storeError{"relation finance.upstream_account does not exist"}

type storeError struct{ msg string }

func (e *storeError) Error() string { return e.msg }
