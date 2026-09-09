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
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeApprovalService struct {
	req      approval.Request
	list     []approval.Request
	getErr   error
	listErr  error
	voteErr  error
	cancErr  error
	voted    []fakeVote
	listArgs []fakeListArgs
	canceled []string
}

type fakeVote struct {
	id      string
	who     principal.Principal
	verdict approval.Verdict
	comment string
}

type fakeListArgs struct {
	status approval.Status
	limit  int
}

func (f *fakeApprovalService) Get(context.Context, string) (approval.Request, error) {
	return f.req, f.getErr
}

func (f *fakeApprovalService) List(_ context.Context, status approval.Status, limit int) ([]approval.Request, error) {
	f.listArgs = append(f.listArgs, fakeListArgs{status: status, limit: limit})
	return f.list, f.listErr
}

func (f *fakeApprovalService) Vote(_ context.Context, id string, who principal.Principal,
	v approval.Verdict, comment string) (approval.Request, error) {
	f.voted = append(f.voted, fakeVote{id: id, who: who, verdict: v, comment: comment})
	return f.req, f.voteErr
}

func (f *fakeApprovalService) Cancel(_ context.Context, id string, _ principal.Principal) error {
	f.canceled = append(f.canceled, id)
	return f.cancErr
}

func (f *fakeApprovalService) Policy() approval.Policy { return approval.DefaultPolicy() }

type fakeApprovalExecutor struct {
	res    action.Result
	err    error
	calls  int
	params []map[string]any
}

func (f *fakeApprovalExecutor) ExecuteApproved(_ context.Context, _, _ string, params map[string]any) (action.Result, error) {
	f.calls++
	f.params = append(f.params, params)
	return f.res, f.err
}

func sampleApproval() approval.Request {
	decided := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	return approval.Request{
		ID:            uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		ActionID:      "registry.connection.set_status",
		ActionVersion: "1",
		Params:        map[string]any{"connection_id": "c-1", "status": "ACTIVE"},
		ParamsHash:    "abc123",
		RiskLevel:     "L3",
		RequesterID:   "staff_bob",
		RequesterType: principal.TypeHuman,
		Reason:        "上游换域名",
		Status:        approval.StatusPending,
		CreatedAt:     time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC),
		ExpiresAt:     time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC),
		DecidedAt:     nil,
		Decisions: []approval.Decision{{
			ApproverID: "staff_carol", ApproverType: principal.TypeHuman,
			Verdict: approval.VerdictApprove, Comment: "看过了",
			Privileged: false, CreatedAt: decided,
		}},
	}
}

func approvalRouter(t *testing.T, svc ApprovalService, exec ApprovalExecutor) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, ActionRegistry: action.NewRegistry(),
		Approvals: svc, ApprovalExec: exec,
	})
}

func doJSON(t *testing.T, h http.Handler, method, path, scopes, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	devHeaders(r, scopes)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestListApprovalsShapesTheQueue(t *testing.T) {
	svc := &fakeApprovalService{list: []approval.Request{sampleApproval()}}
	h := approvalRouter(t, svc, nil)

	w := doJSON(t, h, http.MethodGet, "/api/v1/approvals?status=PENDING", approval.ScopeRead, "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", w.Code, w.Body.String())
	}
	var got struct {
		Items     []approvalDTO `json:"items"`
		Limit     int           `json:"limit"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("条数不对：%+v", got.Items)
	}
	item := got.Items[0]
	// 界面要能说「还差一票」，所以票数要算好，别让前端自己数。
	if item.VotesRequired != 2 || item.VotesCast != 1 {
		t.Fatalf("票数不对：需要 %d、已投 %d", item.VotesRequired, item.VotesCast)
	}
	if item.RiskLevel != "L3" || item.Status != "PENDING" || item.Reason != "上游换域名" {
		t.Fatalf("单的内容不对：%+v", item)
	}
	if len(item.Decisions) != 1 || item.Decisions[0].ApproverID != "staff_carol" {
		t.Fatalf("票没带出来：%+v", item.Decisions)
	}
	if got.Limit != defaultApprovalLimit || got.Truncated {
		t.Fatalf("limit/truncated 不对：%d %v", got.Limit, got.Truncated)
	}
	if len(svc.listArgs) != 1 || svc.listArgs[0].status != approval.StatusPending {
		t.Fatalf("status 没传下去：%+v", svc.listArgs)
	}
}

// TestListApprovalsRejectsUnknownStatus：拼错的过滤条件要拒绝，不能静默当作
// 「不过滤」——那会让人看着一屏全量数据以为那就是筛选结果。
func TestListApprovalsRejectsUnknownStatus(t *testing.T) {
	svc := &fakeApprovalService{}
	h := approvalRouter(t, svc, nil)

	w := doJSON(t, h, http.MethodGet, "/api/v1/approvals?status=pending", approval.ScopeRead, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("小写状态该拒（枚举是大写），got %d：%s", w.Code, w.Body.String())
	}
	if len(svc.listArgs) != 0 {
		t.Fatalf("拒绝之后不该还去查库：%+v", svc.listArgs)
	}
	// 对照：大写的合法值查得动——确认拒的是拼写而不是端点本身坏了。
	if w := doJSON(t, h, http.MethodGet, "/api/v1/approvals?status=PENDING", approval.ScopeRead, ""); w.Code != http.StatusOK {
		t.Fatalf("合法状态应当 200，got %d", w.Code)
	}
}

func TestListApprovalsLimitBounds(t *testing.T) {
	svc := &fakeApprovalService{}
	h := approvalRouter(t, svc, nil)

	for _, raw := range []string{"0", "-1", "abc", "101"} {
		if w := doJSON(t, h, http.MethodGet, "/api/v1/approvals?limit="+raw, approval.ScopeRead, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s 应当 400，got %d", raw, w.Code)
		}
	}
	if w := doJSON(t, h, http.MethodGet, "/api/v1/approvals?limit=100", approval.ScopeRead, ""); w.Code != http.StatusOK {
		t.Fatalf("上限值本身应当放行，got %d", w.Code)
	}
}

// TestListApprovalsSaysWhenTruncated：取满上限时要说出来（XM-WORKBENCH-TRUNCATION
// 的同一条教训——一屏看起来完整的列表如果其实被截断了，人会据此收工）。
func TestListApprovalsSaysWhenTruncated(t *testing.T) {
	full := make([]approval.Request, 3)
	for i := range full {
		r := sampleApproval()
		r.ID = uuid.New()
		full[i] = r
	}
	svc := &fakeApprovalService{list: full}
	h := approvalRouter(t, svc, nil)

	w := doJSON(t, h, http.MethodGet, "/api/v1/approvals?limit=3", approval.ScopeRead, "")
	var got struct {
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !got.Truncated {
		t.Fatal("取满上限时必须说可能被截断")
	}
	// 对照：没取满就不说——「可能被截断」天天挂着等于没说。
	svc.list = full[:2]
	w = doJSON(t, h, http.MethodGet, "/api/v1/approvals?limit=3", approval.ScopeRead, "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Truncated {
		t.Fatal("没取满不该说被截断")
	}
}

func TestApprovalEndpointsRequireScopes(t *testing.T) {
	svc := &fakeApprovalService{req: sampleApproval()}
	h := approvalRouter(t, svc, &fakeApprovalExecutor{})
	const id = "/api/v1/approvals/11111111-2222-3333-4444-555555555555"

	cases := []struct {
		name, method, path, scopes string
		wantForbidden              bool
	}{
		{"列表缺 read", http.MethodGet, "/api/v1/approvals", "ops.read", true},
		{"列表有 read", http.MethodGet, "/api/v1/approvals", approval.ScopeRead, false},
		{"详情缺 read", http.MethodGet, id, "ops.read", true},
		{"详情有 read", http.MethodGet, id, approval.ScopeRead, false},
		{"投票缺 decide", http.MethodPost, id + "/decide", approval.ScopeRead, true},
		{"投票有 decide", http.MethodPost, id + "/decide", approval.ScopeDecide, false},
		{"撤回只需 read", http.MethodPost, id + "/cancel", approval.ScopeRead, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := ""
			if strings.HasSuffix(tc.path, "/decide") {
				body = `{"verdict":"APPROVE"}`
			}
			w := doJSON(t, h, tc.method, tc.path, tc.scopes, body)
			forbidden := w.Code == http.StatusForbidden
			if forbidden != tc.wantForbidden {
				t.Fatalf("状态码 %d（期望 403=%v）：%s", w.Code, tc.wantForbidden, w.Body.String())
			}
		})
	}
}

func TestDecideApprovalPassesVerdictAndPrincipal(t *testing.T) {
	svc := &fakeApprovalService{req: sampleApproval()}
	h := approvalRouter(t, svc, nil)
	const path = "/api/v1/approvals/11111111-2222-3333-4444-555555555555/decide"

	w := doJSON(t, h, http.MethodPost, path, approval.ScopeDecide,
		`{"verdict":"REJECT","comment":"参数不对"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", w.Code, w.Body.String())
	}
	if len(svc.voted) != 1 {
		t.Fatalf("没有投票：%+v", svc.voted)
	}
	v := svc.voted[0]
	if v.verdict != approval.VerdictReject || v.comment != "参数不对" {
		t.Fatalf("投票内容不对：%+v", v)
	}
	// 投票人取自身份而不是请求体——请求体里说自己是谁不算数。
	if v.who.ID != "staff_alice" || v.who.Type != principal.TypeHuman {
		t.Fatalf("投票人不对：%+v", v.who)
	}
	if !v.who.HasScope(approval.ScopeDecide) {
		t.Fatalf("投票人的 scope 没传下去：%+v", v.who.Scopes)
	}
}

func TestDecideApprovalRejectsBadBody(t *testing.T) {
	svc := &fakeApprovalService{req: sampleApproval()}
	h := approvalRouter(t, svc, nil)
	const path = "/api/v1/approvals/11111111-2222-3333-4444-555555555555/decide"

	for name, body := range map[string]string{
		"空 verdict": `{"verdict":""}`,
		"小写":        `{"verdict":"approve"}`,
		"未知字段":      `{"verdict":"APPROVE","approver_id":"staff_mallory"}`,
		"不是 JSON":   `nope`,
	} {
		t.Run(name, func(t *testing.T) {
			if w := doJSON(t, h, http.MethodPost, path, approval.ScopeDecide, body); w.Code != http.StatusBadRequest {
				t.Fatalf("状态码 %d：%s", w.Code, w.Body.String())
			}
			if len(svc.voted) != 0 {
				t.Fatalf("请求体不合法却投了票：%+v", svc.voted)
			}
		})
	}
}

// TestExecuteApprovalHasNoScopeGate：执行端点不挂 RequireScope——要什么权限
// 取决于单上那个 Action，只有内核知道。这里断言「一个只有 read 的人也能到达
// handler」，而不是断言他能执行成功（内核会按 def.Permission 拒他）。
func TestExecuteApprovalHasNoScopeGate(t *testing.T) {
	exec := &fakeApprovalExecutor{res: action.Result{RunID: uuid.New(), Value: "ok"}}
	h := approvalRouter(t, &fakeApprovalService{req: sampleApproval()}, exec)
	const path = "/api/v1/approvals/11111111-2222-3333-4444-555555555555/execute"

	w := doJSON(t, h, http.MethodPost, path, "some.unrelated.scope", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", w.Code, w.Body.String())
	}
	if exec.calls != 1 {
		t.Fatalf("没有到达内核：%d 次", exec.calls)
	}
	// 不带请求体时传 nil params（表示「按单上的来」），不是空 map。
	if exec.params[0] != nil {
		t.Fatalf("空请求体应当传 nil params，got %+v", exec.params[0])
	}
}

func TestExecuteApprovalForwardsDeclaredParams(t *testing.T) {
	exec := &fakeApprovalExecutor{res: action.Result{RunID: uuid.New()}}
	h := approvalRouter(t, &fakeApprovalService{req: sampleApproval()}, exec)
	const path = "/api/v1/approvals/11111111-2222-3333-4444-555555555555/execute"

	w := doJSON(t, h, http.MethodPost, path, approval.ScopeRead,
		`{"params":{"connection_id":"c-1","status":"ACTIVE"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", w.Code, w.Body.String())
	}
	if len(exec.params) != 1 || exec.params[0]["connection_id"] != "c-1" {
		t.Fatalf("声明的参数没传下去：%+v", exec.params)
	}
}

// TestApprovalRoutesAbsentWithoutService：没接审批中心时这组端点一概不存在。
//
// 断言的是「404」，而 404 也可能来自路径写错，所以配一个对照组：接上服务之后
// 同样的路径要变成非 404。
func TestApprovalRoutesAbsentWithoutService(t *testing.T) {
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	bare := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, ActionRegistry: action.NewRegistry(),
	})
	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/approvals"},
		{http.MethodGet, "/api/v1/approvals/11111111-2222-3333-4444-555555555555"},
		{http.MethodPost, "/api/v1/approvals/11111111-2222-3333-4444-555555555555/decide"},
		{http.MethodPost, "/api/v1/approvals/11111111-2222-3333-4444-555555555555/cancel"},
		{http.MethodPost, "/api/v1/approvals/11111111-2222-3333-4444-555555555555/execute"},
	}
	for _, p := range paths {
		if w := doJSON(t, bare, p.method, p.path, approval.ScopeRead+" "+approval.ScopeDecide, `{"verdict":"APPROVE"}`); w.Code != http.StatusNotFound {
			t.Fatalf("%s %s 在没接审批中心时应当 404，got %d", p.method, p.path, w.Code)
		}
	}

	wired := approvalRouter(t, &fakeApprovalService{req: sampleApproval()}, &fakeApprovalExecutor{})
	for _, p := range paths {
		if w := doJSON(t, wired, p.method, p.path, approval.ScopeRead+" "+approval.ScopeDecide, `{"verdict":"APPROVE"}`); w.Code == http.StatusNotFound {
			t.Fatalf("%s %s 接上之后不该还是 404", p.method, p.path)
		}
	}
}

// TestApprovalExecuteAbsentWithoutExecutor：只接了查询服务、没接内核时，
// 其余端点在，唯独执行端点不在。
func TestApprovalExecuteAbsentWithoutExecutor(t *testing.T) {
	h := approvalRouter(t, &fakeApprovalService{req: sampleApproval()}, nil)
	const path = "/api/v1/approvals/11111111-2222-3333-4444-555555555555/execute"

	if w := doJSON(t, h, http.MethodPost, path, approval.ScopeRead, ""); w.Code != http.StatusNotFound {
		t.Fatalf("没接内核时执行端点应当 404，got %d", w.Code)
	}
	if w := doJSON(t, h, http.MethodGet, "/api/v1/approvals", approval.ScopeRead, ""); w.Code != http.StatusOK {
		t.Fatal("其余端点应当照常可用")
	}
}

func TestApprovalServiceErrorsMapToStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"单不存在":  {action.NewError(action.CodeApprovalNotFound, "审批单不存在", nil), http.StatusNotFound},
		"已经落定":  {action.NewError(action.CodePreconditionFailed, "已经落定", nil), http.StatusPreconditionFailed},
		"重复投票":  {action.NewError(action.CodeConflict, "只能投一票", nil), http.StatusConflict},
		"AI 投票": {action.NewError(action.CodePrincipalTypeNotAllowed, "必须自然人", nil), http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeApprovalService{req: sampleApproval(), voteErr: tc.err}
			h := approvalRouter(t, svc, nil)
			w := doJSON(t, h, http.MethodPost,
				"/api/v1/approvals/11111111-2222-3333-4444-555555555555/decide",
				approval.ScopeDecide, `{"verdict":"APPROVE"}`)
			if w.Code != tc.want {
				t.Fatalf("状态码 %d，期望 %d：%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
