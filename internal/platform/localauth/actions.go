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
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Action 标识（XM-LOGIN）。全部 L1（修改低风险平台配置，ADR-003）、仅
// HUMAN：账号管理是运营的显式动作，机器身份没有理由做这件事。
const (
	ActionAccountCreate        = "staff.account.create"
	ActionAccountSetRoles      = "staff.account.set_roles"
	ActionAccountSetDisabled   = "staff.account.set_disabled"
	ActionAccountResetPassword = "staff.account.reset_password"

	actionVersion = "1"

	resourceStaffAccount = "staff_account"
)

var (
	allEnvironments = []string{"development", "staging", "production"}
	humanOnly       = []principal.Type{principal.TypeHuman}
)

// accountWriter 是 Action Handler 依赖的最小面（*Store 满足），便于用假实现
// 测试。三个写方法各自在仓储层完成必要的连带效果（SetDisabled/ResetPassword
// 吊销会话），Handler 不需要再单独调用一次 RevokeAllSessions。
type accountWriter interface {
	CreateAccount(ctx context.Context, username, displayName string, roles []string, passwordHash, actor string) (Account, error)
	SetRoles(ctx context.Context, username string, roles []string, actor string) (Account, error)
	SetDisabled(ctx context.Context, username string, disabled bool, actor string) (Account, error)
	ResetPassword(ctx context.Context, username, passwordHash string, mustChange bool, actor string) (Account, error)
}

// RegisterActions 登记四个员工账号管理 Action。store 为 nil 时只登记声明
// （供列表与测试），执行一律失败。
//
// knownRoles 是允许分配的角色名全集——与登录时把角色翻译成 scope 用的是
// **同一张表**的键（oidcauth.DefaultRoleScopeMap 或 XM_OIDC_ROLE_SCOPES 解析
// 出的表），一处配置两处生效，不会出现"能分配一个谁也翻译不出 scope 的
// 角色"这种死角色。
func RegisterActions(reg *action.Registry, store *Store, knownRoles map[string][]string) error {
	var writer accountWriter
	if store != nil {
		writer = store
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
		{createDefinition(), createHandler(writer, names)},
		{setRolesDefinition(), setRolesHandler(writer, names)},
		{setDisabledDefinition(), setDisabledHandler(writer)},
		{resetPasswordDefinition(), resetPasswordHandler(writer)},
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
	generatedPasswordLen      = 16
	generatedAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
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

// accountSummary 是进审计链与返回值的非机密摘要——**绝不含密码或哈希**。
func accountSummary(a Account) map[string]any {
	out := map[string]any{
		"username":             a.Username,
		"display_name":         a.DisplayName,
		"roles":                append([]string(nil), a.Roles...),
		"disabled":             a.Disabled,
		"must_change_password": a.MustChangePassword,
	}
	if a.LockedUntil != nil {
		out["locked_until"] = a.LockedUntil.UTC().Format(time.RFC3339)
	}
	if a.LastLoginAt != nil {
		out["last_login_at"] = a.LastLoginAt.UTC().Format(time.RFC3339)
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
	default:
		return action.NewError(action.CodeExecutionFailed, "账号仓储写入失败", err)
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

func createHandler(store accountWriter, known []string) action.Handler {
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
		acc, err := store.CreateAccount(ctx, username, displayName, roles, hash, p.ID)
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

func setRolesHandler(store accountWriter, known []string) action.Handler {
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
		acc, err := store.SetRoles(ctx, username, roles, p.ID)
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
