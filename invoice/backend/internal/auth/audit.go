package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type SecuritySeverity string

const (
	SeverityInfo     SecuritySeverity = "info"
	SeverityNotice   SecuritySeverity = "notice"
	SeverityWarning  SecuritySeverity = "warning"
	SeverityCritical SecuritySeverity = "critical"
)

type SecurityAuditEvent struct {
	ID           string
	ActorType    string
	ActorID      string
	Action       string
	ObjectType   string
	ObjectID     string
	RequestID    string
	SourceIPHash string
	BeforeHash   string
	AfterHash    string
	Reason       string
	Severity     SecuritySeverity
	CreatedAt    time.Time
}

func (e SecurityAuditEvent) Validate() error {
	for name, value := range map[string]string{
		"actor_type": e.ActorType, "actor_id": e.ActorID, "action": e.Action,
		"object_type": e.ObjectType, "object_id": e.ObjectID, "request_id": e.RequestID,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 512 || hasControl(value) {
			return errors.New("security audit " + name + " is missing or invalid")
		}
	}
	if strings.TrimSpace(e.RequestID) != e.RequestID {
		return errors.New("security audit request_id contains surrounding whitespace")
	}
	if e.Reason != "" && (len(e.Reason) > 500 || hasControl(e.Reason)) {
		return errors.New("security audit reason is invalid")
	}
	switch e.Severity {
	case SeverityInfo, SeverityNotice, SeverityWarning, SeverityCritical:
	default:
		return errors.New("security audit severity is invalid")
	}
	return nil
}

type SecurityAuditSink interface {
	RecordSecurityEvent(ctx context.Context, event SecurityAuditEvent) error
}

func (m *SessionManager) record(ctx context.Context, event SecurityAuditEvent) error {
	if m.audit == nil {
		return errors.New("security audit sink is required")
	}
	if event.ID == "" {
		event.ID = randomUUIDv4()
	}
	if event.RequestID == "" {
		event.RequestID = "not-provided"
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = m.now().UTC()
	}
	return m.audit.RecordSecurityEvent(ctx, event)
}

type MemorySecurityAuditSink struct {
	mu     sync.Mutex
	Events []SecurityAuditEvent
}

func (m *MemorySecurityAuditSink) RecordSecurityEvent(_ context.Context, event SecurityAuditEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = append(m.Events, event)
	return nil
}
