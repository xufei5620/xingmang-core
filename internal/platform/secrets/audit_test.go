package secrets

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

type captureRecorder struct{ recs []AccessRecord }

func (c *captureRecorder) RecordSecretAccess(_ context.Context, rec AccessRecord) {
	c.recs = append(c.recs, rec)
}

func newTestAudited(rec AccessRecorder) SecretProvider {
	env, _ := NewEnvProvider(
		map[string]string{"secret://alerting/tg": "XM_TG"},
		WithLookup(fakeLookup(map[string]string{"XM_TG": "test-value-a"})),
	)
	return NewAudited(env, rec, "development")
}

func TestAuditedRecordsSuccessAndFailure(t *testing.T) {
	got := &captureRecorder{}
	p := newTestAudited(got)
	ctx := WithCaller(context.Background(), "connector:sub2api")

	if _, err := p.Resolve(ctx, MustCredentialRef("secret://alerting/tg"), "alert delivery"); err != nil {
		t.Fatal(err)
	}
	_, _ = p.Resolve(ctx, MustCredentialRef("secret://alerting/missing"), "alert delivery")

	if len(got.recs) != 2 {
		t.Fatalf("应记录 2 条, got %d", len(got.recs))
	}
	ok, fail := got.recs[0], got.recs[1]
	if !ok.Success || ok.Caller != "connector:sub2api" || ok.Environment != "development" ||
		ok.Purpose != "alert delivery" || ok.Provider != "env" || ok.ErrorCode != "" || ok.At.IsZero() {
		t.Fatalf("成功记录字段不全: %+v", ok)
	}
	if fail.Success || fail.ErrorCode != "unmapped" {
		t.Fatalf("失败记录应含 error_code: %+v", fail)
	}
}

func TestAuditedCallerDefault(t *testing.T) {
	got := &captureRecorder{}
	p := newTestAudited(got)
	_, _ = p.Resolve(context.Background(), MustCredentialRef("secret://alerting/tg"), "t")
	if got.recs[0].Caller != "unknown" {
		t.Fatalf("未设置 caller 应为 unknown: %+v", got.recs[0])
	}
}

func TestSlogRecorderNeverLogsPlaintext(t *testing.T) {
	var sb strings.Builder
	p := newTestAudited(NewSlogRecorder(slog.New(slog.NewTextHandler(&sb, nil))))
	_, _ = p.Resolve(context.Background(), MustCredentialRef("secret://alerting/tg"), "t")
	out := sb.String()
	if strings.Contains(out, "test-value-a") {
		t.Fatalf("审计日志泄漏明文: %s", out)
	}
	for _, want := range []string{"secret_access", "secret://alerting/tg", "module=secrets", "environment=development"} {
		if !strings.Contains(out, want) {
			t.Fatalf("审计日志缺少 %q: %s", want, out)
		}
	}
}

func TestAuditedMetadataPassthrough(t *testing.T) {
	got := &captureRecorder{}
	p := newTestAudited(got)
	m, err := p.Metadata(context.Background(), MustCredentialRef("secret://alerting/tg"))
	if err != nil || !m.Available {
		t.Fatalf("Metadata 透传失败: %+v, %v", m, err)
	}
	if len(got.recs) != 0 {
		t.Fatal("Metadata 不应产生凭据读取审计")
	}
}
