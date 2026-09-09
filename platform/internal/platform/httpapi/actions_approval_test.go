package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// approvalRequiredExecutor 模拟内核把一次 L2+ 调用受理成审批单。
type approvalRequiredExecutor struct {
	approvalID string
	reasons    []string
}

func (e *approvalRequiredExecutor) Execute(_ context.Context, req action.Request) (action.Result, error) {
	e.reasons = append(e.reasons, req.Reason)
	err := action.NewError(action.CodeApprovalRequired,
		"action registry.connection.set_status 风险等级 L2 需要审批，已受理为审批单 "+e.approvalID, nil)
	var ae *action.Error
	if !asActionError(err, &ae) {
		panic("NewError 应当返回 *action.Error")
	}
	ae.ApprovalRequestID = e.approvalID
	return action.Result{}, ae
}

func asActionError(err error, target **action.Error) bool {
	ae, ok := err.(*action.Error)
	if ok {
		*target = ae
	}
	return ok
}

// TestExecuteActionReturns202WhenApprovalRequired：调用被**受理**了，不是失败。
//
// 走 WriteError 的话前端会把「已提交待审批」显示成红色的失败提示，服务端也会
// 记一条 error 级日志——两者都是错的。
func TestExecuteActionReturns202WhenApprovalRequired(t *testing.T) {
	exec := &approvalRequiredExecutor{approvalID: "11111111-2222-3333-4444-555555555555"}
	h := testRouter(t, exec, nil)

	r := httptest.NewRequest(http.MethodPost,
		executePath("registry.connection.set_status", "1"),
		strings.NewReader(`{"params":{"connection_id":"c-1"},"reason":"上游换域名"}`))
	r.Header.Set("Content-Type", "application/json")
	devHeaders(r, "registry.service.manage")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("状态码 %d，期望 202：%s", w.Code, w.Body.String())
	}
	var got approvalRequiredResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if got.ApprovalRequestID != exec.approvalID {
		t.Fatalf("单号没带回来：%+v", got)
	}
	if got.Status != string(action.CodeApprovalRequired) {
		t.Fatalf("status 字段不对：%s", got.Status)
	}
	// 响应体**不是**错误体：不该有 error 包装，否则前端的通用错误处理会先接住它。
	var maybeErr ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &maybeErr); err == nil && maybeErr.Error.Code != "" {
		t.Fatalf("202 的响应体不该是错误体：%s", w.Body.String())
	}
	// reason 要真的传到内核——它是审批人唯一能据以判断的东西。
	if len(exec.reasons) != 1 || exec.reasons[0] != "上游换域名" {
		t.Fatalf("reason 没传下去：%+v", exec.reasons)
	}
}

// TestExecuteActionStillErrorsForRealFailures：只有 APPROVAL_REQUIRED 走 202，
// 别的错误码原样走错误路径。这条防的是「把 202 那一支写宽了」。
func TestExecuteActionStillErrorsForRealFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		code action.Code
		want int
	}{
		"权限不足":     {action.CodePermissionDenied, http.StatusForbidden},
		"参数不合法":    {action.CodeInvalidParams, http.StatusBadRequest},
		"仍未实装高级控制": {action.CodeAdvancedControlsRequired, http.StatusNotImplemented},
	} {
		t.Run(name, func(t *testing.T) {
			h := testRouter(t, fixedErrorExecutor{code: tc.code}, nil)
			r := httptest.NewRequest(http.MethodPost,
				executePath("registry.connection.set_status", "1"),
				strings.NewReader(`{"params":{}}`))
			r.Header.Set("Content-Type", "application/json")
			devHeaders(r, "registry.service.manage")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != tc.want {
				t.Fatalf("状态码 %d，期望 %d：%s", w.Code, tc.want, w.Body.String())
			}
			var body ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != string(tc.code) {
				t.Fatalf("应当是标准错误体：%s", w.Body.String())
			}
		})
	}
}

type fixedErrorExecutor struct{ code action.Code }

func (e fixedErrorExecutor) Execute(context.Context, action.Request) (action.Result, error) {
	return action.Result{}, action.NewError(e.code, "测试用错误", nil)
}
