package secrets

import (
	"context"
	"log/slog"
	"time"
)

// AccessRecord 是一次凭据读取的审计事实（规格 §4.5：谁请求/哪个用途/
// 哪个环境/成功失败；绝不含明文）。
type AccessRecord struct {
	At          time.Time
	Ref         CredentialRef
	Purpose     string
	Caller      string // 请求方标识，如 "connector:sub2api"、"module:alerts"
	Environment string
	Provider    string // 实际解析的 Provider ID
	Success     bool
	ErrorCode   string // errKind 分类；成功为空
}

// AccessRecorder 接收审计记录。正式审计模块（XM-0011 后续）实现本接口；
// 在此之前用 SlogRecorder 落结构化日志。
type AccessRecorder interface {
	RecordSecretAccess(ctx context.Context, rec AccessRecord)
}

type slogRecorder struct{ l *slog.Logger }

// NewSlogRecorder 用结构化日志记录凭据访问（字段风格按规格 §18.8）。
func NewSlogRecorder(l *slog.Logger) AccessRecorder { return slogRecorder{l: l} }

func (s slogRecorder) RecordSecretAccess(ctx context.Context, rec AccessRecord) {
	s.l.LogAttrs(ctx, slog.LevelInfo, "secret_access",
		slog.String("module", "secrets"),
		slog.String("environment", rec.Environment),
		slog.String("principal_id", rec.Caller),
		slog.String("credential_ref", rec.Ref.String()),
		slog.String("purpose", rec.Purpose),
		slog.String("provider", rec.Provider),
		slog.Bool("success", rec.Success),
		slog.String("error_code", rec.ErrorCode),
	)
}

type callerKey struct{}

// WithCaller 在 ctx 标记请求方身份（如 "connector:sub2api"）。
func WithCaller(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// CallerFrom 取出请求方身份；未设置返回 "unknown"。
func CallerFrom(ctx context.Context) string {
	if v, ok := ctx.Value(callerKey{}).(string); ok && v != "" {
		return v
	}
	return "unknown"
}

type audited struct {
	inner SecretProvider
	rec   AccessRecorder
	env   string
}

// NewAudited 包装任意 Provider：每次 Resolve（无论成败）都产生审计记录。
// Metadata 只回答可用性、不触达明文，不计入凭据读取审计。
func NewAudited(inner SecretProvider, rec AccessRecorder, environment string) SecretProvider {
	return &audited{inner: inner, rec: rec, env: environment}
}

func providerID(p SecretProvider) string {
	if ider, ok := p.(interface{ ID() string }); ok {
		return ider.ID()
	}
	return "unknown"
}

func (a *audited) Resolve(ctx context.Context, ref CredentialRef, purpose string) (SecretValue, error) {
	v, err := a.inner.Resolve(ctx, ref, purpose)
	a.rec.RecordSecretAccess(ctx, AccessRecord{
		At:          time.Now().UTC(),
		Ref:         ref,
		Purpose:     purpose,
		Caller:      CallerFrom(ctx),
		Environment: a.env,
		Provider:    providerID(a.inner),
		Success:     err == nil,
		ErrorCode:   errKind(err),
	})
	return v, err
}

func (a *audited) Metadata(ctx context.Context, ref CredentialRef) (SecretMetadata, error) {
	return a.inner.Metadata(ctx, ref)
}
