package action

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// AuditEvent 是内核交给审计设施的执行事实（字段对齐规格 §4.4）。
//
// 内核**不 import audit 包**：审计实现通过本接口接入，两个核心包保持互不依赖。
type AuditEvent struct {
	OccurredAt    time.Time
	PrincipalID   string
	PrincipalType principal.Type
	ActionID      string
	ActionVersion string
	ActionRunID   uuid.UUID
	ResourceType  string
	ResourceID    string
	Environment   string
	Reason        string
	RequestID     string

	BeforeSummary map[string]any
	AfterSummary  map[string]any

	Succeeded bool
	ErrorCode Code
}

// AuditSink 接收审计事件。实现方负责哈希链、签名与持久化。
//
// 返回错误表示审计没写进去——这是事故（规格 §4.4 要求审计可追溯），
// 但**不回滚已经发生的业务变更**：业务写与审计写不在同一事务里。
// 见 docs/modules/action/README.md 对这个缺口的说明与 Foundation-B 的补法。
type AuditSink interface {
	Append(ctx context.Context, e AuditEvent) error
}

// auditMeta 收集 Handler 贡献的业务侧审计信息。
//
// 为什么用 ctx 而不是改 Handler 签名：resource_type / before / after 是业务知识，
// 只有 Handler 知道；但把它们塞进返回值会迫使每个 Handler 都改签名，
// 连那些没有资源概念的动作也不例外。ctx 让贡献成为**可选**的。
type auditMeta struct {
	mu           sync.Mutex
	resourceType string
	resourceID   string
	reason       string
	before       map[string]any
	after        map[string]any
}

type auditMetaKey struct{}

func withAuditMeta(ctx context.Context) (context.Context, *auditMeta) {
	m := &auditMeta{}
	return context.WithValue(ctx, auditMetaKey{}, m), m
}

func metaFrom(ctx context.Context) *auditMeta {
	m, _ := ctx.Value(auditMetaKey{}).(*auditMeta)
	return m
}

// RecordResource 由 Handler 调用，声明本次动作作用在哪个资源上。
// 不在 Action 执行上下文中调用时静默忽略（便于 Handler 被单独测试）。
func RecordResource(ctx context.Context, resourceType, resourceID string) {
	if m := metaFrom(ctx); m != nil {
		m.mu.Lock()
		m.resourceType, m.resourceID = resourceType, resourceID
		m.mu.Unlock()
	}
}

// RecordBefore 记录变更前摘要。**调用方负责脱敏**——本函数不判断什么是敏感的。
func RecordBefore(ctx context.Context, summary map[string]any) {
	if m := metaFrom(ctx); m != nil {
		m.mu.Lock()
		m.before = summary
		m.mu.Unlock()
	}
}

// RecordAfter 记录变更后摘要。**调用方负责脱敏**。
func RecordAfter(ctx context.Context, summary map[string]any) {
	if m := metaFrom(ctx); m != nil {
		m.mu.Lock()
		m.after = summary
		m.mu.Unlock()
	}
}

// RecordReason 记录本次动作的理由（L3/L4 场景由审批流填入）。
func RecordReason(ctx context.Context, reason string) {
	if m := metaFrom(ctx); m != nil {
		m.mu.Lock()
		m.reason = reason
		m.mu.Unlock()
	}
}

func (m *auditMeta) snapshot() (resourceType, resourceID, reason string, before, after map[string]any) {
	if m == nil {
		return "", "", "", nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resourceType, m.resourceID, m.reason, m.before, m.after
}
