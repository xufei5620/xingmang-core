package localauth

import (
	"context"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeAccountWriter struct {
	createErr    error
	lastUsername string
	lastRoles    []string
	lastActor    string
	lastDisabled bool
}

func (f *fakeAccountWriter) CreateAccount(_ context.Context, username, displayName string, roles []string, _, actor string) (Account, error) {
	f.lastUsername, f.lastRoles, f.lastActor = username, roles, actor
	if f.createErr != nil {
		return Account{}, f.createErr
	}
	return Account{Username: username, DisplayName: displayName, Roles: roles, MustChangePassword: true}, nil
}

func (f *fakeAccountWriter) SetRoles(_ context.Context, username string, roles []string, actor string) (Account, error) {
	f.lastUsername, f.lastRoles, f.lastActor = username, roles, actor
	return Account{Username: username, Roles: roles}, nil
}

func (f *fakeAccountWriter) SetDisabled(_ context.Context, username string, disabled bool, actor string) (Account, error) {
	f.lastUsername, f.lastDisabled, f.lastActor = username, disabled, actor
	return Account{Username: username, Disabled: disabled}, nil
}

func (f *fakeAccountWriter) ResetPassword(_ context.Context, username, _ string, mustChange bool, actor string) (Account, error) {
	f.lastUsername, f.lastActor = username, actor
	return Account{Username: username, MustChangePassword: mustChange}, nil
}

func humanCtx() context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff:admin1", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: issuerLocalAuth, Environment: "staging", Scopes: []string{ScopeManage},
	})
}

var testKnownRoles = map[string][]string{"staff": {"registry.read"}, "admin": {"registry.read", "staff.manage"}}

func TestActionDefinitionsRegistered(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil, testKnownRoles); err != nil {
		t.Fatal(err)
	}
	for id, permission := range map[string]string{
		ActionAccountCreate:        ScopeManage,
		ActionAccountSetRoles:      ScopeManage,
		ActionAccountSetDisabled:   ScopeManage,
		ActionAccountResetPassword: ScopeManage,
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
		if len(def.Environments) != 3 {
			t.Fatalf("%s environments = %v", id, def.Environments)
		}
	}
	// 未绑定仓储时执行一律失败，而不是静默成功
	_, h, _ := reg.Lookup(ActionAccountCreate, "1")
	if _, err := h(humanCtx(), map[string]any{"username": "bob", "roles": "staff"}); action.ErrorCode(err) != action.CodeExecutionFailed {
		t.Fatalf("nil store 应 EXECUTION_FAILED, got %v", err)
	}
}

func TestSchemaRejectsEnvironmentAndActorSmuggling(t *testing.T) {
	for _, def := range []action.Definition{
		createDefinition(), setRolesDefinition(), setDisabledDefinition(), resetPasswordDefinition(),
	} {
		for _, field := range []string{"environment", "actor", "updated_by", "principal_id"} {
			params := map[string]any{"username": "bob", "roles": "staff", "disabled": false}
			params[field] = "attacker"
			if err := def.Schema.Validate(params); !errors.Is(err, action.ErrUnknownField) {
				t.Fatalf("%s: field %s error = %v", def.ID, field, err)
			}
		}
	}
}

func TestCreateHandlerGeneratesPasswordOnceAndKeepsItOutOfAudit(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := createHandler(writer, []string{"staff", "admin"})
	value, contrib, err := action.CaptureAudit(humanCtx(), h, map[string]any{
		"username": "bob", "roles": " staff, admin ,staff",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", value)
	}
	pw, ok := out["initial_password"].(string)
	if !ok || len(pw) < MinPasswordLen {
		t.Fatalf("initial_password = %#v", out["initial_password"])
	}
	if _, leaked := contrib.After["initial_password"]; leaked {
		t.Fatal("审计 After 摘要不该包含 initial_password")
	}
	if writer.lastActor != "staff:admin1" {
		t.Fatalf("actor 应来自 caller principal, got %q", writer.lastActor)
	}
	if len(writer.lastRoles) != 2 || writer.lastRoles[0] != "admin" || writer.lastRoles[1] != "staff" {
		t.Fatalf("roles 应去重排序, got %v", writer.lastRoles)
	}
}

func TestCreateHandlerDoesNotEchoProvidedPassword(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := createHandler(writer, []string{"staff"})
	value, _, err := action.CaptureAudit(humanCtx(), h, map[string]any{
		"username": "bob", "roles": "staff", "initial_password": "a-fixed-password-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	if _, present := out["initial_password"]; present {
		t.Fatal("显式提供密码时不该在结果里回显它")
	}
}

func TestCreateHandlerRejectsShortProvidedPassword(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := createHandler(writer, []string{"staff"})
	if _, err := h(humanCtx(), map[string]any{
		"username": "bob", "roles": "staff", "initial_password": "short",
	}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("过短密码应 INVALID_PARAMS, got %v", err)
	}
}

func TestSetRolesHandlerValidatesRoleNames(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := setRolesHandler(writer, []string{"staff", "admin"})
	if _, err := h(humanCtx(), map[string]any{
		"username": "bob", "roles": "staff,not-a-real-role",
	}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("未知角色应 INVALID_PARAMS, got %v", err)
	}
	if _, err := h(humanCtx(), map[string]any{"username": "bob", "roles": "staff,admin"}); err != nil {
		t.Fatalf("已知角色应通过: %v", err)
	}
}

func TestSetRolesHandlerRejectsEmptyRoles(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := setRolesHandler(writer, []string{"staff"})
	if _, err := h(humanCtx(), map[string]any{
		"username": "bob", "roles": " , ,",
	}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("空角色列表应 INVALID_PARAMS, got %v", err)
	}
}

func TestSetDisabledHandler(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := setDisabledHandler(writer)
	value, err := h(humanCtx(), map[string]any{"username": "bob", "disabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if !writer.lastDisabled || writer.lastUsername != "bob" {
		t.Fatalf("writer state = %+v", writer)
	}
	out := value.(map[string]any)
	if out["disabled"] != true {
		t.Fatalf("result = %#v", out)
	}
}

func TestResetPasswordHandlerGeneratesOnceAndValidatesProvided(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := resetPasswordHandler(writer)

	value, err := h(humanCtx(), map[string]any{"username": "bob"})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	if _, ok := out["new_password"].(string); !ok {
		t.Fatalf("留空应生成并返回一次, got %#v", out)
	}

	if _, err := h(humanCtx(), map[string]any{"username": "bob", "new_password": "short"}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("过短的显式密码应 INVALID_PARAMS, got %v", err)
	}
}

func TestGeneratePasswordMeetsPolicy(t *testing.T) {
	pw, err := generatePassword()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePasswordPolicy(pw); err != nil {
		t.Fatalf("生成的密码应满足策略: %v", err)
	}
}

func TestDomainErrorMapsKnownErrors(t *testing.T) {
	cases := map[error]action.Code{
		ErrAlreadyExists: action.CodeConflict,
		ErrNotFound:      action.CodePreconditionFailed,
		ErrInvalidInput:  action.CodeInvalidParams,
	}
	for src, want := range cases {
		if got := action.ErrorCode(domainError(src)); got != want {
			t.Fatalf("domainError(%v) code = %q, want %q", src, got, want)
		}
	}
	if action.ErrorCode(domainError(errors.New("boom"))) != action.CodeExecutionFailed {
		t.Fatal("未知错误应归为 EXECUTION_FAILED")
	}
}
