package action

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorCodeExtraction(t *testing.T) {
	e := newError(CodePermissionDenied, "缺少权限 registry.read", nil)
	if ErrorCode(e) != CodePermissionDenied {
		t.Fatalf("ErrorCode = %q", ErrorCode(e))
	}
	if ErrorCode(errors.New("plain")) != CodeInternal {
		t.Fatal("非 action 错误应归类为 internal")
	}
	if ErrorCode(nil) != "" {
		t.Fatal("nil 错误的 code 应为空")
	}
}

func TestErrorDoesNotLeakCause(t *testing.T) {
	cause := errors.New("pq: duplicate key value violates unique constraint \"service_type_instance_key\"")
	e := newError(CodeExecutionFailed, "服务登记失败", cause)
	if strings.Contains(e.Error(), "pq:") || strings.Contains(e.Error(), "constraint") {
		t.Fatalf("对外错误信息泄漏底层细节: %s", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is 应能追到根因")
	}
}

func TestErrorWrappingChain(t *testing.T) {
	base := newError(CodeInvalidParams, "参数非法", nil)
	wrapped := fmt.Errorf("execute action: %w", base)
	if ErrorCode(wrapped) != CodeInvalidParams {
		t.Fatalf("包装后仍应能取到 code, got %q", ErrorCode(wrapped))
	}
}
