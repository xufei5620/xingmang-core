package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

func TestStatusForCode(t *testing.T) {
	for code, want := range map[action.Code]int{
		action.CodeInvalidParams:            http.StatusBadRequest,
		action.CodePermissionDenied:         http.StatusForbidden,
		action.CodePrincipalTypeNotAllowed:  http.StatusForbidden,
		action.CodeEnvironmentMismatch:      http.StatusConflict,
		action.CodeConflict:                 http.StatusConflict,
		action.CodePreconditionFailed:       http.StatusPreconditionFailed,
		action.CodeNotRegistered:            http.StatusNotFound,
		action.CodeAdvancedControlsRequired: http.StatusNotImplemented,
		action.CodeExecutionFailed:          http.StatusBadGateway,
		action.CodeRunwayConfigUnavailable:  http.StatusServiceUnavailable,
		action.CodeRevisionConflict:         http.StatusConflict,
		action.CodeInternal:                 http.StatusInternalServerError,
		action.Code("SOMETHING_NEW"):        http.StatusInternalServerError,
	} {
		if got := StatusForCode(code); got != want {
			t.Fatalf("StatusForCode(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestWriteErrorMapsActionError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/x/1:execute", nil)

	cause := errors.New("pq: duplicate key value violates unique constraint \"service_type_instance_key\"")
	kernelErr := fmt.Errorf("wrapped: %w", action.NewError(action.CodeExecutionFailed, "服务登记失败", cause))

	WriteError(rec, req, kernelErr)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "pq:") || strings.Contains(body, "constraint") {
		t.Fatalf("响应泄漏底层细节: %s", body)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, body)
	}
	if got.Error.Code != string(action.CodeExecutionFailed) {
		t.Fatalf("code = %q", got.Error.Code)
	}
	if got.Error.Message == "" {
		t.Fatal("message 不应为空")
	}
}

func TestWriteErrorNonActionErrorIsInternalAndOpaque(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)

	WriteError(rec, req, errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "connection refused") {
		t.Fatalf("响应泄漏内部细节: %s", body)
	}
	var got ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Error.Code != string(action.CodeInternal) {
		t.Fatalf("code = %q, want INTERNAL", got.Error.Code)
	}
}

func TestWriteErrorMapsRunwayUnavailableWithoutLeakingCause(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/runway-thresholds", nil)
	cause := errors.New("pq: relation finance.runway_threshold_config at 10.0.0.4:5432 constraint secret")
	WriteError(rec, req, action.NewError(action.CodeRunwayConfigUnavailable, "可用天数阈值配置暂不可用", cause))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "10.0.0.4") || strings.Contains(body, "constraint") || strings.Contains(body, "pq:") {
		t.Fatalf("response leaked cause: %s", body)
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]string{"a": "b"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if strings.TrimSpace(rec.Body.String()) != `{"a":"b"}` {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
