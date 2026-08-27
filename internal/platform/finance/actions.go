package finance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 四个 Action 的稳定 ID（设计稿 §8.3 建议的命名）。
const (
	// ActionAccountSet 登记或修改一个上游账号。
	ActionAccountSet = "finance.upstream_account.set"
	// ActionRechargeRatioSet 单独修改充值倍率（§6.3 要求它可被单独审计）。
	ActionRechargeRatioSet = "finance.recharge_ratio.set"
	// ActionTokenMapSet 登记或改写一条令牌映射。
	ActionTokenMapSet = "finance.token_map.set"
	// ActionTokenMapRemove 删除一条令牌映射。
	ActionTokenMapRemove = "finance.token_map.remove"

	actionVersion = "1"
)

// 审计里的资源类型，与库表同名，便于从审计事件直接定位到行。
const (
	resourceUpstreamAccount = "finance.upstream_account"
	resourceTokenMap        = "finance.token_map"
)

// allEnvironments 是这批 Action 允许执行的环境。
//
// 显式列举而不是「除了生产都行」：登记簿在生产才最需要维护，
// 而生产权限不从测试继承是另一条独立的闸门（宪法 15 条，由内核比对
// Principal.Environment 落实，再由每个 Handler 复核资源所属环境）。
var allEnvironments = []string{"development", "staging", "production"}

// humanOnly：Foundation-A 阶段登记簿写操作只允许人类身份。
//
// 尤其要挡住机器身份：登记簿决定「用哪套凭据、按什么倍率算成本」，
// 让采集任务自己有能力改这两样，等于给了一条「算不出来就把倍率改到能算」
// 的路径（ADR-009 红线：AI 不拥有生产后门）。
var humanOnly = []principal.Type{principal.TypeHuman}

// systemTypeEnum / accessMethodEnum / statusEnum 是 Schema 的枚举白名单。
//
// 从领域常量拼出来而不是各写一遍字面量：枚举加一项时，
// 忘了同步 Schema 的后果是「Action 拒绝一个库允许的值」，
// 那种错会被当成权限问题排查很久。
var (
	systemTypeEnum = []string{
		string(SystemSub2API), string(SystemNewAPI), string(SystemOfficial),
	}
	accessMethodEnum = []string{
		string(AccessUpstreamKey), string(AccessOfficialAPI), string(AccessSubscriptionAccount),
	}
	statusEnum = []string{string(StatusActive), string(StatusDisabled)}
)

// RegisterActions 把登记簿的写操作注册为 Action（ADR-003：写操作唯一入口）。
//
// store 允许为 nil：此时只登记声明而不绑定实际执行体，供「注册表完整性」类
// 测试与文档生成使用；Handler 被调用时会返回明确错误（照搬 registry / alerts）。
func RegisterActions(reg *action.Registry, store *Store) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{accountSetDef(), accountSetHandler(store)},
		{rechargeRatioSetDef(), rechargeRatioSetHandler(store)},
		{tokenMapSetDef(), tokenMapSetHandler(store)},
		{tokenMapRemoveDef(), tokenMapRemoveHandler(store)},
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
		return fmt.Errorf("finance store 未绑定：本注册表实例仅用于声明登记")
	}
	return nil
}

// accountSummary 是进审计链的登记簿摘要。
//
// credential_ref 进链是**安全的**且必要的：它是引用不是凭据（ADR-014），
// 而「这条渠道的成本用哪个凭据读出来的」正是对账出问题时要问的第一个问题。
// 明文永远不经过这里——只有 SecretProvider 碰得到值（宪法 7 条）。
//
// recharge_ratio 用定点字符串而不是数值：审计摘要要进哈希链并经 jsonb 往返，
// 一个 float 化的 1.15 会在往返后变成 1.1499999999999999，让链校验不过，
// 更糟的是让「倍率改成了多少」这条记录本身不精确（宪法 13 条）。
func accountSummary(a UpstreamAccount) map[string]any {
	m := map[string]any{
		"system_type":     string(a.SystemType),
		"access_method":   string(a.AccessMethod),
		"base_url":        a.BaseURL,
		"credential_ref":  a.CredentialRef,
		"currency":        a.Currency,
		"business_day_tz": a.BusinessDayTZ,
		"status":          string(a.Status),
		"environment":     a.Environment,
	}
	// 未归属时**不写这个键**（同下面的 recharge_ratio）：「没有归属」与
	// 「归属到某个平台」在审计上是两件事，写一个空串会让前者看起来像
	// 「归属被清空成了空字符串」。
	if a.PlatformID != "" {
		m["platform_id"] = a.PlatformID
	}
	// 未配倍率时**不写这个键**，而不是写 "0" 或 ""：
	// 「没有倍率」（订阅型）与「倍率是某个值」在审计上是两件事。
	if !a.RechargeRatio.IsZero() {
		m["recharge_ratio"] = a.RechargeRatio.String()
	}
	// 同上：未配分组倍率时**不写这个键**。「没有分组倍率」是多数渠道的
	// 正常状态，写一个 "" 会让它看起来像「分组倍率被清空了」。
	if !a.GroupRate.IsZero() {
		m["group_rate"] = a.GroupRate.String()
	}
	return m
}

// mappingSummary 是进审计链的令牌映射摘要。
func mappingSummary(m TokenMapping) map[string]any {
	out := map[string]any{
		"upstream_account_id": m.UpstreamAccountID.String(),
		"upstream_token_id":   m.UpstreamTokenID,
		"own_account_id":      m.OwnAccountID,
	}
	if m.CredentialRef != "" {
		out["credential_ref"] = m.CredentialRef
	}
	return out
}

// callerPrincipal 从上下文取调用者。
//
// 内核在 Execute 里已经校验过身份存在且合法，这里再取一次是为了拿
// Environment：它不能由参数传入——由调用方自称「我要操作生产的登记簿」
// 正是宪法 15 条要挡的东西。
func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

// requireSameEnvironment 是**跨环境闸门**（宪法 15 条）。
//
// 内核只校验「这个 Action 允许在你的环境执行」，它不认识资源——一个 staging
// 身份完全可能拿着生产账号的 UUID 打过来。这道判定必须在读到资源之后。
func requireSameEnvironment(p principal.Principal, resourceEnv string) error {
	if resourceEnv != p.Environment {
		return action.NewError(action.CodePermissionDenied,
			"不允许跨环境操作成本登记簿：调用者身份属于 "+p.Environment, nil)
	}
	return nil
}

// --- finance.upstream_account.set（L1：修改低风险平台配置）---

// accountSetDef 声明登记 / 修改上游账号的动作。
//
// L1 而不是 L2/L3：ADR-003 把「修改低风险平台配置」列为 L1，而登记簿正是
// 平台配置——它不触碰任何第三方系统，不改变任何运行中的服务，写错了改回来
// 即可（历史台账由 ratio_snapshot 逐行冻结，不会被追溯篡改，§6.3）。
// Foundation-A 内核对 L2+ 是 fail-closed 的，声明成 L2 等于让这个动作
// 在当前阶段根本执行不了（设计稿 §8.3 明确要求 L1）。
//
// 不含 environment 参数：环境取自调用者身份，不由参数自称（宪法 15 条）。
// 不含 access_method 的修改路径：它是成本口径的分叉点，改它会让同一个账号的
// 历史成本前后用两套算法算出来而台账里毫无痕迹——要变只能新登记一条。
func accountSetDef() action.Definition {
	return action.Definition{
		ID:         ActionAccountSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeAccountManage,
		Schema: action.Schema{Fields: []action.Field{
			// upstream_account_id 可选：留空 = 新登记，填了 = 改这一条。
			// 用「有没有 id」区分新建与修改，而不是两个 Action：
			// 两者的参数集合完全相同，拆开只会让调用方在选哪个上出错。
			{Name: "upstream_account_id", Type: action.FieldString},
			{Name: "system_type", Type: action.FieldString, Required: true, Enum: systemTypeEnum},
			{Name: "access_method", Type: action.FieldString, Required: true, Enum: accessMethodEnum},
			{Name: "credential_ref", Type: action.FieldString, Required: true},
			{Name: "base_url", Type: action.FieldString},
			// recharge_ratio 是**定点十进制字符串**，不是数字字段。
			//
			// Schema 只有 int / string / bool / string_slice 四种类型，没有
			// Decimal；而 JSON 数字一路解成 float64，1.15 进来就已经不是 1.15 了
			// （宪法 13 条：比例使用 Decimal）。所以倍率必须以字符串传，
			// 由 money.ParseRatio 在整数域里解析。
			{Name: "recharge_ratio", Type: action.FieldString},
			// group_rate 是**分组倍率**（§10.2 + §13 的 groupRate，XM-0049），
			// 与 recharge_ratio 是两个完全不同的量：后者是成本折算的
			// 除数，前者只是定价分组的展示标注，**后端一次都不会乘它**。
			// 同样是定点十进制字符串，理由同上。可空（多数渠道没有）。
			{Name: "group_rate", Type: action.FieldString},
			{Name: "currency", Type: action.FieldString},
			{Name: "business_day_tz", Type: action.FieldString},
			// platform_id 是「哪个自营平台在用这个上游账号」的归属标注
			// （§5.2，XM-0037c）。**可空、且登记后可改**——与上面刻意
			// 不可改的 access_method 相反：它不改变任何一个金额怎么算，
			// 而台账侧 platform_id 的 COALESCE 方向是「空缺可补、已有不动」
			// （§5.3），历史行的归属不会被追溯改写。传空串 = 取消归属。
			{Name: "platform_id", Type: action.FieldString},
			{Name: "status", Type: action.FieldString, Enum: statusEnum},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func accountSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		systemType, err := ParseSystemType(action.StringParam(params, "system_type"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		accessMethod, err := ParseAccessMethod(action.StringParam(params, "access_method"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}

		ratio, err := optionalRatioParam(params, "recharge_ratio")
		if err != nil {
			return nil, err
		}
		groupRate, err := optionalRatioParam(params, "group_rate")
		if err != nil {
			return nil, err
		}

		desired := UpstreamAccount{
			SystemType:    systemType,
			AccessMethod:  accessMethod,
			BaseURL:       strings.TrimSpace(action.StringParam(params, "base_url")),
			CredentialRef: strings.TrimSpace(action.StringParam(params, "credential_ref")),
			RechargeRatio: ratio,
			GroupRate:     groupRate,
			Currency:      defaultIfBlank(action.StringParam(params, "currency"), DefaultCurrency),
			BusinessDayTZ: defaultIfBlank(action.StringParam(params, "business_day_tz"), DefaultBusinessDayTZ),
			PlatformID:    strings.TrimSpace(action.StringParam(params, "platform_id")),
			Status:        Status(defaultIfBlank(action.StringParam(params, "status"), string(StatusActive))),
			Environment:   p.Environment,
		}

		idText := strings.TrimSpace(action.StringParam(params, "upstream_account_id"))
		if idText == "" {
			created, err := store.CreateAccount(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			// 新建没有 before——资源此前不存在。留空而不是写一个空对象：
			// 「没有前态」和「前态是空的」在审计上不是一回事。
			action.RecordResource(ctx, resourceUpstreamAccount, created.ID.String())
			action.RecordAfter(ctx, accountSummary(created))
			return created, nil
		}

		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams,
				"upstream_account_id 不是合法 UUID", err)
		}
		before, err := store.GetAccount(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}
		// access_method 是成本口径的分叉点：改它等于让同一个账号的历史成本
		// 前后用两套算法算出来，而台账里没有任何痕迹。当场拒绝，
		// 并告诉调用方正确的做法是新登记一条。
		if before.AccessMethod != accessMethod {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("不允许修改 access_method（%s → %s）：它是成本口径的分叉点，"+
					"请新登记一条并停用旧的", before.AccessMethod, accessMethod), nil)
		}
		if before.SystemType != systemType {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("不允许修改 system_type（%s → %s）：连接器与取数口径都会变，"+
					"请新登记一条并停用旧的", before.SystemType, systemType), nil)
		}

		desired.ID = id
		action.RecordResource(ctx, resourceUpstreamAccount, id.String())
		action.RecordBefore(ctx, accountSummary(before))

		updated, err := store.UpdateAccount(ctx, desired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, accountSummary(updated))
		return updated, nil
	}
}

// --- finance.recharge_ratio.set（L1：单独审计的倍率修改，§6.3）---

// rechargeRatioSetDef 声明单独修改倍率的动作。
//
// 与 accountSetDef 分开是设计稿 §6.3 的要求：倍率是**唯一**会改变成本口径的
// 字段，可随时改写且 SoloAI 侧无历史（迁移 0084）。给它一个独立的 Action ID
// 与独立的权限，「这个月倍率被谁改过几次」才是一次审计检索就能答的问题，
// 而不是靠比对每条通用更新的 before/after。
//
// reason 必填：改倍率直接改变毛利报表，事后复盘的第一个问题就是「当时为什么改」。
func rechargeRatioSetDef() action.Definition {
	return action.Definition{
		ID:         ActionRechargeRatioSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeRatioManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "upstream_account_id", Type: action.FieldString, Required: true},
			// 定点十进制字符串，理由见 accountSetDef 里同名字段的注释
			{Name: "recharge_ratio", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func rechargeRatioSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(strings.TrimSpace(action.StringParam(params, "upstream_account_id")))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams,
				"upstream_account_id 不是合法 UUID", err)
		}
		ratio, err := requiredRatioParam(params, "recharge_ratio")
		if err != nil {
			return nil, err
		}
		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}

		before, err := store.GetAccount(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}

		action.RecordResource(ctx, resourceUpstreamAccount, id.String())
		action.RecordBefore(ctx, accountSummary(before))
		action.RecordReason(ctx, reason)

		after, err := store.SetRechargeRatio(ctx, id, ratio)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, accountSummary(after))
		return after, nil
	}
}

// --- finance.token_map.set（L1）---

// tokenMapSetDef 声明登记 / 改写令牌映射的动作。
//
// 手工维护是 §11.11 与 §12 的拍板结果（「映射 Action 手工 + 自动发现候选」）：
// 自动发现只产出**候选**，落地仍要人确认——一条自动写进来的错误映射会把成本
// 记到别的渠道上，两条渠道的毛利一个虚高一个虚低而合计完全正确，
// 是最难从总数上看出来的一类错误。
func tokenMapSetDef() action.Definition {
	return action.Definition{
		ID:         ActionTokenMapSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeTokenMapManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "upstream_account_id", Type: action.FieldString, Required: true},
			{Name: "upstream_token_id", Type: action.FieldString, Required: true},
			{Name: "own_account_id", Type: action.FieldString, Required: true},
			// credential_ref 是每令牌明文 sk- 的引用（sub2api 成本侧要用）。
			// 可选：newapi 的成本侧走账号级 New-Api-User + Cookie，没有每令牌凭据。
			{Name: "credential_ref", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func tokenMapSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		account, err := resolveOwningAccount(ctx, store, params)
		if err != nil {
			return nil, err
		}

		tokenID := strings.TrimSpace(action.StringParam(params, "upstream_token_id"))
		mapping := TokenMapping{
			UpstreamAccountID: account.ID,
			UpstreamTokenID:   tokenID,
			OwnAccountID:      strings.TrimSpace(action.StringParam(params, "own_account_id")),
			CredentialRef:     strings.TrimSpace(action.StringParam(params, "credential_ref")),
		}
		// sub2api 的成本侧**必须**有每令牌凭据，否则 /v1/usage 打不出去（§3.1）。
		// 在登记这一刻拒绝，比让采集任务每轮报一次「凭据缺失」有用得多。
		if account.SystemType == SystemSub2API &&
			account.AccessMethod == AccessUpstreamKey &&
			mapping.CredentialRef == "" {
			return nil, action.NewError(action.CodeInvalidParams,
				"sub2api 计量型渠道的每条令牌映射必须带 credential_ref："+
					"成本侧要用该令牌的明文凭据打 /v1/usage（§3.1），明文经 SecretProvider 解析", nil)
		}

		action.RecordResource(ctx, resourceTokenMap,
			account.ID.String()+"/"+tokenID)
		// 改写已有映射时留下前态：「这个令牌本来记在哪个渠道上」正是
		// 成本归属出错时要回答的问题。
		if before, err := store.GetTokenMapping(ctx, account.ID, tokenID); err == nil {
			action.RecordBefore(ctx, mappingSummary(before))
		}

		saved, err := store.PutTokenMapping(ctx, mapping)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, mappingSummary(saved))
		return saved, nil
	}
}

// --- finance.token_map.remove（L1）---

// tokenMapRemoveDef 声明删除令牌映射的动作。
//
// 必须存在：一条写错的映射会持续把成本记到别的渠道上，没有删除路径时
// 唯一的补救是直接改库——那正是宪法 2 条要挡的绕过。
func tokenMapRemoveDef() action.Definition {
	return action.Definition{
		ID:         ActionTokenMapRemove,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeTokenMapManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "upstream_account_id", Type: action.FieldString, Required: true},
			{Name: "upstream_token_id", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func tokenMapRemoveHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		account, err := resolveOwningAccount(ctx, store, params)
		if err != nil {
			return nil, err
		}
		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}
		tokenID := strings.TrimSpace(action.StringParam(params, "upstream_token_id"))

		before, err := store.GetTokenMapping(ctx, account.ID, tokenID)
		if err != nil {
			return nil, domainError(err)
		}

		action.RecordResource(ctx, resourceTokenMap, account.ID.String()+"/"+tokenID)
		action.RecordBefore(ctx, mappingSummary(before))
		action.RecordReason(ctx, reason)

		if err := store.DeleteTokenMapping(ctx, account.ID, tokenID); err != nil {
			return nil, domainError(err)
		}
		// 删除没有 after——资源已不存在。同上：留空而不是写空对象。
		return map[string]any{
			"upstream_account_id": account.ID.String(),
			"upstream_token_id":   tokenID,
			"deleted":             true,
		}, nil
	}
}

// resolveOwningAccount 解析 upstream_account_id 并落实跨环境闸门。
//
// 令牌映射本身没有 environment 列（它挂在账号下），所以环境判定必须先把
// 账号读出来——一个 staging 身份拿着生产账号的 UUID 打过来，
// 只有这一步拦得住（宪法 15 条）。
func resolveOwningAccount(
	ctx context.Context, store *Store, params map[string]any,
) (UpstreamAccount, error) {
	p, err := callerPrincipal(ctx)
	if err != nil {
		return UpstreamAccount{}, err
	}
	id, err := uuid.Parse(strings.TrimSpace(action.StringParam(params, "upstream_account_id")))
	if err != nil {
		return UpstreamAccount{}, action.NewError(
			action.CodeInvalidParams, "upstream_account_id 不是合法 UUID", err)
	}
	account, err := store.GetAccount(ctx, id)
	if err != nil {
		return UpstreamAccount{}, domainError(err)
	}
	if err := requireSameEnvironment(p, account.Environment); err != nil {
		return UpstreamAccount{}, err
	}
	return account, nil
}

// optionalRatioParam 解析可选的倍率参数；缺省返回零值 Ratio（= 未配置）。
func optionalRatioParam(params map[string]any, name string) (money.Ratio, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return money.Ratio{}, nil
	}
	return parseRatioParam(raw, name)
}

// requiredRatioParam 解析必填的倍率参数。
func requiredRatioParam(params map[string]any, name string) (money.Ratio, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return money.Ratio{}, action.NewError(action.CodeInvalidParams, name+" 不能为空白", nil)
	}
	return parseRatioParam(raw, name)
}

func parseRatioParam(raw, name string) (money.Ratio, error) {
	ratio, err := money.ParseRatio(raw)
	if err != nil {
		return money.Ratio{}, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 必须是定点十进制字符串（如 \"1.5\"），最多 9 位小数", name, raw), err)
	}
	if !ratio.IsPositive() {
		// 算术层对非正倍率有「按 1 处理」的兜底（对齐 SoloAI），但那条路
		// 只用于与标准答案对齐；登记入口一律拒绝，理由见 money.Divide 的注释。
		return money.Ratio{}, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 必须为正：非正倍率会被静默当成 1，"+
				"让「上游涨价」与「倍率填错」在台账上无法区分", name, raw), nil)
	}
	return ratio, nil
}

func defaultIfBlank(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// domainError 把领域错误映射成 Action 错误码。
//
// 不做这层映射的话，「计量型渠道必须配 recharge_ratio」会以 INTERNAL 返回，
// 调用方看到的是「服务器内部错误」，于是去查日志、查数据库，
// 而真正要做的只是补一个参数。
//
// ErrNotFound 也归 INVALID_PARAMS（→ 400）而不是 ACTION_NOT_REGISTERED（→ 404）：
// 后者的语义是「这个 Action 没注册」，拿它表达「这个账号不存在」会让前端
// 把「id 打错了」显示成「功能不存在」。内核的错误码集合里没有资源级 404
// （见 internal/platform/action/errors.go），而「你给的 id 在登记簿里查不到」
// 本来就是一个参数问题——调用方要改的是参数，不是权限也不是重试。
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrMissingField),
		errors.Is(err, ErrInvalidFormat),
		errors.Is(err, ErrInconsistent),
		errors.Is(err, money.ErrFormat),
		errors.Is(err, money.ErrUnknownCurrency):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	default:
		// 库层 CHECK 违反等落这里：它们的文本带约束名，对调用方没用，
		// 由 WriteError 统一收敛成 INTERNAL 并隐藏细节。
		return err
	}
}
