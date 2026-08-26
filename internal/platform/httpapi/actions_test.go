package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

type fakeExecutor struct {
	gotReq action.Request
	result action.Result
	err    error
}

func (f *fakeExecutor) Execute(_ context.Context, req action.Request) (action.Result, error) {
	f.gotReq = req
	return f.result, f.err
}

func TestExecuteActionSuccessReturnsActionRunID(t *testing.T) {
	runID := uuid.New()
	exec := &fakeExecutor{result: action.Result{RunID: runID, Value: map[string]string{"instance_id": "sub2api-prod"}}}
	h := testRouter(t, exec, nil)

	body := strings.NewReader(`{"params":{"service_type":"sub2api"}}`)
	req := httptest.NewRequest(http.MethodPost, executePath("registry.service.create", "1"), body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-abc")
	devHeaders(req, "registry.service.manage")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	// 规格 §5.8：所有写接口返回 action_run_id
	if got["action_run_id"] != runID.String() {
		t.Fatalf("action_run_id = %v, want %s", got["action_run_id"], runID)
	}
	// 内核收到的 RequestID 必须是中间件那一份，保证日志与审计可串联
	if exec.gotReq.RequestID != "req-abc" {
		t.Fatalf("内核收到的 request_id = %q", exec.gotReq.RequestID)
	}
	if exec.gotReq.ActionID != "registry.service.create" || exec.gotReq.ActionVersion != "1" {
		t.Fatalf("路径参数解析错误: %+v", exec.gotReq)
	}
	if exec.gotReq.Params["service_type"] != "sub2api" {
		t.Fatalf("参数未透传: %+v", exec.gotReq.Params)
	}
}

func TestExecuteActionMapsKernelErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		"权限不足": {action.NewError(action.CodePermissionDenied, "缺少权限", nil), http.StatusForbidden, "PERMISSION_DENIED"},
		"参数非法": {action.NewError(action.CodeInvalidParams, "参数不符", nil), http.StatusBadRequest, "INVALID_PARAMS"},
		"未注册":  {action.NewError(action.CodeNotRegistered, "未注册", nil), http.StatusNotFound, "ACTION_NOT_REGISTERED"},
		"需高级控制": {action.NewError(action.CodeAdvancedControlsRequired, "需 Foundation-B", nil),
			http.StatusNotImplemented, "ADVANCED_CONTROLS_REQUIRED"},
	} {
		h := testRouter(t, &fakeExecutor{err: tc.err}, nil)
		req := httptest.NewRequest(http.MethodPost,
			executePath("registry.service.create", "1"), strings.NewReader(`{"params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		devHeaders(req, "registry.service.manage")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != tc.wantStatus {
			t.Fatalf("%s: status = %d, want %d", name, rec.Code, tc.wantStatus)
		}
		var got ErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.Error.Code != tc.wantCode {
			t.Fatalf("%s: code = %q, want %q", name, got.Error.Code, tc.wantCode)
		}
	}
}

func TestExecuteActionRejectsBadBody(t *testing.T) {
	h := testRouter(t, &fakeExecutor{}, nil)
	for name, body := range map[string]string{
		"非 JSON": `not json`,
		"顶层是数组":  `[1,2,3]`,
		"未知字段":   `{"params":{},"sneaky":true}`,
	} {
		req := httptest.NewRequest(http.MethodPost,
			executePath("registry.service.create", "1"), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		devHeaders(req, "registry.service.manage")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestExecuteActionRequiresPrincipal(t *testing.T) {
	h := testRouter(t, &fakeExecutor{}, nil)
	req := httptest.NewRequest(http.MethodPost,
		executePath("registry.service.create", "1"), strings.NewReader(`{"params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无身份应 403, got %d", rec.Code)
	}
}

func TestExecuteActionRejectsOversizedBody(t *testing.T) {
	// 规格 §18.1-4：所有外部输入必须有大小上限
	h := testRouter(t, &fakeExecutor{}, nil)
	huge := `{"params":{"x":"` + strings.Repeat("a", 2*1024*1024) + `"}}`
	req := httptest.NewRequest(http.MethodPost,
		executePath("registry.service.create", "1"), strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	devHeaders(req, "registry.service.manage")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大请求体应 400, got %d", rec.Code)
	}
}
