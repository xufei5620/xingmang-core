package alerts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 四个 Action 的稳定 ID。
const (
	ActionAcknowledge   = "alerts.alert.acknowledge"
	ActionSilenceCreate = "alerts.silence.create"
	// ActionAcknowledgeUpstreamVersion：「这条上游的这个版本我核对过了」。
	//
	// 子片 B 任务书里写的是 alerts.acknowledge_upstream_version，那个 ID
	// **注册时会被拒**：action.actionIDPattern（definition.go）要求
	// `<域>.<资源>.<动作>` 至少三段，两段式在 Definition.Validate 里直接被拒，
	// 进程起不来（cmd/platform-api 的注册返回值会中断启动）。所以取三段式。
	ActionAcknowledgeUpstreamVersion = "alerts.upstream_version.acknowledge"
	// ActionRevokeUpstreamVersion：撤销一条已核对记录。
	//
	// 它存在的理由不是对称好看，是**已核对版本原本是一个没有解除路径的闩**：
	// 点错一次，那条「去核对桥接契约与兼容矩阵」的提醒对该版本永久消失，
	// 而唯一的自动解除条件是上游再升一次版本——那是外部事件，不在操作者
	// 手里。而且 acknowledge 要求 version 与当轮观测逐字相同，所以连
	// 「用另一个值覆盖掉」这条路都走不通。
	ActionRevokeUpstreamVersion = "alerts.upstream_version.revoke"

	actionVersion = "1"
)

// 审计里的资源类型，与库表同名，便于从审计事件直接定位到行。
const (
	resourceAlert          = "alerts.alert"
	resourceSilence        = "alerts.alert_silence"
	resourceUpstreamVerAck = "alerts.upstream_version_ack"
	maxAckVersionBytes     = 64
	maxAckNoteBytes        = 200
)

// ackVersionPattern 是 version 参数允许的形态。
//
// **参数不得可装凭据**（子片 B 任务书第 3 条）。这条正则是第一道闸，但它
// 不是主要的那道——真正的收口在 Handler：version 必须与平台自己从上游观测到
// 的 value_json.version **逐字相同**才会落库，「调用方给什么就存什么」这条路
// 根本不存在。正则只是把明显不像版本号的东西挡在前面，让错误文案更有用。
var ackVersionPattern = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,3}(-[0-9A-Za-z.]{1,32})?$`)

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

// ObservationReader 是核对上游版本时用到的 ops 仓储子集（*ops.Store 满足）。
//
// 只声明一个方法：Handler 要问的问题只有一个——「这条指标此刻观测到的
// 上游版本是什么」。参数里的 version 必须与它逐字相同才会落库。
type ObservationReader interface {
	Get(ctx context.Context, metricKey, environment string) (ops.Observation, error)
}

// RegisterActions 把告警的四个写操作注册为 Action（ADR-003：写操作唯一入口）。
//
// store 允许为 nil：此时只登记声明而不绑定实际执行体，供「注册表完整性」类
// 测试与文档生成使用；Handler 被调用时会返回明确错误（照搬 registry 的做法）。
// observations 同理。
func RegisterActions(reg *action.Registry, store *Store, observations ObservationReader) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{acknowledgeDef(), acknowledgeHandler(store)},
		{silenceCreateDef(), silenceCreateHandler(store)},
		{acknowledgeUpstreamVersionDef(), acknowledgeUpstreamVersionHandler(store, observations)},
		// 撤销与核对**成对注册**：一个能建立永久抑制器的 Action 如果没有
		// 与它同时上线的解除路径，那个抑制器就只能靠外部事件解除。
		{revokeUpstreamVersionDef(), revokeUpstreamVersionHandler(store)},
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

// --- alerts.upstream_version.acknowledge（L1：核对上游版本）---

// acknowledgeUpstreamVersionDef 声明「我核对过这个上游版本了」。
//
// **L1，永久锁定，不抬级。** 理由不是「感觉不严重」，是两条硬的：
//
//  1. 本 Action 有**三个**参数，其中 note 是 ≤200 字节的自由文本，除长度外
//     没有任何形态校验——它在形状上**装得下凭据**。而 L2 及以上会把整包
//     params 原样冻进 core.approval_request.params_json（迁移 000049），
//     并由 GET /api/v1/approvals 回给每一个能看审批队列的人。所以抬级会给
//     note 里的东西开一条展示通道，本 Action 因此**必须**留在 L1，
//     在 action.Schema 支持形状校验（Pattern / Redacted 标记）之前不得抬级。
//
//     ⚠️ 这段注释的第一版写的是「本 Action 的两个参数在形状上装不下凭据」
//     ——它把 note 数漏了，读起来像「所有参数都被约束过，抬级也安全」。
//     一条把自己的理由说错了的注释，比没有注释更容易被拿去做相反的决定。
//     另外两个参数确实收得住：metric_key 必须在这个环境下真有观测，
//     version 必须与平台自己观测到的 value_json.version 逐字相同。
//
//     note 今天只有**一条**读路径：GET /api/v1/audit/events，要 audit.read。
//     已核对清单那个端点（GET /api/v1/alerts/upstream-versions，ops.read）
//     有意**不**回显它——它初版回显过，复审指出那等于把 note 从 audit.read
//     掉到 ops.read（staff 这个粗粒度角色拿的是 registry.read + ops.read +
//     ui.saved_view.manage，不含 audit.read）。要给 note 再开任何一条通道，
//     得先回到这一段把「有哪些通道、各要什么 scope」写全。
//
//  2. 它也确实不该是 L0：L0 的定位是「保存个人视图、低影响偏好」，而这个
//     动作会让一条规则不再命中、让既有告警在下一轮被解决，是改变系统行为的
//     写操作，与 alerts.silence.create 同一档。
//
// 权限复用 ScopeAcknowledge 而不新增 scope：核对上游版本与确认告警是同一类
// 「我看过了」，爆炸半径远小于静默（它不会让任何**别的**告警闭嘴）。见
// permissions.go 里那段说明。
func acknowledgeUpstreamVersionDef() action.Definition {
	return action.Definition{
		ID:         ActionAcknowledgeUpstreamVersion,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeAcknowledge,
		Schema: action.Schema{Fields: []action.Field{
			// metric_key **不写 Enum**：R6 的判据是「这条观测里有没有
			// version」而不是指标键叫什么（见 versionChangeFinding），手列一份
			// 指标键清单会让「将来多一个连接器探测自动被覆盖」这条性质失效。
			// 存在性校验在 Handler 里，而且用的是**同一个现实**：这个环境下
			// 这条指标此刻确实观测到了一个版本。
			{Name: "metric_key", Type: action.FieldString, Required: true},
			{Name: "version", Type: action.FieldString, Required: true},
			// note 可选：给「为什么这次升级不用改钉子」留一句话。它进审计，
			// 不进任何判定。**它是自由文本，装得下凭据**——这就是本 Action
			// 必须锁在 L1 的那条理由（见上面的声明注释）。
			{Name: "note", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func acknowledgeUpstreamVersionHandler(store *Store, observations ObservationReader) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		if observations == nil {
			return nil, fmt.Errorf("alerts 观测来源未绑定：本注册表实例仅用于声明登记")
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		metricKey, err := ackMetricKeyParam(params)
		if err != nil {
			return nil, err
		}

		version := strings.TrimSpace(action.StringParam(params, "version"))
		if version == "" {
			return nil, action.NewError(action.CodeInvalidParams, "version 不能为空白", nil)
		}
		if len(version) > maxAckVersionBytes {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("version 最长 %d 字节", maxAckVersionBytes), nil)
		}
		if !ackVersionPattern.MatchString(version) {
			return nil, action.NewError(action.CodeInvalidParams,
				"version 只允许数字、点、连字符与可选的 v 前缀（如 0.2.3 / v0.2.3-rc.1）", nil)
		}
		note := strings.TrimSpace(action.StringParam(params, "note"))
		if len(note) > maxAckNoteBytes {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("note 最长 %d 字节", maxAckNoteBytes), nil)
		}

		// 环境取自 Principal，**不取自参数**（宪法 15 条：不许调用方自称身份）。
		//
		// 这一步同时是 metric_key 的**存在性判据**，而且是有意换掉
		// ops.KnownMetricKey 的：R6 的命中范围是**发现出来的**（判据是这条
		// 观测里有没有 version，不是它的键叫什么，将来多一个连接器探测就
		// 自动被覆盖），而 KnownMetricKey 读的是一份**手列**的白名单
		// （ops/freshness.go 的 registeredMetrics）。落库侧只校验
		// ValidMetricKey，所以一条未注册的指标观测完全可以存在——两个范围
		// 一旦漂开就会出现「告警响得起来、但按钮点不动」：R6 对某条带 version
		// 的未注册指标命中，Action 直接拒绝，这条告警又回到本片要消灭的
		// 那个状态（只能等旧样本被挤出窗口后自己消失）。今天不出事只是因为
		// 白名单**恰好**覆盖了现有连接器。
		//
		// 已注册清单退到**提示文案**里：拼错照样能拿到有用的报错，但它不再
		// 是准入闸。
		observed, err := observations.Get(ctx, metricKey, p.Environment)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, action.NewError(action.CodePreconditionFailed,
					fmt.Sprintf("环境 %s 下还没有指标 %s 的观测，没有可核对的东西。已注册的指标：%s",
						p.Environment, metricKey, strings.Join(ops.RegisteredMetricKeys(), " / ")), nil)
			}
			return nil, err
		}
		observedVer := observedVersion(observed.Value)
		if observedVer == versionChangeUnknown {
			return nil, action.NewError(action.CodePreconditionFailed,
				fmt.Sprintf("指标 %s 没有上游自报版本，没有可核对的东西", metricKey), nil)
		}
		// 逐字相同才落库。这一条是「参数装不下凭据」的**硬理由**：version 不是
		// 「调用方给什么就存什么」，它必须等于平台自己刚刚从上游观测到的那个值。
		if observedVer != version {
			return nil, action.NewError(action.CodeConflict,
				fmt.Sprintf("上游现在自报的是 %s，你要核对的是 %s，请刷新后再确认",
					observedVer, version), nil)
		}

		ack, err := store.SetUpstreamVersionAck(ctx, UpstreamVersionAck{
			Environment: p.Environment,
			MetricKey:   metricKey,
			Version:     version,
			// source 从观测里读，不从参数来。
			Source:         observed.Source,
			AcknowledgedBy: p.ID,
			AcknowledgedAt: time.Now().UTC(),
			Note:           note,
		})
		if err != nil {
			return nil, domainError(err)
		}

		action.RecordResource(ctx, resourceUpstreamVerAck, ack.Environment+"/"+ack.MetricKey)
		if note != "" {
			action.RecordReason(ctx, note)
		}
		action.RecordAfter(ctx, upstreamVersionAckSummary(ack))
		return ack, nil
	}
}

// ackMetricKeyParam 取并**只校验形态**的 metric_key。
//
// 存在性不在这里判：那一步由「这个环境下这条指标此刻有没有观测」回答
// （见 acknowledgeUpstreamVersionHandler 里的长注释）。这里挡住的是连落库
// 都通不过的形状——库层对 metric_key 有同一条 CHECK，让它在这里就报
// INVALID_PARAMS 比让它走到查询再报别的码诚实。
func ackMetricKeyParam(params map[string]any) (string, error) {
	metricKey := strings.TrimSpace(action.StringParam(params, "metric_key"))
	if metricKey == "" {
		return "", action.NewError(action.CodeInvalidParams, "metric_key 不能为空白", nil)
	}
	if !ops.ValidMetricKey(metricKey) {
		return "", action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("metric_key=%q 须匹配 ^[a-z0-9][a-z0-9_.-]{0,127}$", metricKey), nil)
	}
	return metricKey, nil
}

// --- alerts.upstream_version.revoke（L1：撤销已核对的上游版本）---

// revokeUpstreamVersionDef 声明「刚才那次核对作废」。
//
// **与 acknowledge 同一等级、同一权限、同一身份限制。** 撤销的爆炸半径比
// 核对更小：它只会让一条已经被压住的提醒重新出现，不会让任何东西闭嘴。
//
// 参数里**不带 version**：撤销的对象是「这条上游此刻记着的那条核对」。
// 让调用方再报一次版本号只会多出一种失败形态——「上游已经又升级了，
// 所以你撤不掉上一次的核对」——而那恰恰是最需要撤销的时刻。
//
// reason 必填：撤销是把一条被抑制的告警放回来，事后第一个问题永远是
// 「当时为什么撤」。它与 alerts.silence.create 的 reason 同一条纪律。
func revokeUpstreamVersionDef() action.Definition {
	return action.Definition{
		ID:         ActionRevokeUpstreamVersion,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeAcknowledge,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "metric_key", Type: action.FieldString, Required: true},
			// reason 同样是自由文本，因此本 Action 与 acknowledge 一样
			// **锁定 L1**（L2+ 会把 params 冻进审批单展示给每个审批人）。
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func revokeUpstreamVersionHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		metricKey, err := ackMetricKeyParam(params)
		if err != nil {
			return nil, err
		}
		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}
		if len(reason) > maxAckNoteBytes {
			return nil, action.NewError(action.CodeInvalidParams,
				fmt.Sprintf("reason 最长 %d 字节", maxAckNoteBytes), nil)
		}

		// 环境取自 Principal，不取自参数。
		before, err := store.DeleteUpstreamVersionAck(ctx, p.Environment, metricKey)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, action.NewError(action.CodePreconditionFailed,
					fmt.Sprintf("环境 %s 下的指标 %s 没有已核对的上游版本，没有可撤销的东西",
						p.Environment, metricKey), nil)
			}
			return nil, domainError(err)
		}

		action.RecordResource(ctx, resourceUpstreamVerAck, before.Environment+"/"+before.MetricKey)
		action.RecordBefore(ctx, upstreamVersionAckSummary(before))
		action.RecordReason(ctx, reason)
		// 撤销之后这条记录不存在了。**不写一个空对象**：「没有后态」与
		// 「后态是空的」在审计上不是一回事（同 silenceCreateHandler 对 before
		// 的处理，方向相反）。
		return map[string]any{
			"environment": before.Environment,
			"metric_key":  before.MetricKey,
			"revoked":     true,
		}, nil
	}
}

// upstreamVersionAckSummary 是进审计链的已核对版本摘要。
func upstreamVersionAckSummary(a UpstreamVersionAck) map[string]any {
	return map[string]any{
		"environment":     a.Environment,
		"metric_key":      a.MetricKey,
		"version":         a.Version,
		"source":          a.Source,
		"acknowledged_by": a.AcknowledgedBy,
		"acknowledged_at": a.AcknowledgedAt.UTC().Format(time.RFC3339),
		"note":            a.Note,
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
