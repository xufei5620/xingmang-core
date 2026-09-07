package finance

import (
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// TestBindingActionErrorMapping 钉住渠道绑定 Action 的错误码映射。
//
// 在 XM-KERNEL-ERRCODE0 之前，这张表**在生产里一次都没生效过**——内核无条件
// 把 Handler 的错误改写成 EXECUTION_FAILED，于是这里写什么码都一样。修复之后
// 它们才真正决定调用方看到的 HTTP 状态，因此值得有一条测试把它当契约钉住，
// 而不是等下一次改动时靠人记得。
//
// （本包此前**没有任何**测试覆盖这张表；assurance 与 credentials 各有一条，
// alerts 与本包是空白。这是 XM-KERNEL-ERRCODE0 follow_up 第 2 条要求的复核
// 的一部分。）
func TestBindingActionErrorMapping(t *testing.T) {
	cases := map[string]struct {
		err  error
		want action.Code
	}{
		// 乐观并发的版本冲突。用 REVISION_CONFLICT 而不是笼统的 CONFLICT：
		// 两者都映射 409，但这一条与 approval 包里那些状态冲突（已执行过／
		// 重复投票）不是一回事，而 CodeRevisionConflict 正是为它声明的。
		// assurance 的 ErrVersionConflict 用同一个码。
		"绑定被别人改过": {ErrBindingConflict, action.CodeRevisionConflict},
		"渠道清单不完整": {ErrBindingPrecondition, action.CodePreconditionFailed},
		"资源不存在":   {ErrNotFound, action.CodeNotRegistered},
		"缺字段":     {ErrMissingField, action.CodeInvalidParams},
		"格式不对":    {ErrInvalidFormat, action.CodeInvalidParams},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := action.ErrorCode(bindingActionError(tc.err)); got != tc.want {
				t.Fatalf("bindingActionError(%v) = %q，期望 %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestBindingActionErrorDoesNotLeakUnknownErrors：认不出来的错误**不能**被
// 包成某个具体的域错误码。
//
// 库层 CHECK 违反、驱动错误之类的文本带约束名与内网细节，给它们一个具体的
// 4xx 码等于把「我们不知道这是什么」说成「你的请求有问题」，调用方会照着
// 一个错误的方向去改。归一成 EXECUTION_FAILED（502）并由内核换掉文案才对。
func TestBindingActionErrorDoesNotLeakUnknownErrors(t *testing.T) {
	leaky := errors.New(`pq: new row violates check constraint "binding_ratio_positive"`)
	got := bindingActionError(leaky)
	code := action.ErrorCode(got)
	if code != action.CodeExecutionFailed && code != action.CodeInternal {
		t.Fatalf("未知错误被映射成了 %q——那会让调用方以为是自己的请求有问题", code)
	}
	// 对照：认得出的错误确实拿到具体码，确认上面不是「所有错误都归一」。
	if action.ErrorCode(bindingActionError(ErrBindingConflict)) != action.CodeRevisionConflict {
		t.Fatal("认得出的错误应当拿到具体码")
	}
}
