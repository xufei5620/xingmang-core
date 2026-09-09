package alerts

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

// ActionAcknowledge / ActionSilenceCreate 是两个 Action 的稳定 ID。
const (
	ActionAcknowledge   = "alerts.alert.acknowledge"
	ActionSilenceCreate = "alerts.silence.create"
	actionVersion       = "1"
)

// 审计里的资源类型，与库表同名，便于从审计事件直接定位到行。
const (
	resourceAlert   = "alerts.alert"
	resourceSilence = "alerts.alert_silence"
)

// maxSilenceMinutes 是单个静默窗口的时长上限（7 天）。
//
// 上限是必须的：一个「静默 99999 分钟」的窗口等于永久关掉这条规则，
// 而且没有任何东西会提醒有人去解除它——一年后事故复盘时才会发现告警
// 早就哑了。7 天覆盖得住「等上游修好」「过完长假再说」这类真实场景，
// 更长的诉求应该走「改规则」或「停用采集」（宪法 26 条的 Kill Switch），
// 那两条路都有变更记录，静默没有。
const maxSilenceMinutes = 7 * 24 * 60

// allEnvironments 是两个 Action 允许执行的环境。
//
// 显式列举而不是「除了生产都行」：告警确认在生产才最需要，
// 而生产权限不从测试继承是另一条独立的闸门（规格 §20.5，由内核比对
// Principal.Environment 落实）。
var allEnvironments = []string{"development", "staging", "production"}

// humanOnly：Foundation-A 阶段告警的确认与静默只允许人类身份。
//
// 尤其是静默：让机器有能力关掉告警，等于给了一条「出事时自己把嘴捂上」的
// 路径。AI 参与运维处置属于 AI Control Plane 的范围（ADR-009 红线：
// AI 不拥有生产后门）。
var humanOnly = []principal.Type{principal.TypeHuman}

// RegisterActions 把告警的两个写操作注册为 Action（ADR-003：写操作唯一入口）。
//
// store 允许为 nil：此时只登记声明而不绑定实际执行体，供「注册表完整性」类
// 测试与文档生成使用；Handler 被调用时会返回明确错误（照搬 registry 的做法）。
func RegisterActions(reg *action.Registry, store *Store) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{acknowledgeDef(), acknowledgeHandler(store)},
		{silenceCreateDef(), silenceCreateHandler(store)},
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
		return fmt.Errorf("alerts store 未绑定：本注册表实例仅用于声明登记")
	}
	return nil
}

// alertSummary 是进审计链的告警摘要。
//
// 只挑「改了会影响运营判断」的字段。detail 不进链：它是随每轮评估刷新的
// 描述文本（余额数字会变），把它塞进 before/after 会让每次确认的审计事件
// 都带一段与本次动作无关的噪声。审计要回答的是「谁在什么时候把哪条告警
// 从什么状态改成了什么状态」。
func alertSummary(a Alert) map[string]any {
	m := map[string]any{
		"rule_key":     a.RuleKey,
		"dedup_key":    a.DedupKey,
		"severity":     string(a.Severity),
		"status":       string(a.Status),
		"title":        a.Title,
		"environment":  a.Environment,
		"fire_count":   a.FireCount,
		"opened_at":    a.OpenedAt.UTC().Format(time.RFC3339),
		"last_seen_at": a.LastSeenAt.UTC().Format(time.RFC3339),
	}
	if a.AcknowledgedAt != nil {
		m["acknowledged_at"] = a.AcknowledgedAt.UTC().Format(time.RFC3339)
	}
	return m
}

// silenceSummary 是进审计链的静默窗口摘要。
//
// reason 进链是**必须的**：静默是主动让告警闭嘴，事后复盘的第一个问题
// 就是「当时为什么静默」。它同时经 action.RecordReason 进事件的 reason 列。
func silenceSummary(s Silence) map[string]any {
	return map[string]any{
		"rule_key":    s.RuleKey,
		"environment": s.Environment,
		"reason":      s.Reason,
		"starts_at":   s.StartsAt.UTC().Format(time.RFC3339),
		"ends_at":     s.EndsAt.UTC().Format(time.RFC3339),
		"created_by":  s.CreatedBy,
	}
}

// callerPrincipal 从上下文取调用者。
//
// 内核在 Execute 里已经校验过身份存在且合法，这里再取一次是为了拿
// Environment 与 ID：两者都不能由参数传入——由调用方自称「我要操作生产的
// 告警」正是宪法 15 条要挡的东西。
func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

// domainError 把仓储错误翻成 Action 错误码。
//
// **修的是一个真实的错误分类问题**（XM-ERRCODE-AUDIT）：在这之前
// `store.Get` 的错误是**裸返回**的，于是「确认一个不存在的 alert_id」在内核
// 那里被归一成 EXECUTION_FAILED——调用方拿到 502，像是服务端坏了，而实际上
// 是他给的 id 不对。这个错分在 XM-KERNEL-ERRCODE0 之前看不出来（那时**所有**
// Handler 错误都是 502），修完内核之后它成了唯一还错着的那一类。
//
// ErrNotFound 取 PRECONDITION_FAILED（412）是跟随本仓多数派：assurance 的
// 「声明不存在」与 credentials 的「credential_ref 尚未登记」都是这个码。
// （finance 的渠道绑定用的是 NOT_REGISTERED/404——三处不一致这件事记在
// 交接文档的 follow_ups 里，不在本片顺手统一。）
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return action.NewError(action.CodePreconditionFailed, "指定的告警不存在", err)
	case errors.Is(err, ErrNotAcknowledgeable):
		// **409 而不是 412**：告警在，只是状态不允许确认（已解决 / 已静默）。
		// 「与目标资源的当前状态冲突」正是 409 的定义；412 那一档留给
		// 「参数指名的对象不存在」。
		//
		// 这条真的会被撞到：评估器每轮都会把不再命中的告警自动转 RESOLVED，
		// 而运维点「确认」的那一刻它可能刚好已经恢复了。此前这个竞态返回
		// **502**，看起来像服务端坏了。
		return action.NewError(action.CodeConflict,
			"该告警当前状态不允许确认（只有待处理 / 重新触发可确认，它可能刚刚自动恢复了）", err)
	case errors.Is(err, ErrMissingField), errors.Is(err, ErrInvalidFormat):
		// 防御性：静默窗口的这几项在 Handler 里已经先校验过一遍（rule_key 走
		// KnownRuleKey、时长与理由各有判断），所以这一支**目前不可达**。
		// 映射它不是因为今天会走到，而是因为 Store.CreateSilence 的契约里
		// 确实会返回它们——留一条正确的翻译比留一个 502 的口子便宜。
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	default:
		// 其余仓储错误原样返回：内核会归一成 EXECUTION_FAILED 并换掉文案，
		// 细节（约束名、内网地址）只进服务端日志。
		return err
	}
}

// --- alerts.alert.acknowledge（L0：确认普通告警）---

// acknowledgeDef 声明确认动作。
//
// L0 而不是 L1：ADR-003 的风险等级表把「确认普通告警」列在 L1 的例子里，
// 但那一行的完整表述是「修改低风险平台配置、确认普通告警」——L1 描述的是
// **改配置**那一类。确认不改变任何配置、不影响任何运行中的系统，
// 它只是在一条告警上记下「有人看见了」，且完全可逆（下一轮命中仍会更新
// last_seen_at，运维随时能重新处理）。任务书也明确要求 L0。
// 静默才是真正会改变系统行为的那个，它是 L1。
func acknowledgeDef() action.Definition {
	return action.Definition{
		ID:         ActionAcknowledge,
		Version:    actionVersion,
		RiskLevel:  action.L0,
		Permission: ScopeAcknowledge,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "alert_id", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func acknowledgeHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(action.StringParam(params, "alert_id"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "alert_id 不是合法 UUID", err)
		}

		before, err := store.Get(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		// **跨环境闸门**（宪法 15 条）。内核只校验「这个 Action 允许在你的
		// 环境执行」，它不认识资源——一个 staging 身份完全可能拿着生产告警的
		// UUID 打过来。这道判定必须在这里，而且必须在读到资源之后。
		if before.Environment != p.Environment {
			return nil, action.NewError(action.CodePermissionDenied,
				"不允许跨环境操作告警：调用者身份属于 "+p.Environment, nil)
		}

		action.RecordResource(ctx, resourceAlert, before.ID.String())
		action.RecordBefore(ctx, alertSummary(before))

		after, err := store.Acknowledge(ctx, id, time.Now().UTC())
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, alertSummary(after))
		return after, nil
	}
}

// --- alerts.silence.create（L1：修改低风险平台配置）---

// silenceCreateDef 声明创建静默窗口的动作。
//
// duration_minutes 而不是 ends_at：让调用方传一个绝对时刻，就得处理
// 「传了过去的时间」「时区不一致」「客户端时钟偏了」三类问题，
// 而这三类问题的失败形态都是「以为静默了其实没有」。传时长则窗口起点
// 恒为服务端的此刻，只有一个正数需要校验。
func silenceCreateDef() action.Definition {
	return action.Definition{
		ID:         ActionSilenceCreate,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeSilenceManage,
		Schema: action.Schema{Fields: []action.Field{
			// rule_key 必填但**允许空串**：空串是「全局静默」这个明确的选择。
			// 设成可选字段的话，「忘了填」与「有意全局」在参数里长得一模一样，
			// 而这两者的后果差着整套告警。Schema 的 Required 只保证键存在，
			// 值的语义由 Handler 判定。
			{Name: "rule_key", Type: action.FieldString, Required: true},
			{Name: "duration_minutes", Type: action.FieldInt, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func silenceCreateHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		ruleKey := strings.TrimSpace(action.StringParam(params, "rule_key"))
		// 拼错的 rule_key 会静默**零条**告警，而创建者以为已经静默了。
		// 这正是宪法 12 条禁止的那种沉默，必须当场拒绝并告诉他有哪些键。
		if ruleKey != "" && !KnownRuleKey(ruleKey) {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("rule_key=%q 不是已注册的规则；留空表示全局静默，或从 %s 中选一个",
					ruleKey, strings.Join(RuleKeys(), " / ")), nil)
		}

		minutes, err := intParam(params, "duration_minutes")
		if err != nil {
			return nil, err
		}
		if minutes < 1 || minutes > maxSilenceMinutes {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("duration_minutes 必须在 1 到 %d（7 天）之间", maxSilenceMinutes), nil)
		}

		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}

		now := time.Now().UTC()
		silence, err := store.CreateSilence(ctx, Silence{
			RuleKey:     ruleKey,
			Environment: p.Environment,
			Reason:      reason,
			StartsAt:    now,
			EndsAt:      now.Add(time.Duration(minutes) * time.Minute),
			CreatedBy:   p.ID,
		})
		if err != nil {
			return nil, domainError(err)
		}

		action.RecordResource(ctx, resourceSilence, silence.ID.String())
		// 新建没有 before——窗口此前不存在。留空而不是写一个空对象：
		// 「没有前态」和「前态是空的」在审计上不是一回事。
		action.RecordReason(ctx, reason)
		action.RecordAfter(ctx, silenceSummary(silence))
		return silence, nil
	}
}

// intParam 从已校验的参数里取整数。
//
// Schema 已经保证了类型（int / int64 / 无小数的 float64），这里只是把三种
// 表示收敛成一个 int64。JSON 解码后整数是 float64，所以这个分支是主路径。
func intParam(params map[string]any, name string) (int64, error) {
	switch n := params[name].(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	default:
		return 0, action.NewError(action.CodeInvalidParams, name+" 必须是整数", nil)
	}
}
