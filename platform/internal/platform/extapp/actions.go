package extapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 三个 Action 的稳定 ID。
const (
	// ActionAppSet 登记或修改一个前端站点（整行替换）。
	ActionAppSet = "extapp.app.set"
	// ActionAppRetire 把一个站点标记为已下线。
	ActionAppRetire = "extapp.app.retire"
	// ActionReleaseRecord 记录一次**已经发生**的发布。
	ActionReleaseRecord = "extapp.release.record"

	actionVersion = "1"
)

// 审计里的资源类型，与库表同名，便于从审计事件直接定位到行。
const (
	resourceApp     = "core.ext_app"
	resourceRelease = "core.ext_app_release"
)

// allEnvironments 是这批 Action 允许执行的环境；显式列举而不是「除了生产都行」
// ——生产权限不从测试继承（宪法 15 条）。
var allEnvironments = []string{"development", "staging", "production"}

// humanOnly：登记簿写操作只允许人类身份。
//
// 这一条对 extapp.release.record 尤其要紧，而且**是一次刻意的取舍**：
// 「发布完自动登记一条」听上去正该由 CI 或部署脚本的机器身份来做。但今天
// 平台里没有任何一条无人值守链路会调它（GitHub Actions 自 2026-08-29 停摆，
// 部署走 deploy/scripts/deploy-local.sh 由人在目标机上执行），所以开放
// SERVICE 身份换不来任何自动化，只是先把一个写入面敞开。要接自动登记时，
// 该做的是给它单独一个 SERVICE 可用的 Action 定义并连同凭据一起审一次，
// 而不是现在顺手把 PrincipalTypes 放宽。
var humanOnly = []principal.Type{principal.TypeHuman}

var (
	appStatusEnum   = []string{string(AppPlanned), string(AppActive), string(AppRetired)}
	authModeEnum    = []string{string(AuthDevHeader), string(AuthOIDC), string(AuthLocal)}
	releaseKindEnum = []string{string(ReleaseDeploy), string(ReleaseRollback)}
)

// RegisterActions 把前端应用登记簿的写操作注册为 Action（ADR-003：写操作唯一入口）。
//
// store 允许为 nil：此时只登记声明而不绑定实际执行体，供「注册表完整性」类
// 测试与文档生成使用；Handler 被调用时会返回明确错误（同 registry 包）。
func RegisterActions(reg *action.Registry, store *Store) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{appSetDef(), appSetHandler(store)},
		{appRetireDef(), appRetireHandler(store)},
		{releaseRecordDef(), releaseRecordHandler(store)},
	}
	for _, d := range defs {
		if err := reg.Register(d.def, d.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", d.def.ID, err)
		}
	}
	return nil
}

func requireStore(store *Store) error {
	if store == nil {
		return fmt.Errorf("extapp store 未绑定：本注册表实例仅用于声明登记")
	}
	return nil
}

func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

// requireSameEnvironment 是跨环境闸门（宪法 15 条）。
//
// 内核只校验「这个 Action 允许在你的环境执行」，它不认识资源——一个 staging
// 身份完全可能拿着生产应用的 UUID 打过来，这道判定必须在**读到资源之后**。
func requireSameEnvironment(p principal.Principal, resourceEnv string) error {
	if resourceEnv != p.Environment {
		return action.NewError(action.CodePermissionDenied,
			"不允许跨环境操作前端应用登记簿：调用者身份属于 "+p.Environment, nil)
	}
	return nil
}

// domainError 把领域错误映射成 Action 错误码。不做这层映射的话，
// 「app_key 格式不对」会以 INTERNAL 返回，调用方看到的是「服务器内部错误」
// 而不是哪个参数填错了。
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrMissingField),
		errors.Is(err, ErrInvalidFormat):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	default:
		return err
	}
}

func trimmed(params map[string]any, name string) string {
	return strings.TrimSpace(action.StringParam(params, name))
}

func defaultIfBlank(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// --- extapp.app.set（L1）--------------------------------------------------

// appSetDef 声明登记 / 修改一个前端站点的动作。
//
// **L1，依据 ADR-003 的风险等级表第二行「修改低风险平台配置」**，与仓库里
// 同一类登记簿写操作等档：`server.asset.set@1`、`server.domain.set@1`、
// `finance.upstream_account.set@1` 全是 L1。
//
// 为什么不是 L2（「批量配置、启停低风险资源」）：本动作一次只写一行，
// 不批量、不启停任何东西，**不触碰任何第三方系统、不改变任何运行中的服务**。
// 改错了改回来即可，改错期间线上站点该怎么跑还怎么跑——登记簿本身没有任何
// 执行副作用。
//
// 反过来说，如果哪天这张表开始**驱动**什么（比如让 nginx 按登记生成 vhost），
// 那一刻就不再是 L1 了：定级跟着「改这行会不会让线上发生变化」走，
// 不跟着表名走。
//
// 不含 environment 参数：环境取自调用者身份，不由参数自称（宪法 15 条）。
func appSetDef() action.Definition {
	return action.Definition{
		ID:         ActionAppSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			// app_id 留空 = 新登记，填了 = 改这一条（整行替换）——与
			// server.asset.set / finance.upstream_account.set 同一条约定。
			{Name: "app_id", Type: action.FieldString},
			{Name: "app_key", Type: action.FieldString, Required: true},
			{Name: "display_name", Type: action.FieldString, Required: true},
			{Name: "owner", Type: action.FieldString, Required: true},
			{Name: "primary_domain", Type: action.FieldString},
			{Name: "auth_mode", Type: action.FieldString, Enum: authModeEnum},
			{Name: "status", Type: action.FieldString, Enum: appStatusEnum},
			{Name: "notes", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func appSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		status, err := ParseAppStatus(defaultIfBlank(trimmed(params, "status"), string(AppActive)))
		if err != nil {
			return nil, domainError(err)
		}
		authMode, err := ParseAuthMode(trimmed(params, "auth_mode"))
		if err != nil {
			return nil, domainError(err)
		}

		desired := App{
			AppKey:      trimmed(params, "app_key"),
			DisplayName: trimmed(params, "display_name"),
			// 主机名一律小写：DNS 不区分大小写，而唯一索引区分——
			// 登记 Admin.Solov.CC 与 admin.solov.cc 会得到两行，
			// 「这个域名归谁」于是有两个答案。
			PrimaryDomain: strings.ToLower(trimmed(params, "primary_domain")),
			AuthMode:      authMode,
			Owner:         trimmed(params, "owner"),
			Status:        status,
			Notes:         trimmed(params, "notes"),
			Environment:   p.Environment,
		}

		idText := trimmed(params, "app_id")
		if idText == "" {
			created, err := store.CreateApp(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			// 新建没有 before——资源此前不存在。留空而不是写一个空对象：
			// 「没有前态」和「前态是空的」在审计上不是一回事。
			action.RecordResource(ctx, resourceApp, created.ID.String())
			action.RecordAfter(ctx, appSummary(created))
			return created, nil
		}

		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "app_id 不是合法 UUID", err)
		}
		before, err := store.GetApp(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}

		desired.ID = id
		desired.Environment = before.Environment
		action.RecordResource(ctx, resourceApp, id.String())
		action.RecordBefore(ctx, appSummary(before))

		updated, err := store.UpdateApp(ctx, desired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, appSummary(updated))
		return updated, nil
	}
}

// appSummary 是进审计链的登记摘要；未登记的字段不写键——「没有」与
// 「值是空字符串」在审计上是两件事（同 server.assetSummary 的理由）。
//
// 这张表里没有任何敏感字段：app_key / 域名 / 负责人 / 状态都是公开事实，
// primary_domain 更是**主机名而不是 URL**（领域校验挡掉了带凭据的形态），
// 所以摘要可以完整。真有凭据要登记时，它只能走 CredentialRef（宪法 7 条），
// 而那需要先给这张表加字段，那时这段注释要一起重看。
func appSummary(a App) map[string]any {
	m := map[string]any{
		"app_key":      a.AppKey,
		"display_name": a.DisplayName,
		"owner":        a.Owner,
		"status":       string(a.Status),
		"environment":  a.Environment,
	}
	if a.PrimaryDomain != "" {
		m["primary_domain"] = a.PrimaryDomain
	}
	if a.AuthMode != "" {
		m["auth_mode"] = string(a.AuthMode)
	}
	if a.Notes != "" {
		m["notes"] = a.Notes
	}
	return m
}

// --- extapp.app.retire（L1）-----------------------------------------------

// appRetireDef 声明把一个站点标记为已下线的动作。
//
// 与 appSetDef 分开，同 server.asset.retire 分开的理由：这是一个**能被单独
// 审计检索**的状态迁移（「这个站点什么时候下线的、谁做的、为什么」不该靠比对
// 一次整行更新的 before/after 才答得出），也让列表页能有一个不必打开完整编辑
// 表单的一键下线按钮。
//
// reason 必填：下线会让这个站点从「在线」清单里消失，事后复盘的第一个问题
// 就是「当时为什么下线」。
//
// 仍是 L1：它改的是**登记簿里的一个字段**，不是真的把站点关掉——平台没有
// 关停站点的通道，那是 Platform Lifecycle Operation。把它抬到 L2 会让「改一个
// 记录字段」需要走审批，而真正关站的那个动作反而不经过这里。
func appRetireDef() action.Definition {
	return action.Definition{
		ID:         ActionAppRetire,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "app_id", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func appRetireHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(trimmed(params, "app_id"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "app_id 不是合法 UUID", err)
		}
		reason := trimmed(params, "reason")
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空", nil)
		}

		before, err := store.GetApp(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}
		action.RecordResource(ctx, resourceApp, id.String())
		action.RecordBefore(ctx, appSummary(before))

		updated, err := store.SetAppStatus(ctx, id, AppRetired)
		if err != nil {
			return nil, domainError(err)
		}
		after := appSummary(updated)
		after["reason"] = reason
		action.RecordAfter(ctx, after)
		return updated, nil
	}
}

// --- extapp.release.record（L1）-------------------------------------------

// releaseRecordDef 声明**记录一次已经发生的发布**的动作。
//
// ⚠️ 这个动作**不发布任何东西**。平台没有、也不会有发布 / 回滚端点：
// 发布与迁移是 Platform Lifecycle Operation（宪法 2、3 条 / ADR-003）——
// 走版本化脚本 + 人工批准，不经 Action 通道。后台没有注册任何
// release.* / deploy.* 这类**执行**动作，「版本与发布」页上也没有任何
// 发布按钮（同 ChangesPage 的门禁措辞）。
//
// 那为什么它是一个 Action？因为「往登记簿里加一行」是一次平台配置写操作，
// 而所有平台配置写操作必须经 Action（宪法 2 条）。**被记录的那次发布**不经
// Action，**记录这件事**必须经 Action——两件事共用「发布」两个字，
// 但不是同一件事。
//
// L1，与本包另外两个动作同档：它写的是一行历史记录，不改变任何运行中的东西。
// 记错了的补救是再记一条正确的（登记簿保留两条，谁在什么时候记的由审计链回答），
// 所以本包**不提供删除发布记录的动作**——一条能被删掉的历史记录不是历史记录。
func releaseRecordDef() action.Definition {
	return action.Definition{
		ID:         ActionReleaseRecord,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "app_id", Type: action.FieldString, Required: true},
			{Name: "version", Type: action.FieldString, Required: true},
			{Name: "released_by", Type: action.FieldString, Required: true},
			{Name: "commit_sha", Type: action.FieldString},
			{Name: "kind", Type: action.FieldString, Enum: releaseKindEnum},
			// released_at 留空 = 现在（刚发完就来登记的常见情形）；
			// 补记历史发布时显式给一个 RFC3339 时刻。
			{Name: "released_at", Type: action.FieldString},
			{Name: "notes", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func releaseRecordHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		appID, err := uuid.Parse(trimmed(params, "app_id"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "app_id 不是合法 UUID", err)
		}
		// 先把父行读出来：既是跨环境闸门（宪法 15 条），也是「这个应用存在吗」
		// 的判定。库层的外键会兜住不存在的 app_id，但那报出来的是一条外键
		// 违例，说不出「你要登记的这个应用不在你的环境里」。
		app, err := store.GetApp(ctx, appID)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, app.Environment); err != nil {
			return nil, err
		}

		kind, err := ParseReleaseKind(trimmed(params, "kind"))
		if err != nil {
			return nil, domainError(err)
		}
		releasedAt, err := optionalTimeParam(params, "released_at")
		if err != nil {
			return nil, err
		}
		if releasedAt.IsZero() {
			releasedAt = time.Now().UTC()
		}

		rel, err := store.CreateRelease(ctx, Release{
			AppID:   appID,
			Version: trimmed(params, "version"),
			// 提交号一律小写：短 SHA 常被从 GitHub 页面复制成大写，
			// 而校验正则只认小写十六进制。
			CommitSHA:  strings.ToLower(trimmed(params, "commit_sha")),
			Kind:       kind,
			ReleasedAt: releasedAt,
			ReleasedBy: trimmed(params, "released_by"),
			Notes:      trimmed(params, "notes"),
		})
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceRelease, rel.ID.String())
		action.RecordAfter(ctx, releaseSummary(app, rel))
		return rel, nil
	}
}

// releaseSummary 是进审计链的发布记录摘要。
//
// 带上 app_key 而不只是 app_id：审计事件是给人读的，一串 UUID 回答不了
// 「是哪个站点发版了」，而这条记录的全部价值就在那个问题上。
func releaseSummary(app App, r Release) map[string]any {
	m := map[string]any{
		"app_id":      r.AppID.String(),
		"app_key":     app.AppKey,
		"environment": app.Environment,
		"version":     r.Version,
		"kind":        string(r.Kind),
		"released_at": r.ReleasedAt.UTC().Format(time.RFC3339),
		"released_by": r.ReleasedBy,
	}
	if r.CommitSHA != "" {
		m["commit_sha"] = r.CommitSHA
	}
	if r.Notes != "" {
		m["notes"] = r.Notes
	}
	return m
}

// optionalTimeParam 解析可选的 RFC3339 时刻；空白返回零值。
//
// 只收 RFC3339（带时区）而不收 "2026-09-08 10:00" 这类本地写法：库内一律 UTC
// （宪法 14 条），一个不带时区的时刻在补记历史发布时会静默偏移几个小时，
// 而「上一版是几点上的」正是回滚复盘要问的问题。
func optionalTimeParam(params map[string]any, name string) (time.Time, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 必须是 RFC3339 时刻（形如 2026-09-08T10:00:00Z），"+
				"必须带时区：不带时区的时刻会被当成另一个时刻，而且不报错", name, raw), err)
	}
	return t.UTC(), nil
}
