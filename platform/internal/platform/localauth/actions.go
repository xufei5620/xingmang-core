package localauth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Action 标识（XM-LOGIN，XM-AUTH-TOTP0 追加三个）。全部 L1（修改低风险平台
// 配置，ADR-003）、仅 HUMAN：账号管理是运营/操作员本人的显式动作，机器身份
// 没有理由做这件事。
const (
	ActionAccountCreate        = "staff.account.create"
	ActionAccountSetRoles      = "staff.account.set_roles"
	ActionAccountSetDisabled   = "staff.account.set_disabled"
	ActionAccountResetPassword = "staff.account.reset_password"
	// ActionAccountEnrollTOTP / ConfirmTOTP：自助操作，作用对象**只能是调用者
	// 自己**（Schema 里没有 username 字段）——与既有四个"管理他人账号"的
	// Action 不同，但沿用同一枚 Permission（ScopeManage）：CR-0006 正文裁定
	// TOTP 的强制范围"仅限持有 staff.manage 或后续开票断言所需角色的账号"，
	// 今天唯一持有前者的角色是 admin，用同一枚 scope 天然圈住这个人群，
	// 不需要另开一个"人人都有"的新 scope。
	ActionAccountEnrollTOTP  = "staff.account.enroll_totp"
	ActionAccountConfirmTOTP = "staff.account.confirm_totp"
	// ActionAccountResetTOTP：管理员动作，作用对象是**另一个**账号（Schema
	// 含 username），语义与 ActionAccountResetPassword 对称。
	ActionAccountResetTOTP = "staff.account.reset_totp"

	actionVersion = "1"

	resourceStaffAccount = "staff_account"

	// totpSecretRefScope 是 TOTP 密钥的 CredentialRef scope（CR-0006 正文：
	// "TOTP 密钥经 credential.secret.upsert 写入 secret://staff-totp/<account_id>"）。
	// name 段用账号 UUID（小写十六进制+连字符，天然满足 refPart 的
	// ^[a-z0-9][a-z0-9-]{0,63}$ 校验）。
	totpSecretRefScope = "staff-totp"

	// financeReadScope 与 finance.ScopeRead 同值（本包不 import finance 只为
	// 复用一个字符串常量，理由与 cmd/staff-bootstrap 独立声明字母表常量的
	// 取舍一致：两个仅有的调用方各自持有一份比引入依赖更直接）。CR-0006 正文：
	// TOTP 强制范围"或后续开票断言所需角色"，断言签发（XM-INVCON1）要求的
	// 正是这个 scope。
	financeReadScope = "finance.read"
)

// totpRequiredScopes 是"这个角色需要强制启用 TOTP"的判定集合：翻译出的
// scope 只要交集非空就要求 TOTP。见上面两个常量各自的注释。
var totpRequiredScopes = []string{ScopeManage, financeReadScope}

// rolesRequireTOTP 判断 roles 翻译成的 scope 集合是否落入 totpRequiredScopes
// ——与 parseRoles 用的是**同一张**"角色 -> scope"翻译表（knownRoles），
// 因此"谁需要 TOTP"与"谁能被分配这个角色"共享同一份权威定义，不会出现
// "登录时判定需要 TOTP，但那个角色其实翻译不出这枚 scope"的分歧。
func rolesRequireTOTP(roles []string, knownRoles map[string][]string) bool {
	for _, r := range roles {
		for _, sc := range knownRoles[strings.TrimSpace(r)] {
			if slices.Contains(totpRequiredScopes, sc) {
				return true
			}
		}
	}
	return false
}

var (
	allEnvironments = []string{"development", "staging", "production"}
	humanOnly       = []principal.Type{principal.TypeHuman}
)

// accountWriter 是 Action Handler 依赖的最小面（*Store 满足），便于用假实现
// 测试。三个写方法各自在仓储层完成必要的连带效果（SetDisabled/ResetPassword
// 吊销会话），Handler 不需要再单独调用一次 RevokeAllSessions。
type accountWriter interface {
	CreateAccount(ctx context.Context, username, displayName string, roles []string, passwordHash, actor string, requiresTOTP bool) (Account, error)
	SetRoles(ctx context.Context, username string, roles []string, actor string, requiresTOTP bool) (Account, error)
	SetDisabled(ctx context.Context, username string, disabled bool, actor string) (Account, error)
	ResetPassword(ctx context.Context, username, passwordHash string, mustChange bool, actor string) (Account, error)
}

// totpAccountWriter 是 TOTP 三个 Action Handler 依赖的最小面（*Store 满足）。
// 与 accountWriter 分开声明：既有四个 Action 的 fakeAccountWriter 测试替身
// 不必跟着实现 TOTP 方法。
type totpAccountWriter interface {
	GetByUsername(ctx context.Context, username string) (Account, error)
	SetTOTPSecretRef(ctx context.Context, username, ref, actor string) (Account, error)
	ConfirmTOTP(ctx context.Context, username, secretRef string, recoveryCodeHashes []string, actor string) (Account, error)
	ResetTOTP(ctx context.Context, username, actor string) (Account, error)
}

// totpSecretStore 是 TOTP 密钥读写依赖的最小面（*credentials.Store 满足写
// 半边，任意 secrets.SecretProvider 满足读半边）。CR-0006 正文："TOTP 密钥
// 经 credential.secret.upsert 写入 secret://staff-totp/<account_id>……复用
// 既有 L1 Action，不新增写入口"——enroll/reset 因此不直接碰文件系统，而是
// 调用与 credential.secret.upsert 完全相同的仓储方法。
type totpSecretWriter interface {
	Upsert(ctx context.Context, ref secrets.CredentialRef, value secrets.SecretValue, env, actor string) (credentials.WriteResult, error)
	Revoke(ctx context.Context, ref secrets.CredentialRef, env, actor, reason string) (credentials.WriteResult, error)
}

// totpSecretReader 是校验 TOTP 码所需的读路径最小面（任意
// secrets.SecretProvider 满足，生产装配传与 secretStore 同一份
// XM_SECRET_ROOT 的文件优先 Provider）。写与读故意分成两个接口：写路径
// 与 credential.secret.upsert/revoke 共用同一个 *credentials.Store 实例，
// 读路径复用的是"任意引用皆可解析"的通用 SecretProvider（与
// platformUsersSecretProvider 同一构造，见 cmd/platform-api），两者不是
// 同一个 Go 类型，用一个接口硬凑会强迫其中一侧塞进不需要的方法。
type totpSecretReader interface {
	Resolve(ctx context.Context, ref secrets.CredentialRef, purpose string) (secrets.SecretValue, error)
}

// totpConfirmPurpose / totpLoginPurpose 是读取 TOTP 密钥时记入
// secrets.AccessRecord 的 purpose 文案，供排查区分"这次读取是确认启用还是
// 登录校验"。
const (
	totpConfirmPurpose = "console totp enrollment confirm"
	totpLoginPurpose   = "console totp login verification"
)

// resolveTOTPSecret 从引用读回 TOTP 密钥的原始字节（Base32 解码）。
func resolveTOTPSecret(ctx context.Context, reader totpSecretReader, rawRef, purpose string) ([]byte, error) {
	ref, err := secrets.ParseCredentialRef(rawRef)
	if err != nil {
		return nil, fmt.Errorf("totp secret ref 不合法: %w", err)
	}
	value, err := reader.Resolve(ctx, ref, purpose)
	if err != nil {
		return nil, err
	}
	return DecodeTOTPSecret(value.Reveal())
}

// RegisterActions 登记七个员工账号管理 Action（XM-LOGIN 四个 + XM-AUTH-TOTP0
// 三个）。store 为 nil 时只登记声明（供列表与测试），执行一律失败。
//
// knownRoles 是允许分配的角色名全集——与登录时把角色翻译成 scope 用的是
// **同一张表**的键（oidcauth.DefaultRoleScopeMap 或 XM_OIDC_ROLE_SCOPES 解析
// 出的表），一处配置两处生效，不会出现"能分配一个谁也翻译不出 scope 的
// 角色"这种死角色；rolesRequireTOTP 复用同一张表判定"谁需要强制启用 TOTP"。
//
// secretStore 是 TOTP 密钥的写路径（生产装配传 *credentials.Store，与
// credential.secret.upsert 共用同一个仓储实例——同一份 SecretProvider 目录，
// 不是两份独立状态）；secretReader 是读路径（生产装配传一个能解析任意
// CredentialRef 的 secrets.SecretProvider，与 platformUsersSecretProvider
// 同一构造）；environment 供 secretStore 的 Upsert/Revoke 签名使用（宪法
// 15 条：环境显式，不从参数读）。任一为 nil/空都会让相应 TOTP Action
// 在执行时报 EXECUTION_FAILED，与 store 为 nil 时既有四个 Action 的纪律一致。
func RegisterActions(
	reg *action.Registry, store *Store, knownRoles map[string][]string,
	secretStore totpSecretWriter, secretReader totpSecretReader, environment string,
) error {
	var writer accountWriter
	var totpWriter totpAccountWriter
	if store != nil {
		writer = store
		totpWriter = store
	}
	names := make([]string, 0, len(knownRoles))
	for r := range knownRoles {
		names = append(names, r)
	}
	sort.Strings(names)

	definitions := []struct {
		definition action.Definition
		handler    action.Handler
	}{
		{createDefinition(), createHandler(writer, names, knownRoles)},
		{setRolesDefinition(), setRolesHandler(writer, names, knownRoles)},
		{setDisabledDefinition(), setDisabledHandler(writer)},
		{resetPasswordDefinition(), resetPasswordHandler(writer)},
		{enrollTOTPDefinition(), enrollTOTPHandler(totpWriter, secretStore, environment)},
		{confirmTOTPDefinition(), confirmTOTPHandler(totpWriter, secretReader)},
		{resetTOTPDefinition(), resetTOTPHandler(totpWriter, secretStore, environment)},
	}
	for _, item := range definitions {
		if err := reg.Register(item.definition, item.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", item.definition.ID, err)
		}
	}
	return nil
}

func createDefinition() action.Definition {
	return action.Definition{
		ID: ActionAccountCreate, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "username", Type: action.FieldString, Required: true},
			{Name: "display_name", Type: action.FieldString},
			// 逗号分隔，与 credentials.ConnectorConfig 的 target_allowlist 同一形态
			{Name: "roles", Type: action.FieldString, Required: true},
			// 留空则服务端生成一个随机初始密码，在本次结果里返回一次
			{Name: "initial_password", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func setRolesDefinition() action.Definition {
	return action.Definition{
		ID: ActionAccountSetRoles, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "username", Type: action.FieldString, Required: true},
			{Name: "roles", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func setDisabledDefinition() action.Definition {
	return action.Definition{
		ID: ActionAccountSetDisabled, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "username", Type: action.FieldString, Required: true},
			{Name: "disabled", Type: action.FieldBool, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func resetPasswordDefinition() action.Definition {
	return action.Definition{
		ID: ActionAccountResetPassword, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "username", Type: action.FieldString, Required: true},
			// 留空则服务端生成一个随机新密码，在本次结果里返回一次
			{Name: "new_password", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// enrollTOTPDefinition / confirmTOTPDefinition：无参数——作用对象只能是
// 调用者自己（callerPrincipal 派生），不接受 username 字段，防止一个持有
// staff.manage 的账号顺手把 TOTP 挂到别人名下。
func enrollTOTPDefinition() action.Definition {
	return action.Definition{
		ID: ActionAccountEnrollTOTP, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema:       action.Schema{},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func confirmTOTPDefinition() action.Definition {
	return action.Definition{
		ID: ActionAccountConfirmTOTP, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "code", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func resetTOTPDefinition() action.Definition {
	return action.Definition{
		ID: ActionAccountResetTOTP, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "username", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// callerPrincipal 取出调用者。环境与操作者**只**来自它——参数里没有对应
// 字段，Schema 的白名单语义会把偷渡进来的 environment / actor 直接拒掉
// （与 credentials.callerPrincipal 同一条纪律）。
func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

// parseRoles 解析逗号分隔的角色名，校验每一个都在 known 之内、非空、去重。
func parseRoles(raw string, known []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		r := strings.TrimSpace(part)
		if r == "" {
			continue
		}
		if !slices.Contains(known, r) {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("未知角色 %q，可选：%s", r, strings.Join(known, ", ")), nil)
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, action.NewError(action.CodeInvalidParams, "roles 不能为空", nil)
	}
	sort.Strings(out)
	return out, nil
}

// generatedAlphabet 去掉容易混淆的字符（l/I/1/O/0），照着念给人听
// 或抄到另一个地方时不容易出错。
const (
	generatedPasswordLen = 16
	generatedAlphabet    = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// generatePassword 生成一个随机初始/重置密码。16 个字符、57 个符号的字母表
// 给出远超 MinPasswordLen 策略要求的熵（约 93 bit），modulo 带来的轻微分布
// 偏差对这个用途（管理员一次性发给新同事、下次登录强制改密）可以忽略——
// 它不是长期使用的密钥，只是一次性引导凭据。
func generatePassword() (string, error) {
	buf := make([]byte, generatedPasswordLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机密码失败: %w", err)
	}
	out := make([]byte, generatedPasswordLen)
	for i, v := range buf {
		out[i] = generatedAlphabet[int(v)%len(generatedAlphabet)]
	}
	return string(out), nil
}

// accountSummary 是进审计链与返回值的非机密摘要——**绝不含密码或哈希、
// 不含 TOTP 密钥引用**（引用本身不是机密，但审计摘要只记必要的状态位，
// 不记"这把密钥存在哪个 CredentialRef"这种排查用不到的细节）。
func accountSummary(a Account) map[string]any {
	out := map[string]any{
		"username":             a.Username,
		"display_name":         a.DisplayName,
		"roles":                append([]string(nil), a.Roles...),
		"disabled":             a.Disabled,
		"must_change_password": a.MustChangePassword,
		"totp_enrolled":        a.TOTPActive(),
		"must_enroll_totp":     a.MustEnrollTOTP,
	}
	if a.LockedUntil != nil {
		out["locked_until"] = a.LockedUntil.UTC().Format(time.RFC3339)
	}
	if a.LastLoginAt != nil {
		out["last_login_at"] = a.LastLoginAt.UTC().Format(time.RFC3339)
	}
	if a.TOTPEnrolledAt != nil {
		out["totp_enrolled_at"] = a.TOTPEnrolledAt.UTC().Format(time.RFC3339)
	}
	return out
}

// domainError 把仓储错误映射成稳定的 Action 错误码（与 credentials.domainError
// 同一条纪律：根因只经 Unwrap 供服务端日志，对外文案固定）。
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrAlreadyExists):
		return action.NewError(action.CodeConflict, "用户名已存在", err)
	case errors.Is(err, ErrNotFound):
		return action.NewError(action.CodePreconditionFailed, "账号不存在", err)
	case errors.Is(err, ErrInvalidInput):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	case errors.Is(err, ErrTOTPAlreadyEnrolled):
		return action.NewError(action.CodeConflict, "TOTP 已启用，如需更换请先重置", err)
	case errors.Is(err, ErrTOTPNotEnrolled):
		return action.NewError(action.CodePreconditionFailed, "尚未开始启用 TOTP 或验证码不正确", err)
	default:
		return action.NewError(action.CodeExecutionFailed, "账号仓储写入失败", err)
	}
}

// totpSecretRef 构造账号的 TOTP CredentialRef 字符串（secret://staff-totp/<uuid>）。
func totpSecretRef(accountID string) (secrets.CredentialRef, error) {
	return secrets.ParseCredentialRef("secret://" + totpSecretRefScope + "/" + accountID)
}

// enrollTOTPHandler 处理 staff.account.enroll_totp@1：只作用于调用者自己。
// 生成一把新密钥、经**既有** credential.secret.upsert 写路径落盘（CR-0006：
// 不新增写入口），再把引用挂到账号上（仍是"启用中"，未激活）。返回
// otpauth URI 与 base32 手动录入串——密钥明文只出现在这一次返回值里，
// 审计摘要（RecordAfter）不含它。
func enrollTOTPHandler(store totpAccountWriter, secretStore totpSecretWriter, environment string) action.Handler {
	return func(ctx context.Context, _ map[string]any) (any, error) {
		if store == nil || secretStore == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "TOTP 仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		username := strings.TrimPrefix(p.ID, "staff:")
		acc, err := store.GetByUsername(ctx, username)
		if err != nil {
			return nil, domainError(err)
		}
		if acc.TOTPActive() {
			return nil, domainError(ErrTOTPAlreadyEnrolled)
		}

		secret, err := GenerateTOTPSecret()
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "生成 TOTP 密钥失败", err)
		}
		ref, err := totpSecretRef(acc.ID.String())
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "构造 TOTP 密钥引用失败", err)
		}
		value, err := credentials.NormalizeSecretValue(EncodeTOTPSecret(secret))
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "TOTP 密钥值不合法", err)
		}
		if _, err := secretStore.Upsert(ctx, ref, value, environment, p.ID); err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "TOTP 密钥写入失败", err)
		}
		updated, err := store.SetTOTPSecretRef(ctx, username, ref.String(), p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceStaffAccount, updated.Username)
		action.RecordAfter(ctx, accountSummary(updated))
		return map[string]any{
			"otpauth_uri":   BuildOTPAuthURI(username, secret),
			"secret_base32": EncodeTOTPSecret(secret),
			"issuer":        "xingmang",
			"username":      username,
		}, nil
	}
}

// confirmTOTPHandler 处理 staff.account.confirm_totp@1：只作用于调用者自己。
// 校验一次动态码是否与"启用中"的密钥匹配，通过则激活（ConfirmTOTP）并
// 生成一次性恢复码列表——恢复码明文只出现在这一次返回值里，落库只存哈希，
// 审计摘要不含明文也不含哈希（哈希本身不是机密，但审计摘要不需要它）。
func confirmTOTPHandler(store totpAccountWriter, secretReader totpSecretReader) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil || secretReader == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "TOTP 仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		code := strings.TrimSpace(action.StringParam(params, "code"))
		if code == "" {
			return nil, action.NewError(action.CodeInvalidParams, "code 不能为空", nil)
		}
		username := strings.TrimPrefix(p.ID, "staff:")
		acc, err := store.GetByUsername(ctx, username)
		if err != nil {
			return nil, domainError(err)
		}
		if acc.TOTPActive() {
			return nil, domainError(ErrTOTPAlreadyEnrolled)
		}
		if acc.TOTPSecretRef == "" {
			return nil, domainError(ErrTOTPNotEnrolled)
		}
		secret, err := resolveTOTPSecret(ctx, secretReader, acc.TOTPSecretRef, totpConfirmPurpose)
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "读取 TOTP 密钥失败", err)
		}
		if !VerifyTOTP(secret, code, time.Now().UTC()) {
			return nil, action.NewError(action.CodeInvalidParams, "验证码不正确", nil)
		}
		codes, err := GenerateRecoveryCodes()
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "生成恢复码失败", err)
		}
		hashes := make([]string, len(codes))
		for i, c := range codes {
			hashes[i] = HashRecoveryCode(c)
		}
		updated, err := store.ConfirmTOTP(ctx, username, acc.TOTPSecretRef, hashes, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceStaffAccount, updated.Username)
		action.RecordAfter(ctx, accountSummary(updated))
		return map[string]any{
			"username":         username,
			"totp_enrolled_at": updated.TOTPEnrolledAt.UTC().Format(time.RFC3339),
			"recovery_codes":   codes,
		}, nil
	}
}

// resetTOTPHandler 处理 staff.account.reset_totp@1：管理员动作，作用于
// **另一个**账号（username 参数），与 resetPasswordHandler 对称。同时吊销
// 该账号在 SecretProvider 目录里的密钥文件（credential.secret.revoke 同一
// 写路径）——仓储侧 ResetTOTP 只清 DB 引用列，密钥文件本身要靠这里显式
// Revoke，否则旧密钥仍然躺在磁盘上，只是没有任何 DB 行指向它。
func resetTOTPHandler(store totpAccountWriter, secretStore totpSecretWriter, environment string) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil || secretStore == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "TOTP 仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		username := strings.TrimSpace(action.StringParam(params, "username"))
		if username == "" {
			return nil, action.NewError(action.CodeInvalidParams, "username 不能为空", nil)
		}
		before, err := store.GetByUsername(ctx, username)
		if err != nil {
			return nil, domainError(err)
		}
		updated, err := store.ResetTOTP(ctx, username, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		if before.TOTPSecretRef != "" {
			if ref, rerr := secrets.ParseCredentialRef(before.TOTPSecretRef); rerr == nil {
				// 吊销失败不回滚已经完成的 DB 重置——账号已经不再信任这把密钥
				// （totp_secret_ref 已清空），文件残留是运维可核对、可事后补吊销
				// 的次要缺口，不该让"文件系统一次失败"挡住"账号已被解除"这个
				// 更重要的结果。失败原因经 Unwrap 供服务端日志。
				if _, rerr := secretStore.Revoke(ctx, ref, environment, p.ID, "staff.account.reset_totp"); rerr != nil {
					return nil, action.NewError(action.CodeExecutionFailed, "TOTP 密钥文件吊销失败", rerr)
				}
			}
		}
		action.RecordResource(ctx, resourceStaffAccount, updated.Username)
		action.RecordBefore(ctx, accountSummary(before))
		action.RecordAfter(ctx, accountSummary(updated))
		return accountSummary(updated), nil
	}
}

func newPasswordOrGenerated(raw string) (pw string, generated bool, err error) {
	pw = strings.TrimSpace(raw)
	if pw == "" {
		pw, err = generatePassword()
		return pw, true, err
	}
	return pw, false, nil
}

func createHandler(store accountWriter, known []string, knownRoles map[string][]string) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "账号仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		username := strings.TrimSpace(action.StringParam(params, "username"))
		if username == "" {
			return nil, action.NewError(action.CodeInvalidParams, "username 不能为空", nil)
		}
		displayName := strings.TrimSpace(action.StringParam(params, "display_name"))
		roles, err := parseRoles(action.StringParam(params, "roles"), known)
		if err != nil {
			return nil, err
		}
		pw, generated, err := newPasswordOrGenerated(action.StringParam(params, "initial_password"))
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "生成初始密码失败", err)
		}
		if err := ValidatePasswordPolicy(pw); err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		hash, err := HashPassword(pw)
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "口令哈希失败", err)
		}
		acc, err := store.CreateAccount(ctx, username, displayName, roles, hash, p.ID, rolesRequireTOTP(roles, knownRoles))
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceStaffAccount, acc.Username)
		action.RecordAfter(ctx, accountSummary(acc))

		result := accountSummary(acc)
		if generated {
			// 只在这一次的 HTTP 响应里出现；上面 RecordAfter 已经用不含它的
			// summary 写过审计链，这里追加不会让密码漏进审计事件。
			result["initial_password"] = pw
		}
		return result, nil
	}
}

func setRolesHandler(store accountWriter, known []string, knownRoles map[string][]string) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "账号仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		username := strings.TrimSpace(action.StringParam(params, "username"))
		if username == "" {
			return nil, action.NewError(action.CodeInvalidParams, "username 不能为空", nil)
		}
		roles, err := parseRoles(action.StringParam(params, "roles"), known)
		if err != nil {
			return nil, err
		}
		acc, err := store.SetRoles(ctx, username, roles, p.ID, rolesRequireTOTP(roles, knownRoles))
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceStaffAccount, acc.Username)
		action.RecordAfter(ctx, accountSummary(acc))
		return accountSummary(acc), nil
	}
}

func setDisabledHandler(store accountWriter) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "账号仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		username := strings.TrimSpace(action.StringParam(params, "username"))
		if username == "" {
			return nil, action.NewError(action.CodeInvalidParams, "username 不能为空", nil)
		}
		disabled := action.BoolParam(params, "disabled")
		acc, err := store.SetDisabled(ctx, username, disabled, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceStaffAccount, acc.Username)
		action.RecordAfter(ctx, accountSummary(acc))
		return accountSummary(acc), nil
	}
}

func resetPasswordHandler(store accountWriter) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "账号仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		username := strings.TrimSpace(action.StringParam(params, "username"))
		if username == "" {
			return nil, action.NewError(action.CodeInvalidParams, "username 不能为空", nil)
		}
		pw, generated, err := newPasswordOrGenerated(action.StringParam(params, "new_password"))
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "生成密码失败", err)
		}
		if err := ValidatePasswordPolicy(pw); err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		hash, err := HashPassword(pw)
		if err != nil {
			return nil, action.NewError(action.CodeExecutionFailed, "口令哈希失败", err)
		}
		// 管理员重置：总是要求下次登录改密（与自助改密的 must_change_password=false 相对）
		acc, err := store.ResetPassword(ctx, username, hash, true, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceStaffAccount, acc.Username)
		action.RecordAfter(ctx, accountSummary(acc))

		result := accountSummary(acc)
		if generated {
			result["new_password"] = pw
		}
		return result, nil
	}
}
