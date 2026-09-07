package alerts

import (
	"errors"
	"fmt"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// TestDomainErrorMapping 钉住本包的仓储错误映射。
//
// 本包此前**没有这张表**——`store.Get` 的错误是裸返回的，于是「确认一个不存在
// 的 alert_id」在内核那里被归一成 EXECUTION_FAILED（502），像是服务端坏了，
// 而实际上是调用方给的 id 不对。
//
// 这个错分在 XM-KERNEL-ERRCODE0 之前**看不出来**：那时所有 Handler 错误都是
// 502。修完内核之后它成了唯一还错着的那一类，也正是那一片 follow_up 第 2 条
// 要求各包复核的东西。
func TestDomainErrorMapping(t *testing.T) {
	cases := map[string]struct {
		err  error
		want action.Code
	}{
		"nil 原样": {nil, ""},
		"告警不存在":  {ErrNotFound, action.CodePreconditionFailed},
		// 包装过的也要认出来：store.Get 返回的是 fmt.Errorf("alert %s: %w", …)。
		"包装过的不存在": {fmt.Errorf("alert 123: %w", ErrNotFound), action.CodePreconditionFailed},
		// 认不出的错误**原样返回**（不包 *action.Error）。这里断言的是
		// ErrorCode 对非 Action 错误的兜底值 INTERNAL——归一成
		// EXECUTION_FAILED 发生在**内核**里，不在这一层；下面那条用
		// errors.As 更直接地钉住「没有被包装」这件事。
		"认不出的仓储错误": {errors.New("pq: connection reset"), action.CodeInternal},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := action.ErrorCode(domainError(tc.err)); got != tc.want {
				t.Fatalf("domainError(%v) = %q，期望 %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestDomainErrorDoesNotDressUpUnknownErrors：认不出来的错误**不能**被包成
// 某个具体的域错误码。
//
// 给一个驱动错误安上 4xx，等于把「我们不知道这是什么」说成「你的请求有问题」，
// 调用方会照着错误的方向去改。这条与「不存在 → 412」是同一枚硬币的两面：
// 前者要求认得出的分类准，后者要求认不出的别硬猜。
func TestDomainErrorDoesNotDressUpUnknownErrors(t *testing.T) {
	leaky := errors.New(`pq: new row violates check constraint "alert_severity_check"`)
	got := domainError(leaky)
	// 契约是「原样返回」：没有被包成任何 *action.Error。内核见到非 *Error
	// 才会归一成 EXECUTION_FAILED 并换掉文案，细节只进服务端日志。
	var ae *action.Error
	if errors.As(got, &ae) {
		t.Fatalf("未知错误被包成了 %q——调用方会以为是自己的请求有问题", ae.Code)
	}
	if !errors.Is(got, leaky) {
		t.Fatalf("未知错误没有原样返回：%v", got)
	}
	// 对照：认得出的确实拿到具体码，确认上面不是「所有错误都归一」。
	if action.ErrorCode(domainError(ErrNotFound)) != action.CodePreconditionFailed {
		t.Fatal("认得出的错误应当拿到具体码")
	}
}
