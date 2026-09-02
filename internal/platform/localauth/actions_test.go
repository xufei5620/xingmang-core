package localauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

type fakeAccountWriter struct {
	createErr        error
	lastUsername     string
	lastRoles        []string
	lastActor        string
	lastDisabled     bool
	lastRequiresTOTP bool
}

func (f *fakeAccountWriter) CreateAccount(_ context.Context, username, displayName string, roles []string, _, actor string, requiresTOTP bool) (Account, error) {
	f.lastUsername, f.lastRoles, f.lastActor, f.lastRequiresTOTP = username, roles, actor, requiresTOTP
	if f.createErr != nil {
		return Account{}, f.createErr
	}
	return Account{Username: username, DisplayName: displayName, Roles: roles, MustChangePassword: true, MustEnrollTOTP: requiresTOTP}, nil
}

func (f *fakeAccountWriter) SetRoles(_ context.Context, username string, roles []string, actor string, requiresTOTP bool) (Account, error) {
	f.lastUsername, f.lastRoles, f.lastActor, f.lastRequiresTOTP = username, roles, actor, requiresTOTP
	return Account{Username: username, Roles: roles, MustEnrollTOTP: requiresTOTP}, nil
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
	if err := RegisterActions(reg, nil, testKnownRoles, nil, nil, "staging"); err != nil {
		t.Fatal(err)
	}
	for id, permission := range map[string]string{
		ActionAccountCreate:        ScopeManage,
		ActionAccountSetRoles:      ScopeManage,
		ActionAccountSetDisabled:   ScopeManage,
		ActionAccountResetPassword: ScopeManage,
		ActionAccountEnrollTOTP:    ScopeManage,
		ActionAccountConfirmTOTP:   ScopeManage,
		ActionAccountResetTOTP:     ScopeManage,
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
	h := createHandler(writer, []string{"staff", "admin"}, testKnownRoles)
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
	// admin 在 testKnownRoles 里翻译出 staff.manage，创建时应据此把
	// must_enroll_totp 计算成真——见 rolesRequireTOTP。
	if !writer.lastRequiresTOTP {
		t.Fatal("持有 admin 角色的新账号应被判定为需要 TOTP")
	}
}

func TestCreateHandlerDoesNotEchoProvidedPassword(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := createHandler(writer, []string{"staff"}, testKnownRoles)
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
	if writer.lastRequiresTOTP {
		t.Fatal("只持有 staff 角色不应被判定为需要 TOTP")
	}
}

func TestCreateHandlerRejectsShortProvidedPassword(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := createHandler(writer, []string{"staff"}, testKnownRoles)
	if _, err := h(humanCtx(), map[string]any{
		"username": "bob", "roles": "staff", "initial_password": "short",
	}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("过短密码应 INVALID_PARAMS, got %v", err)
	}
}

func TestSetRolesHandlerValidatesRoleNames(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := setRolesHandler(writer, []string{"staff", "admin"}, testKnownRoles)
	if _, err := h(humanCtx(), map[string]any{
		"username": "bob", "roles": "staff,not-a-real-role",
	}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("未知角色应 INVALID_PARAMS, got %v", err)
	}
	if _, err := h(humanCtx(), map[string]any{"username": "bob", "roles": "staff,admin"}); err != nil {
		t.Fatalf("已知角色应通过: %v", err)
	}
	if !writer.lastRequiresTOTP {
		t.Fatal("改成含 admin 的角色集合应被判定为需要 TOTP")
	}
}

func TestSetRolesHandlerRejectsEmptyRoles(t *testing.T) {
	writer := &fakeAccountWriter{}
	h := setRolesHandler(writer, []string{"staff"}, testKnownRoles)
	if _, err := h(humanCtx(), map[string]any{
		"username": "bob", "roles": " , ,",
	}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("空角色列表应 INVALID_PARAMS, got %v", err)
	}
}

func TestRolesRequireTOTP(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{[]string{"staff"}, false},
		{[]string{"admin"}, true},
		{[]string{"staff", "admin"}, true},
		{nil, false},
		{[]string{"unknown-role"}, false},
	}
	for _, tc := range cases {
		if got := rolesRequireTOTP(tc.roles, testKnownRoles); got != tc.want {
			t.Fatalf("rolesRequireTOTP(%v) = %v, want %v", tc.roles, got, tc.want)
		}
	}
	// finance.read 单独出现（不含 staff.manage）也应命中——CR-0006 正文：
	// "持有 staff.manage 或后续开票断言所需角色的账号"。
	financeOnly := map[string][]string{"finance-only": {"finance.read"}}
	if !rolesRequireTOTP([]string{"finance-only"}, financeOnly) {
		t.Fatal("单独持有 finance.read 的角色也应判定需要 TOTP")
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

// --- TOTP 三个 Action Handler 的测试替身 ---

type fakeTOTPAccountWriter struct {
	acc     Account
	getErr  error
	setErr  error
	confErr error
	rstErr  error

	lastSetSecretRef  string
	lastConfirmRef    string
	lastConfirmHashes []string
	lastResetUsername string
}

func (f *fakeTOTPAccountWriter) GetByUsername(_ context.Context, _ string) (Account, error) {
	if f.getErr != nil {
		return Account{}, f.getErr
	}
	return f.acc, nil
}

func (f *fakeTOTPAccountWriter) SetTOTPSecretRef(_ context.Context, _, ref, _ string) (Account, error) {
	f.lastSetSecretRef = ref
	if f.setErr != nil {
		return Account{}, f.setErr
	}
	updated := f.acc
	updated.TOTPSecretRef = ref
	return updated, nil
}

func (f *fakeTOTPAccountWriter) ConfirmTOTP(_ context.Context, _, secretRef string, hashes []string, _ string) (Account, error) {
	f.lastConfirmRef = secretRef
	f.lastConfirmHashes = hashes
	if f.confErr != nil {
		return Account{}, f.confErr
	}
	now := time.Now().UTC()
	updated := f.acc
	updated.TOTPEnrolledAt = &now
	updated.MustEnrollTOTP = false
	return updated, nil
}

func (f *fakeTOTPAccountWriter) ResetTOTP(_ context.Context, username, _ string) (Account, error) {
	f.lastResetUsername = username
	if f.rstErr != nil {
		return Account{}, f.rstErr
	}
	updated := f.acc
	updated.TOTPSecretRef = ""
	updated.TOTPEnrolledAt = nil
	updated.MustEnrollTOTP = true
	return updated, nil
}

type fakeTOTPSecretWriter struct {
	upsertErr     error
	revokeErr     error
	lastUpsertRef secrets.CredentialRef
	lastRevokeRef secrets.CredentialRef
}

func (f *fakeTOTPSecretWriter) Upsert(_ context.Context, ref secrets.CredentialRef, _ secrets.SecretValue, _, _ string) (credentials.WriteResult, error) {
	f.lastUpsertRef = ref
	if f.upsertErr != nil {
		return credentials.WriteResult{}, f.upsertErr
	}
	return credentials.WriteResult{After: credentials.Metadata{Ref: ref.String()}}, nil
}

func (f *fakeTOTPSecretWriter) Revoke(_ context.Context, ref secrets.CredentialRef, _, _, _ string) (credentials.WriteResult, error) {
	f.lastRevokeRef = ref
	if f.revokeErr != nil {
		return credentials.WriteResult{}, f.revokeErr
	}
	return credentials.WriteResult{After: credentials.Metadata{Ref: ref.String()}}, nil
}

type fakeTOTPSecretReader struct {
	values map[string]secrets.SecretValue
	err    error
}

func (f *fakeTOTPSecretReader) Resolve(_ context.Context, ref secrets.CredentialRef, _ string) (secrets.SecretValue, error) {
	if f.err != nil {
		return secrets.SecretValue{}, f.err
	}
	v, ok := f.values[ref.String()]
	if !ok {
		return secrets.SecretValue{}, errors.New("not found")
	}
	return v, nil
}

func TestEnrollTOTPDefinitionHasNoParams(t *testing.T) {
	if len(enrollTOTPDefinition().Schema.Fields) != 0 {
		t.Fatal("enroll_totp 不应接受任何参数——作用对象只能是调用者自己")
	}
}

func TestConfirmTOTPDefinitionRequiresCode(t *testing.T) {
	fields := confirmTOTPDefinition().Schema.Fields
	if len(fields) != 1 || fields[0].Name != "code" || !fields[0].Required {
		t.Fatalf("confirm_totp schema = %#v", fields)
	}
}

func TestResetTOTPDefinitionRequiresUsername(t *testing.T) {
	fields := resetTOTPDefinition().Schema.Fields
	if len(fields) != 1 || fields[0].Name != "username" || !fields[0].Required {
		t.Fatalf("reset_totp schema = %#v", fields)
	}
}

func TestEnrollTOTPHandlerGeneratesSecretAndStoresRef(t *testing.T) {
	accID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	writer := &fakeTOTPAccountWriter{acc: Account{ID: accID, Username: "admin1"}}
	secretStore := &fakeTOTPSecretWriter{}

	h := enrollTOTPHandler(writer, secretStore, "staging")
	value, contrib, err := action.CaptureAudit(humanCtx(), h, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", value)
	}
	uri, _ := out["otpauth_uri"].(string)
	secretB32, _ := out["secret_base32"].(string)
	if uri == "" || secretB32 == "" || out["username"] != "admin1" {
		t.Fatalf("result = %#v", out)
	}
	if secretStore.lastUpsertRef.Scope() != totpSecretRefScope {
		t.Fatalf("secret ref scope = %q, want %q", secretStore.lastUpsertRef.Scope(), totpSecretRefScope)
	}
	if secretStore.lastUpsertRef.Name() != accID.String() {
		t.Fatalf("secret ref name = %q, want account id", secretStore.lastUpsertRef.Name())
	}
	if writer.lastSetSecretRef != secretStore.lastUpsertRef.String() {
		t.Fatalf("SetTOTPSecretRef 用的引用应与写入密钥时的引用一致: %q vs %q",
			writer.lastSetSecretRef, secretStore.lastUpsertRef.String())
	}
	// 审计摘要不含密钥/URI
	if _, leaked := contrib.After["otpauth_uri"]; leaked {
		t.Fatal("审计 After 摘要不该包含 otpauth_uri")
	}
	if _, leaked := contrib.After["secret_base32"]; leaked {
		t.Fatal("审计 After 摘要不该包含 secret_base32")
	}
}

func TestEnrollTOTPHandlerRejectsWhenAlreadyActive(t *testing.T) {
	now := time.Now().UTC()
	writer := &fakeTOTPAccountWriter{acc: Account{
		Username: "admin1", TOTPSecretRef: "secret://staff-totp/x", TOTPEnrolledAt: &now,
	}}
	h := enrollTOTPHandler(writer, &fakeTOTPSecretWriter{}, "staging")
	if _, err := h(humanCtx(), map[string]any{}); action.ErrorCode(err) != action.CodeConflict {
		t.Fatalf("已激活时重新 enroll 应 CONFLICT, got %v", err)
	}
}

func TestConfirmTOTPHandlerActivatesOnCorrectCode(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	ref := "secret://staff-totp/22222222-2222-2222-2222-222222222222"
	writer := &fakeTOTPAccountWriter{acc: Account{Username: "admin1", TOTPSecretRef: ref}}
	reader := &fakeTOTPSecretReader{values: map[string]secrets.SecretValue{
		ref: secrets.NewSecretValue([]byte(EncodeTOTPSecret(secret))),
	}}
	code := GenerateTOTP(secret, time.Now().UTC())

	h := confirmTOTPHandler(writer, reader)
	value, _, err := action.CaptureAudit(humanCtx(), h, map[string]any{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	codes, ok := out["recovery_codes"].([]string)
	if !ok || len(codes) != RecoveryCodeCount {
		t.Fatalf("recovery_codes = %#v", out["recovery_codes"])
	}
	if len(writer.lastConfirmHashes) != RecoveryCodeCount {
		t.Fatalf("落库的恢复码哈希数 = %d, want %d", len(writer.lastConfirmHashes), RecoveryCodeCount)
	}
	for i, c := range codes {
		if writer.lastConfirmHashes[i] != HashRecoveryCode(c) {
			t.Fatalf("第 %d 个恢复码的哈希与落库值不一致", i)
		}
	}
	if writer.lastConfirmRef != ref {
		t.Fatalf("ConfirmTOTP 应带上当前引用做 TOCTOU 校验, got %q", writer.lastConfirmRef)
	}
}

func TestConfirmTOTPHandlerRejectsWrongCode(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	ref := "secret://staff-totp/22222222-2222-2222-2222-222222222222"
	writer := &fakeTOTPAccountWriter{acc: Account{Username: "admin1", TOTPSecretRef: ref}}
	reader := &fakeTOTPSecretReader{values: map[string]secrets.SecretValue{
		ref: secrets.NewSecretValue([]byte(EncodeTOTPSecret(secret))),
	}}
	h := confirmTOTPHandler(writer, reader)
	if _, err := h(humanCtx(), map[string]any{"code": "000000"}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("错误验证码应 INVALID_PARAMS, got %v", err)
	}
}

func TestConfirmTOTPHandlerRejectsWhenNotEnrolled(t *testing.T) {
	writer := &fakeTOTPAccountWriter{acc: Account{Username: "admin1"}}
	h := confirmTOTPHandler(writer, &fakeTOTPSecretReader{})
	if _, err := h(humanCtx(), map[string]any{"code": "123456"}); action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("尚未 enroll 时 confirm 应 PRECONDITION_FAILED, got %v", err)
	}
}

func TestConfirmTOTPHandlerRequiresCode(t *testing.T) {
	writer := &fakeTOTPAccountWriter{acc: Account{Username: "admin1", TOTPSecretRef: "secret://staff-totp/x"}}
	h := confirmTOTPHandler(writer, &fakeTOTPSecretReader{})
	if _, err := h(humanCtx(), map[string]any{"code": ""}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("空 code 应 INVALID_PARAMS, got %v", err)
	}
}

func TestResetTOTPHandlerRevokesSecretAndClearsState(t *testing.T) {
	ref := "secret://staff-totp/33333333-3333-3333-3333-333333333333"
	now := time.Now().UTC()
	writer := &fakeTOTPAccountWriter{acc: Account{
		Username: "bob", TOTPSecretRef: ref, TOTPEnrolledAt: &now,
	}}
	secretStore := &fakeTOTPSecretWriter{}
	h := resetTOTPHandler(writer, secretStore, "staging")

	value, contrib, err := action.CaptureAudit(humanCtx(), h, map[string]any{"username": "bob"})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	if out["totp_enrolled"] != false || out["must_enroll_totp"] != true {
		t.Fatalf("重置后结果 = %#v", out)
	}
	if writer.lastResetUsername != "bob" {
		t.Fatalf("ResetTOTP 用户名 = %q", writer.lastResetUsername)
	}
	if secretStore.lastRevokeRef.String() != ref {
		t.Fatalf("应吊销原密钥引用, got %q want %q", secretStore.lastRevokeRef.String(), ref)
	}
	beforeMap, _ := contrib.Before["totp_enrolled"].(bool)
	if !beforeMap {
		t.Fatal("审计 Before 摘要应反映重置前 totp_enrolled=true")
	}
}

func TestResetTOTPHandlerSkipsRevokeWhenNeverEnrolled(t *testing.T) {
	writer := &fakeTOTPAccountWriter{acc: Account{Username: "bob"}}
	secretStore := &fakeTOTPSecretWriter{}
	h := resetTOTPHandler(writer, secretStore, "staging")
	if _, err := h(humanCtx(), map[string]any{"username": "bob"}); err != nil {
		t.Fatal(err)
	}
	if !secretStore.lastRevokeRef.IsZero() {
		t.Fatal("从未 enroll 过时不该尝试吊销任何密钥引用")
	}
}

func TestResetTOTPHandlerRequiresUsername(t *testing.T) {
	h := resetTOTPHandler(&fakeTOTPAccountWriter{}, &fakeTOTPSecretWriter{}, "staging")
	if _, err := h(humanCtx(), map[string]any{"username": ""}); action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("空 username 应 INVALID_PARAMS, got %v", err)
	}
}

func TestDomainErrorMapsKnownErrors(t *testing.T) {
	cases := map[error]action.Code{
		ErrAlreadyExists:       action.CodeConflict,
		ErrNotFound:            action.CodePreconditionFailed,
		ErrInvalidInput:        action.CodeInvalidParams,
		ErrTOTPAlreadyEnrolled: action.CodeConflict,
		ErrTOTPNotEnrolled:     action.CodePreconditionFailed,
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
