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
//
// 摘要在这里**强制**过一遍 RedactDefault（XM-0031，回归 Codex 冷审 PR #47
// 第 2 条 / PR #43 head `0a0642c` 第 2 条：「摘要可把明文凭据直接回给前端」）。
//
// 这是纵深防御，不是把责任从 Handler 挪过来。Handler 仍然该脱敏——它最清楚
// 自己那些字段的含义，也只有它拦得住「以别的名字混进来的凭据」。但
// `action/audit.go` 把脱敏责任**完全**交给 Handler，于是整条链上没有任何机械
// 保证：一个新写的 Handler 忘了脱敏，凭据就会一路写进审计链，再经
// /api/v1/audit/events 原样回显给前端。宪法 7 条要求的是在边界上机械 fail
// closed，而这里就是那个边界——所有 Action 审计事件唯一的入链口。
//
// 为什么脱敏放在写入侧而不是只在读 API 上做：审计链不可篡改。一旦明文凭据
// 进了链，它就**永远**在那里——改读 API 只是不再显示它，库里那条记录、导出
// 的链根、任何一份备份里它都还在。凭据必须从一开始就进不去。
//
// 代价与取舍：RedactDefault 按键名子串匹配，可能把一个名字里带 "token" 但
// 其实无害的字段也打上 [REDACTED]。接受——审计摘要少一个字段可以从
// action_run 与业务表复原，多一个凭据不可能收回。
func (s *ActionSink) Append(ctx context.Context, e action.AuditEvent) error {
	ev := eventFromActionEvent(e)
	if _, err := s.store.Append(ctx, ev); err != nil {
		return fmt.Errorf("append audit event for action %s: %w", e.ActionID, err)
	}
	return nil
}

// eventFromActionEvent 是「内核事件 → 审计事件」的纯映射。
//
// 从 Append 里拆出来，是为了让脱敏这条边界规则能被**不带数据库**地测到：
// 一条只验「[REDACTED] 有没有出现」的断言不该依赖一个跑着的 Postgres，
// 否则它在没有库的机器上会被 t.Skip 掉，而这正是最需要它一直跑着的规则。
// 链上端到端的证明另有集成测试。
func eventFromActionEvent(e action.AuditEvent) Event {
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
		BeforeSummary: RedactDefault(e.BeforeSummary),
		AfterSummary:  RedactDefault(e.AfterSummary),
		Result:        result,
	}
	// 失败时把错误码留在链上：审计要能回答「为什么没做成」，
	// 而错误码是唯一不含敏感信息又足够精确的答案（规格 §5.8 错误码是契约）
	if !e.Succeeded && e.ErrorCode != "" {
		ev.CompensationResult = string(e.ErrorCode)
	}
	return ev
}
