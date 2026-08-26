package audit

import (
	"context"
	"fmt"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// ActionSink 把 Action 内核的执行事实写进审计链。
//
// 适配器放在 audit 包而不是 action 包：内核只声明 action.AuditSink 接口，
// 对审计实现零依赖；「怎么变成一条审计事件」属于审计设施自己的知识。
type ActionSink struct {
	store *Store
}

// NewActionSink 创建适配器。用法见 cmd/platform-api/main.go：
//
//	kernel := action.NewKernel(reg, runs, action.WithAuditSink(audit.NewActionSink(auditStore)))
func NewActionSink(store *Store) *ActionSink { return &ActionSink{store: store} }

var _ action.AuditSink = (*ActionSink)(nil)

// Append 把内核事件转成审计事件并追加到链上。
func (s *ActionSink) Append(ctx context.Context, e action.AuditEvent) error {
	result := ResultSucceeded
	if !e.Succeeded {
		result = ResultFailed
	}

	ev := Event{
		OccurredAt:    e.OccurredAt,
		PrincipalID:   e.PrincipalID,
		PrincipalType: e.PrincipalType,
		ActionID:      e.ActionID,
		ActionVersion: e.ActionVersion,
		ActionRunID:   e.ActionRunID,
		ResourceType:  e.ResourceType,
		ResourceID:    e.ResourceID,
		Environment:   e.Environment,
		Reason:        e.Reason,
		RequestID:     e.RequestID,
		BeforeSummary: e.BeforeSummary,
		AfterSummary:  e.AfterSummary,
		Result:        result,
	}
	// 失败时把错误码留在链上：审计要能回答「为什么没做成」，
	// 而错误码是唯一不含敏感信息又足够精确的答案（规格 §5.8 错误码是契约）
	if !e.Succeeded && e.ErrorCode != "" {
		ev.CompensationResult = string(e.ErrorCode)
	}

	if _, err := s.store.Append(ctx, ev); err != nil {
		return fmt.Errorf("append audit event for action %s: %w", e.ActionID, err)
	}
	return nil
}
