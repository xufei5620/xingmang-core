package credentials

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

type fakeStore struct {
	result    WriteResult
	err       error
	lastRef   secrets.CredentialRef
	lastValue string
	lastEnv   string
	lastActor string
	lastOp    string
	reason    string

	cfgBefore *ConnectorConfig
	cfgAfter  ConnectorConfig
	cfgErr    error
	lastCfg   ConnectorConfig
}

func (f *fakeStore) Upsert(_ context.Context, ref secrets.CredentialRef, value secrets.SecretValue, env, actor string) (WriteResult, error) {
	f.lastOp, f.lastRef, f.lastValue, f.lastEnv, f.lastActor = "upsert", ref, value.Reveal(), env, actor
	return f.result, f.err
}

func (f *fakeStore) Rotate(_ context.Context, ref secrets.CredentialRef, value secrets.SecretValue, env, actor string) (WriteResult, error) {
	f.lastOp, f.lastRef, f.lastValue, f.lastEnv, f.lastActor = "rotate", ref, value.Reveal(), env, actor
	return f.result, f.err
}

func (f *fakeStore) Revoke(_ context.Context, ref secrets.CredentialRef, env, actor, reason string) (WriteResult, error) {
	f.lastOp, f.lastRef, f.lastEnv, f.lastActor, f.reason = "revoke", ref, env, actor, reason
	return f.result, f.err
}

func (f *fakeStore) SetConnectorConfig(_ context.Context, cfg ConnectorConfig, actor string) (*ConnectorConfig, ConnectorConfig, error) {
	f.lastCfg, f.lastActor = cfg, actor
	if f.cfgErr != nil {
		return nil, ConnectorConfig{}, f.cfgErr
	}
	after := f.cfgAfter
	if after.Platform == "" {
		after = cfg
		after.Version = 1
		after.UpdatedAt = time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
		after.UpdatedBy = actor
	}
	return f.cfgBefore, after, nil
}

func humanContext(env string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "https://auth.example/realms/staff", Subject: "sub-alice",
		Environment: env, Scopes: []string{ScopeManage, ScopeConnectorManage},
	})
}

const testValue = "sk-test-value-7c1e"

func metadata(version int, fingerprint string) Metadata {
	return Metadata{
		Ref: "secret://sub2api-prod/read-token", Scope: "sub2api-prod", Name: "read-token",
		Environment: "staging", Fingerprint: fingerprint, Version: version,
		UpdatedAt: time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC), UpdatedBy: "staff_alice", Available: true,
	}
}

func TestActionDefinitions(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil); err != nil {
		t.Fatal(err)
	}
	for id, permission := range map[string]string{
		ActionSecretUpsert:       ScopeManage,
		ActionSecretRotate:       ScopeManage,
		ActionSecretRevoke:       ScopeManage,
		ActionConnectorConfigSet: ScopeConnectorManage,
	} {
		def, _, ok := reg.Lookup(id, "1")
		if !ok {
			t.Fatalf("missing %s", id)
		}
		if def.RiskLevel != action.L1 || def.Permission != permission {
			t.Fatalf("%s definition = %#v", id, def)
		}
		if len(def.PrincipalTypes) != 1 || def.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s principal types = %#v", id, def.PrincipalTypes)
		}
		if strings.Join(def.Environments, ",") != "development,staging,production" {
			t.Fatalf("%s environments = %#v", id, def.Environments)
		}
	}
	// 未绑定仓储时执行一律失败，而不是静默成功
	_, h, _ := reg.Lookup(ActionSecretUpsert, "1")
	if _, err := h(humanContext("staging"), map[string]any{
		"credential_ref": "secret://a/b", "secret_value": testValue,
	}); action.ErrorCode(err) != action.CodeExecutionFailed {
		t.Fatalf("nil store 应 EXECUTION_FAILED, got %v", err)
	}
}

func TestSchemaRejectsEnvironmentAndActorSmuggling(t *testing.T) {
	for _, def := range []action.Definition{
		secretDefinition(ActionSecretUpsert), revokeDefinition(), connectorConfigSetDefinition(),
	} {
		for _, field := range []string{"environment", "actor", "updated_by", "principal_id"} {
			params := map[string]any{"credential_ref": "secret://a/b", "secret_value": "x", "reason": "r",
				"platform": "sub2api", "mode": "fake"}
			params[field] = "attacker"
			if err := def.Schema.Validate(params); !errors.Is(err, action.ErrUnknownField) {
				t.Fatalf("%s: field %s error = %v", def.ID, field, err)
			}
		}
	}
	def := connectorConfigSetDefinition()
	if err := def.Schema.Validate(map[string]any{"platform": "openai", "mode": "fake"}); !errors.Is(err, action.ErrEnumViolation) {
		t.Fatalf("未知平台应 ErrEnumViolation, got %v", err)
	}
	if err := def.Schema.Validate(map[string]any{"platform": "newapi", "mode": "shadow"}); !errors.Is(err, action.ErrEnumViolation) {
		t.Fatalf("未知模式应 ErrEnumViolation, got %v", err)
	}
}

func TestUpsertHandlerUsesPrincipalAndNeverExposesValue(t *testing.T) {
	fake := &fakeStore{result: WriteResult{After: metadata(1, "ba7816bf8f01cfea")}}
	value, auditInfo, err := action.CaptureAudit(humanContext("staging"), secretWriteHandler(fake, false), map[string]any{
		"credential_ref": " secret://sub2api-prod/read-token ", "secret_value": "  " + testValue + "\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastOp != "upsert" || fake.lastRef.String() != "secret://sub2api-prod/read-token" ||
		fake.lastValue != testValue || fake.lastEnv != "staging" || fake.lastActor != "staff_alice" {
		t.Fatalf("store call = %#v", fake)
	}
	out, ok := value.(map[string]any)
	if !ok || out["credential_ref"] != "secret://sub2api-prod/read-token" ||
		out["fingerprint"] != "ba7816bf8f01cfea" || out["version"] != 1 {
		t.Fatalf("result = %#v", value)
	}
	if _, leaked := out["secret_value"]; leaked {
		t.Fatal("结果不得含值")
	}
	if auditInfo.ResourceType != resourceCredentialRef || auditInfo.ResourceID != "secret://sub2api-prod/read-token" {
		t.Fatalf("resource = %#v", auditInfo)
	}
	if auditInfo.Before != nil {
		t.Fatalf("新建不该有 before: %#v", auditInfo.Before)
	}
	if auditInfo.After["fingerprint"] != "ba7816bf8f01cfea" || auditInfo.After["version"] != 1 {
		t.Fatalf("after = %#v", auditInfo.After)
	}
	for _, m := range []map[string]any{out, auditInfo.After} {
		for k, v := range m {
			if s, ok := v.(string); ok && strings.Contains(s, testValue) {
				t.Fatalf("%s 泄漏了值", k)
			}
		}
	}
}

func TestRotateHandlerRecordsBeforeAndAfter(t *testing.T) {
	before := metadata(1, "aaaaaaaaaaaaaaaa")
	fake := &fakeStore{result: WriteResult{Before: &before, After: metadata(2, "bbbbbbbbbbbbbbbb")}}
	_, auditInfo, err := action.CaptureAudit(humanContext("production"), secretWriteHandler(fake, true), map[string]any{
		"credential_ref": "secret://sub2api-prod/read-token", "secret_value": testValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastOp != "rotate" || fake.lastEnv != "production" {
		t.Fatalf("store call = %#v", fake)
	}
	if auditInfo.Before["fingerprint"] != "aaaaaaaaaaaaaaaa" || auditInfo.Before["version"] != 1 ||
		auditInfo.After["fingerprint"] != "bbbbbbbbbbbbbbbb" || auditInfo.After["version"] != 2 {
		t.Fatalf("before/after = %#v / %#v", auditInfo.Before, auditInfo.After)
	}
}

func TestSecretHandlersMapErrors(t *testing.T) {
	ctx := humanContext("staging")
	params := func() map[string]any {
		return map[string]any{"credential_ref": "secret://sub2api-prod/read-token", "secret_value": testValue}
	}
	for name, tc := range map[string]struct {
		mutate func(map[string]any)
		err    error
		want   action.Code
	}{
		"引用格式错":   {mutate: func(p map[string]any) { p["credential_ref"] = "sub2api/token" }, want: action.CodeInvalidParams},
		"值为空":     {mutate: func(p map[string]any) { p["secret_value"] = "  " }, want: action.CodeInvalidParams},
		"值含换行":    {mutate: func(p map[string]any) { p["secret_value"] = "a\nb" }, want: action.CodeInvalidParams},
		"值超长":     {mutate: func(p map[string]any) { p["secret_value"] = strings.Repeat("a", MaxSecretValueBytes+1) }, want: action.CodeInvalidParams},
		"未登记":     {err: ErrNotFound, want: action.CodePreconditionFailed},
		"跨环境":     {err: ErrEnvironmentMismatch, want: action.CodeEnvironmentMismatch},
		"仓储失败":    {err: storeError("file", errors.New("disk full")), want: action.CodeExecutionFailed},
		"仓储入参不合法": {err: ErrInvalidInput, want: action.CodeInvalidParams},
	} {
		fake := &fakeStore{err: tc.err}
		p := params()
		if tc.mutate != nil {
			tc.mutate(p)
		}
		_, err := secretWriteHandler(fake, true)(ctx, p)
		if action.ErrorCode(err) != tc.want {
			t.Fatalf("%s: code = %s (%v), want %s", name, action.ErrorCode(err), err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), testValue) {
			t.Fatalf("%s: 错误文本泄漏了值: %v", name, err)
		}
		if tc.mutate != nil && fake.lastOp != "" {
			t.Fatalf("%s: 参数不合法时不该触达仓储", name)
		}
	}
}

func TestHandlersRequirePrincipal(t *testing.T) {
	fake := &fakeStore{}
	for name, h := range map[string]action.Handler{
		"upsert": secretWriteHandler(fake, false), "revoke": revokeHandler(fake),
		"connector": connectorConfigSetHandler(fake),
	} {
		_, err := h(context.Background(), map[string]any{
			"credential_ref": "secret://a/b", "secret_value": testValue, "reason": "x",
			"platform": "sub2api", "mode": "fake",
		})
		if action.ErrorCode(err) != action.CodePermissionDenied {
			t.Fatalf("%s: 无 Principal 应 PERMISSION_DENIED, got %v", name, err)
		}
	}
}

func TestRevokeHandlerRecordsReason(t *testing.T) {
	before := metadata(2, "bbbbbbbbbbbbbbbb")
	after := metadata(2, "bbbbbbbbbbbbbbbb")
	revokedAt := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)
	after.RevokedAt, after.Available = &revokedAt, false
	fake := &fakeStore{result: WriteResult{Before: &before, After: after}}
	value, auditInfo, err := action.CaptureAudit(humanContext("staging"), revokeHandler(fake), map[string]any{
		"credential_ref": "secret://sub2api-prod/read-token", "reason": " 上游泄漏 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastOp != "revoke" || fake.reason != "上游泄漏" || fake.lastActor != "staff_alice" {
		t.Fatalf("store call = %#v", fake)
	}
	if auditInfo.Reason != "上游泄漏" || auditInfo.After["revoked"] != true || auditInfo.After["revoked_at"] != "2026-08-30T02:00:00Z" {
		t.Fatalf("audit = %#v", auditInfo)
	}
	out := value.(map[string]any)
	if out["revoked"] != true || out["version"] != 2 {
		t.Fatalf("result = %#v", out)
	}
	if _, err := revokeHandler(fake)(humanContext("staging"), map[string]any{
		"credential_ref": "secret://sub2api-prod/read-token", "reason": "  ",
	}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("空白 reason 应 INVALID_PARAMS, got %v", err)
	}
}

func TestConnectorConfigSetHandler(t *testing.T) {
	fake := &fakeStore{}
	value, auditInfo, err := action.CaptureAudit(humanContext("staging"), connectorConfigSetHandler(fake), map[string]any{
		"platform": "sub2api", "mode": "real", "endpoint": " https://api.solov.cc ",
		"target_allowlist": "api.solov.cc, XM.solov.cc", "credential_ref": "secret://sub2api-prod/read-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastCfg.Environment != "staging" || fake.lastActor != "staff_alice" ||
		fake.lastCfg.Endpoint != "https://api.solov.cc" ||
		strings.Join(fake.lastCfg.TargetAllowlist, ",") != "api.solov.cc,xm.solov.cc" {
		t.Fatalf("store call = %#v", fake.lastCfg)
	}
	out := value.(map[string]any)
	if out["platform"] != "sub2api" || out["mode"] != "real" || out["version"] != 1 || out["updated_by"] != "staff_alice" {
		t.Fatalf("result = %#v", out)
	}
	if auditInfo.ResourceType != resourceConnectorConfig || auditInfo.ResourceID != "sub2api" || auditInfo.Before != nil ||
		auditInfo.After["mode"] != "real" {
		t.Fatalf("audit = %#v", auditInfo)
	}

	// real 三项缺一不可：在触达仓储之前就拒绝
	fake = &fakeStore{}
	_, err = connectorConfigSetHandler(fake)(humanContext("staging"), map[string]any{
		"platform": "newapi", "mode": "real", "endpoint": "https://xm.solov.cc",
	})
	if action.ErrorCode(err) != action.CodeInvalidParams || fake.lastCfg.Platform != "" {
		t.Fatalf("缺白名单/引用的 real 应 INVALID_PARAMS 且不触达仓储, got %v / %#v", err, fake.lastCfg)
	}
	_, err = connectorConfigSetHandler(fake)(humanContext("staging"), map[string]any{
		"platform": "newapi", "mode": "fake", "target_allowlist": "https://xm.solov.cc",
	})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("白名单里放 URL 应 INVALID_PARAMS, got %v", err)
	}

	// fake 模式三项留空合法
	fake = &fakeStore{}
	if _, err := connectorConfigSetHandler(fake)(humanContext("staging"), map[string]any{
		"platform": "newapi", "mode": "fake",
	}); err != nil {
		t.Fatalf("fake 留空应合法: %v", err)
	}
	if fake.lastCfg.TargetAllowlist == nil {
		t.Fatal("白名单应为空切片而不是 nil，避免 DB 收到 NULL")
	}

	// 仓储失败 → EXECUTION_FAILED
	fake = &fakeStore{cfgErr: storeError("upsert", errors.New("boom"))}
	if _, err := connectorConfigSetHandler(fake)(humanContext("staging"), map[string]any{
		"platform": "newapi", "mode": "fake",
	}); action.ErrorCode(err) != action.CodeExecutionFailed {
		t.Fatalf("仓储失败应 EXECUTION_FAILED, got %v", err)
	}
}
