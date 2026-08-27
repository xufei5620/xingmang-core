package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// 第一批规则的稳定键。它们进 alert.rule_key，也是静默窗口的匹配键，
// 因此**改名等于让历史告警与已存在的静默窗口一起失配**——只增不改。
const (
	// RuleMetricSyncFailed：某条指标最近一次同步失败（规格 §9.1 的 failed 态）。
	RuleMetricSyncFailed = "metric.sync.failed"
	// RuleMetricDataStale：某条指标的数据陈旧已持续 N 个采集周期。
	RuleMetricDataStale = "metric.data.stale"
	// RuleChannelTokenInvalid：某个上游渠道的 token 已失效。
	RuleChannelTokenInvalid = "channel.token.invalid"
	// RuleChannelBalanceLow：某个上游渠道余额低于阈值。
	RuleChannelBalanceLow = "channel.balance.low"
	// RuleSyncConsecutiveFailed：某条指标连续 N 轮同步失败。
	RuleSyncConsecutiveFailed = "metric.sync.consecutive_failed"
)

// 默认配置。三个数字都可由部署覆盖（见 RuleConfig 各字段与 cmd/platform-worker）。
const (
	// DefaultCollectionInterval 与 jobs.DefaultSub2APISyncInterval 一致。
	// 它是「采集周期」的单位，R1b 的「持续 ≥2 采集周期」以它计。
	// 一致性由 rules_metrickeys_test.go 里的外部测试守住。
	DefaultCollectionInterval = 300 * time.Second
	// DefaultBalanceThresholdMinorUnits 是渠道余额告警阈值，单位是**最小货币单位**
	// （宪法 13 条：金额禁止 float）。500000 = 5000.00 CNY。
	DefaultBalanceThresholdMinorUnits int64 = 500_000
	// DefaultConsecutiveFailureThreshold 是 R4 的连续失败轮数阈值。
	DefaultConsecutiveFailureThreshold = 3
	// DefaultChannelBalanceMetricKey 是渠道规则读的那条聚合指标。
	//
	// 写成字面量而不是 import connectors/sub2api：本包被 httpapi 与 jobs 引用，
	// 让平台层依赖某一个具体 Connector 会让「换一个上游」变成改平台代码。
	// 字面量重复的代价由外部测试包里的一致性测试兜住——那个测试同时看得见
	// 两边，任何一边改了键名都会当场失败（照搬 ops/metrickeys_test.go 的做法）。
	DefaultChannelBalanceMetricKey = "sub2api.channels.balance"
)

// staleCyclesBeforeAlert 是 R1b 的「持续时间」，单位是采集周期。
//
// 为什么是 2 而不是 1：陈旧的定义本身已经是「超过 staleness_threshold」，
// 而阈值（sub2api 是 1800s）已经宽于采集周期（300s）好几倍。刚过阈值那一刻
// 多半是一次抖动，再等两个周期还没回来才是真的停更。
const staleCyclesBeforeAlert = 2

// maxConsecutiveSamples 是 R4 一次回看的样本数上限。
//
// 只要够判定阈值就行：多取几条是为了让「阈值调大」不必立刻改这里，
// 但没必要把整段历史拖回来——判据只关心**最新那一串**连续失败。
const maxConsecutiveSamples int32 = 20

// consecutiveLookback 是 R4 回看的时间窗口。
//
// 采集周期 300s 时，24 小时足够装下几百轮。窗口存在的意义不是"看得更远"，
// 而是防止一条早已停采的指标把三个月前的失败串拿来当"现在连续失败"。
const consecutiveLookback = 24 * time.Hour

// Rule 是一条告警规则的静态声明。
//
// 九个字段逐条对应规格 §9.3「告警规则必须包含」的九项：数据来源、条件、
// 持续时间、严重度、恢复条件、去重键、通知渠道、静默策略、负责人。
// 它们大多是**给人看的**（进 docs/modules/alerts/README.md 的规则清单表，
// 也进 Handoff），真正参与判定的只有 Key / Severity / For。
//
// 为什么不把规则做成库里的数据：规则要跑代码——读 ops 观测、解 value_json、
// 算持续时间。做成数据就得同时发明一门表达式语言和它的沙箱，那是
// Foundation-B 的事。在那之前，改规则是一次带 PR、评审与测试的代码变更，
// 这在本阶段反而是更强的保障。
type Rule struct {
	// Key 是稳定标识，进 alert.rule_key，也是静默窗口的匹配键。
	Key string
	// Title 是给人看的规则名（进文档与前端筛选器）。
	Title string
	// Source 是数据来源（§9.3）。
	Source string
	// Condition 是触发条件的人类可读表述（§9.3）。
	Condition string
	// For 是条件必须持续多久才触发（§9.3「持续时间」）。0 表示立即。
	For time.Duration
	// Severity 是严重度（§9.3）。
	Severity Severity
	// Recovery 是恢复条件（§9.3）。本档的恢复统一由评估器实现：
	// 一轮评估里没有再命中的活跃告警自动转 RESOLVED。
	Recovery string
	// DedupKey 是去重键的**模板**（§9.3）。实际值由评估器按模板拼出。
	DedupKey string
	// Channels 是通知渠道（§9.3）。本档全部规则共用同一组已配置渠道，
	// 按严重度分流留给 Foundation-B。
	Channels []string
	// SilencePolicy 是静默策略（§9.3）。
	SilencePolicy string
	// Owner 是负责人（§9.3）。
	Owner string
}

// RuleConfig 是第一批规则里那几个可由部署调整的阈值。
type RuleConfig struct {
	// CollectionInterval 是采集周期，R1b 的「2 采集周期」以它为单位。
	CollectionInterval time.Duration
	// BalanceThresholdMinorUnits 是渠道余额阈值（最小货币单位）。
	BalanceThresholdMinorUnits int64
	// ConsecutiveFailureThreshold 是 R4 的连续失败轮数阈值。
	ConsecutiveFailureThreshold int
	// ChannelBalanceMetricKey 是渠道规则读的聚合指标键。
	ChannelBalanceMetricKey string
	// Owner 是这批规则的负责人（§9.3 要求每条规则都有）。
	Owner string
}

// DefaultRuleConfig 返回默认阈值。
func DefaultRuleConfig() RuleConfig {
	return RuleConfig{
		CollectionInterval:          DefaultCollectionInterval,
		BalanceThresholdMinorUnits:  DefaultBalanceThresholdMinorUnits,
		ConsecutiveFailureThreshold: DefaultConsecutiveFailureThreshold,
		ChannelBalanceMetricKey:     DefaultChannelBalanceMetricKey,
		Owner:                       "platform-ops",
	}
}

func (c RuleConfig) normalized() RuleConfig {
	d := DefaultRuleConfig()
	if c.CollectionInterval <= 0 {
		c.CollectionInterval = d.CollectionInterval
	}
	if c.BalanceThresholdMinorUnits <= 0 {
		// 零与负阈值都等于「永不触发」（条件是 balance < threshold），
		// 但看起来像是配了一个阈值。回落到默认值而不是照单全收——
		// 静默失效的护栏比没有护栏更危险。
		//
		// **零必须一起挡住**：RuleConfig{} 的零值就是 0，而用字面量构造
		// 配置正是最常见的调用方式（见 jobs.NewClient）。只挡负数的话，
		// 「忘了填阈值」的结果恰好是这条规则永远不响——一个不会报错、
		// 不会留痕、只在需要它的那天才被发现的失效。
		c.BalanceThresholdMinorUnits = d.BalanceThresholdMinorUnits
	}
	if c.ConsecutiveFailureThreshold < 1 {
		c.ConsecutiveFailureThreshold = d.ConsecutiveFailureThreshold
	}
	if strings.TrimSpace(c.ChannelBalanceMetricKey) == "" {
		c.ChannelBalanceMetricKey = d.ChannelBalanceMetricKey
	}
	if strings.TrimSpace(c.Owner) == "" {
		c.Owner = d.Owner
	}
	return c
}

// defaultChannels 是本档规则声明的通知渠道。
//
// 声明的是「该往哪儿投」，实际投得出去取决于部署配了哪几个
// （见 notify.go）。两者故意分开：规则不该因为某个环境没配 Telegram 就
// 变成另一条规则。
var defaultChannels = []string{"telegram", "webhook"}

const silencePolicyText = "支持按 rule_key 静默与全局静默；窗口内不投递，窗口过期后条件仍成立则转回 OPEN 并投递"

// Rules 返回第一批规则的完整声明（顺序稳定，便于文档与测试逐条比对）。
func Rules(cfg RuleConfig) []Rule {
	cfg = cfg.normalized()
	staleFor := time.Duration(staleCyclesBeforeAlert) * cfg.CollectionInterval
	return []Rule{
		{
			Key:           RuleMetricSyncFailed,
			Title:         "指标同步失败",
			Source:        "ops.metric_observation（规格 §9.1 新鲜度模型）",
			Condition:     "新鲜度状态为 failed（最近一次同步失败，last_error_code 非空）",
			For:           0,
			Severity:      SeverityCritical,
			Recovery:      "该指标恢复到非 failed 状态（下一轮采集成功）",
			DedupKey:      RuleMetricSyncFailed + ":<environment>:<metric_key>",
			Channels:      defaultChannels,
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},
		{
			Key:       RuleMetricDataStale,
			Title:     "指标数据陈旧",
			Source:    "ops.metric_observation（规格 §9.1 新鲜度模型）",
			Condition: fmt.Sprintf("新鲜度状态为 stale，且滞后已超过阈值 + %d 个采集周期（%s）", staleCyclesBeforeAlert, staleFor),
			For:       staleFor,
			Severity:  SeverityWarning,
			Recovery:  "该指标的 observed_at 重新进入新鲜度阈值内",
			DedupKey:  RuleMetricDataStale + ":<environment>:<metric_key>",
			Channels:  defaultChannels,
			// 与 R1 同一条指标不会同时命中：Freshness 的状态优先级里
			// failed 高于 stale，一条记录只会落在其中一个状态上。
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},
		{
			Key:           RuleChannelTokenInvalid,
			Title:         "渠道 token 失效",
			Source:        cfg.ChannelBalanceMetricKey + " 的 value_json.channels[].token_valid",
			Condition:     "token_valid 明确为 false",
			For:           0,
			Severity:      SeverityWarning,
			Recovery:      "token_valid 恢复为 true，或该渠道从上游清单中消失",
			DedupKey:      RuleChannelTokenInvalid + ":<environment>:<channel_id>",
			Channels:      defaultChannels,
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},
		{
			Key:       RuleChannelBalanceLow,
			Title:     "渠道余额不足",
			Source:    cfg.ChannelBalanceMetricKey + " 的 value_json.channels[].balance_minor_units",
			Condition: fmt.Sprintf("余额 < %d（最小货币单位）", cfg.BalanceThresholdMinorUnits),
			For:       0,
			Severity:  SeverityWarning,
			Recovery:  "余额回到阈值之上，或该渠道从上游清单中消失",
			DedupKey:  RuleChannelBalanceLow + ":<environment>:<channel_id>",
			Channels:  defaultChannels,
			// 阈值可由 XM_ALERT_BALANCE_THRESHOLD_MINOR_UNITS 按环境调整。
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},
		{
			Key:           RuleSyncConsecutiveFailed,
			Title:         "同步连续失败",
			Source:        "ops.metric_observation_sample（XM-0024 历史样本）",
			Condition:     fmt.Sprintf("最近 %d 条样本连续为 failed", cfg.ConsecutiveFailureThreshold),
			For:           time.Duration(cfg.ConsecutiveFailureThreshold) * cfg.CollectionInterval,
			Severity:      SeverityCritical,
			Recovery:      "出现任意一条成功样本（连续串被打断）",
			DedupKey:      RuleSyncConsecutiveFailed + ":<environment>:<metric_key>",
			Channels:      defaultChannels,
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},
	}
}

// RuleKeys 返回全部已知规则键（升序）。
//
// 建静默窗口时用它做存在性校验：一个拼错的 rule_key 会静默**零条**告警，
// 而创建者以为已经静默了——这正是宪法 12 条禁止的那种沉默。
func RuleKeys() []string {
	rules := Rules(DefaultRuleConfig())
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Key)
	}
	sort.Strings(out)
	return out
}

// KnownRuleKey 报告 key 是否是已注册的规则键。
func KnownRuleKey(key string) bool {
	for _, k := range RuleKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// Finding 是一次规则命中：评估器算出来、交给仓储去去重落库的那份事实。
type Finding struct {
	RuleKey         string
	DedupKey        string
	Severity        Severity
	Title           string
	Detail          string
	SourceMetricKey string
}

// MetricSource 是评估器用到的 ops 仓储子集。
//
// 只声明用得到的两个方法而不是直接依赖 *ops.Store：单元测试能用内存实现
// 跑完整条规则链，不必为了验证"余额低于阈值会告警"先起一个库。
// *ops.Store 天然满足本接口。
type MetricSource interface {
	ListByEnvironment(ctx context.Context, environment string) ([]ops.Observation, error)
	ListSamples(ctx context.Context, environment, metricKey string, since time.Time, limit int32) ([]ops.Observation, bool, error)
}

// Evaluator 按第一批规则评估某个环境的当前状态。
//
// 它**只算不写**：产出 Finding 清单，落库、去重、静默判定与自动恢复由
// Reconciler 负责（见 reconcile.go）。分开是为了让规则本身可以在没有库的
// 情况下被完整测试——规则是这个模块里最容易写错也最该被密集测试的部分。
type Evaluator struct {
	cfg    RuleConfig
	source MetricSource
}

// NewEvaluator 创建评估器。
func NewEvaluator(source MetricSource, cfg RuleConfig) *Evaluator {
	return &Evaluator{cfg: cfg.normalized(), source: source}
}

// Config 返回归一化后的阈值配置（供日志与文档打印实际生效值）。
func (e *Evaluator) Config() RuleConfig { return e.cfg }

// Evaluate 跑一轮评估，返回此刻命中的全部规则。
//
// 返回顺序稳定（按 dedup_key 升序）：让日志、测试与 Handoff 里的清单可比对。
func (e *Evaluator) Evaluate(ctx context.Context, environment string, now time.Time) ([]Finding, error) {
	if e.source == nil {
		return nil, fmt.Errorf("alerts: evaluator 没有指标来源")
	}
	if environment == "" {
		return nil, fmt.Errorf("environment: %w", ErrMissingField)
	}
	now = now.UTC()

	observations, err := e.source.ListByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("读取指标观测: %w", err)
	}

	staleFor := time.Duration(staleCyclesBeforeAlert) * e.cfg.CollectionInterval
	var findings []Finding

	for _, o := range observations {
		f := o.Freshness(now)

		switch f.State {
		case ops.StateFailed:
			findings = append(findings, Finding{
				RuleKey:  RuleMetricSyncFailed,
				DedupKey: dedupKey(RuleMetricSyncFailed, environment, o.MetricKey),
				Severity: SeverityCritical,
				Title:    fmt.Sprintf("指标 %s 同步失败", o.MetricKey),
				Detail: fmt.Sprintf("来源 %s，错误码 %s，最近一次同步尝试 %s。%s",
					o.Source, f.LastErrorCode, o.SyncedAt.Format(time.RFC3339),
					describeLastSuccess(f.LastSuccess)),
				SourceMetricKey: o.MetricKey,
			})

			// R4 只在**当前正在失败**时才去翻历史样本。
			//
			// 两个理由：语义上「连续失败」本就要求最新那条是失败的；
			// 成本上健康时一条样本查询都不发——评估每 60 秒跑一次，
			// 给每条指标无条件配一次历史查询是纯浪费。
			streak, err := e.consecutiveFailures(ctx, environment, o.MetricKey, now)
			if err != nil {
				return nil, err
			}
			if streak >= e.cfg.ConsecutiveFailureThreshold {
				findings = append(findings, Finding{
					RuleKey:  RuleSyncConsecutiveFailed,
					DedupKey: dedupKey(RuleSyncConsecutiveFailed, environment, o.MetricKey),
					Severity: SeverityCritical,
					Title:    fmt.Sprintf("指标 %s 同步连续失败 %d 轮", o.MetricKey, streak),
					Detail: fmt.Sprintf("最近 %d 条样本全部失败，最新错误码 %s。已经不是一次抖动，请按 Runbook 处置。",
						streak, f.LastErrorCode),
					SourceMetricKey: o.MetricKey,
				})
			}

		case ops.StateStale:
			// f.StalenessSeconds 在 stale 态下必然非 nil（见 ops.Observation.Freshness：
			// 只有 ObservedAt == nil 才会留空，而那时状态是 uninitialized/failed）。
			// 仍然判一次 nil：宁可少报一条，也不要在这里 panic 掉整轮评估。
			if f.StalenessSeconds == nil {
				continue
			}
			if *f.StalenessSeconds < int64(f.ThresholdSeconds)+int64(staleFor.Seconds()) {
				continue
			}
			findings = append(findings, Finding{
				RuleKey:  RuleMetricDataStale,
				DedupKey: dedupKey(RuleMetricDataStale, environment, o.MetricKey),
				Severity: SeverityWarning,
				Title:    fmt.Sprintf("指标 %s 数据陈旧", o.MetricKey),
				Detail: fmt.Sprintf("数据时间落后 %d 秒（新鲜度阈值 %d 秒，已持续超过 %d 个采集周期）。来源 %s。",
					*f.StalenessSeconds, f.ThresholdSeconds, staleCyclesBeforeAlert, o.Source),
				SourceMetricKey: o.MetricKey,
			})
		}

		if o.MetricKey == e.cfg.ChannelBalanceMetricKey {
			findings = append(findings, e.channelFindings(o, f, environment)...)
		}
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].DedupKey < findings[j].DedupKey })
	return findings, nil
}

// channelFindings 从渠道余额聚合指标里算出 R2 / R3 的命中。
//
// **同步失败或从未采集时一条都不算**：那两种状态下 value_json 里装的是
// 上一次成功的旧值（见 jobs.Sub2APISyncWorker.failureObservation），拿它去判
// 「现在余额够不够」是在用过期数据下现在的结论。这两种情况 R1 已经以
// critical 报出「你现在是瞎的」，那才是此刻真正需要处理的事。
func (e *Evaluator) channelFindings(o ops.Observation, f ops.Freshness, environment string) []Finding {
	if f.State == ops.StateFailed || f.State == ops.StateUninitialized {
		return nil
	}
	var out []Finding
	for i, ch := range parseChannels(o.Value) {
		id := ch.identity(i)

		if ch.TokenValid != nil && !*ch.TokenValid {
			out = append(out, Finding{
				RuleKey:  RuleChannelTokenInvalid,
				DedupKey: dedupKey(RuleChannelTokenInvalid, environment, id),
				Severity: SeverityWarning,
				Title:    fmt.Sprintf("渠道 %s token 失效", ch.display(i)),
				Detail: fmt.Sprintf("上游报告 token_valid=false。数据时间 %s，来源 %s。",
					describeObservedAt(f.ObservedAt), o.Source),
				SourceMetricKey: o.MetricKey,
			})
		}

		// 余额缺失不告警：「上游没给这个字段」与「余额是 0」是两回事，
		// 把前者当成后者会天天误报（宪法 12 条同一条道理的反面）。
		// 契约缺字段是 Connector 的问题，该由契约测试拦，不该由告警冒充。
		if ch.HasBalance && ch.BalanceMinorUnits < e.cfg.BalanceThresholdMinorUnits {
			out = append(out, Finding{
				RuleKey:  RuleChannelBalanceLow,
				DedupKey: dedupKey(RuleChannelBalanceLow, environment, id),
				Severity: SeverityWarning,
				Title:    fmt.Sprintf("渠道 %s 余额不足", ch.display(i)),
				Detail: fmt.Sprintf("余额 %d 低于阈值 %d（均为最小货币单位%s）。数据时间 %s。",
					ch.BalanceMinorUnits, e.cfg.BalanceThresholdMinorUnits,
					describeCurrency(ch.Currency), describeObservedAt(f.ObservedAt)),
				SourceMetricKey: o.MetricKey,
			})
		}
	}
	return out
}

// consecutiveFailures 数一数最新那一串连续失败样本有多长。
//
// 样本按 (synced_at, id) 升序返回（见 ops.Store.ListSamples），所以从尾部
// 往前数：第一条非 failed 就结束。只取最新一串是关键——历史上任何一次成功
// 都会把连续串打断，那正是 R4「恢复条件」的定义。
func (e *Evaluator) consecutiveFailures(
	ctx context.Context, environment, metricKey string, now time.Time,
) (int, error) {
	limit := maxConsecutiveSamples
	if want := int32(e.cfg.ConsecutiveFailureThreshold); want > limit {
		limit = want
	}
	samples, _, err := e.source.ListSamples(ctx, environment, metricKey, now.Add(-consecutiveLookback), limit)
	if err != nil {
		return 0, fmt.Errorf("读取 %s 历史样本: %w", metricKey, err)
	}
	streak := 0
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].Status != ops.SyncFailed {
			break
		}
		streak++
	}
	return streak, nil
}

// dedupKey 拼出去重键。
//
// **environment 必须进键**：库层那条部分唯一索引只建在 dedup_key 一列上
// （见 000007 迁移），不带环境的话 staging 与生产的同一条指标会抢同一行——
// 一个环境的告警会被另一个环境的评估轮次改写，两边都得到错的答案。
func dedupKey(ruleKey, environment, subject string) string {
	return ruleKey + ":" + environment + ":" + subject
}

// channel 是从 value_json.channels[] 解出来的一个渠道条目。
type channel struct {
	ID                string
	Name              string
	Currency          string
	BalanceMinorUnits int64
	// HasBalance 区分「余额是 0」与「上游没给余额字段」。
	HasBalance bool
	// TokenValid 为 nil 表示上游没给这个字段——不默认当作有效，也不告警。
	TokenValid *bool
}

// identity 返回进去重键的稳定标识。
//
// channel_id 缺失时退回序号：那当然不稳定（上游重排顺序就会漂），但比让
// 所有匿名渠道挤进同一个去重键要好——后者会让第二个渠道的告警把第一个
// 悄悄覆盖掉。上游不给 ID 是契约问题，这里只保证不再叠加一层损失。
func (c channel) identity(index int) string {
	if c.ID != "" {
		return c.ID
	}
	return "#" + strconv.Itoa(index)
}

// display 返回给人看的名字。
func (c channel) display(index int) string {
	switch {
	case c.Name != "":
		return c.Name
	case c.ID != "":
		return c.ID
	default:
		return "#" + strconv.Itoa(index)
	}
}

// parseChannels 解出 value_json.channels[]。
//
// 结构对不上时返回空而不是报错：告警评估不该因为一条指标的形状变了就整轮
// 失败——那会连带让其它规则也不再评估。形状问题由 Connector 契约测试拦。
func parseChannels(value map[string]any) []channel {
	raw, ok := value["channels"].([]any)
	if !ok {
		return nil
	}
	out := make([]channel, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c := channel{
			ID:       stringField(m, "channel_id"),
			Name:     stringField(m, "channel_name"),
			Currency: stringField(m, "currency"),
		}
		if v, ok := asInt64(m["balance_minor_units"]); ok {
			c.BalanceMinorUnits = v
			c.HasBalance = true
		}
		if b, ok := m["token_valid"].(bool); ok {
			c.TokenValid = &b
		}
		out = append(out, c)
	}
	return out
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// asInt64 把 value_json 里的数字取成 int64。
//
// json.Number 分支是主路径：ops.Store 用 UseNumber 解码，正是为了让金额的
// minor units 不经 float64（见 ops.decodeValueJSON 的长注释——超过 2^53 的
// 整数经 float64 会静默丢精度）。这里必须顺着那条纪律走完最后一步：
// 用 Number.Int64() 从字面量直接解析，绝不走 Number.Float64()。
//
// float64 分支只为兼容「不经 ops.Store 解码」的调用方（单元测试直接构造
// map）。它同样拒绝有小数部分的值——余额的最小货币单位是整数，
// 一个 3.5 说明上游或调用方搞错了口径，比起猜一个值，不告警更诚实。
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}

func describeLastSuccess(t *time.Time) string {
	if t == nil {
		return "该指标从未成功采集过。"
	}
	return "最近一次成功采集 " + t.UTC().Format(time.RFC3339) + "。"
}

func describeObservedAt(t *time.Time) string {
	if t == nil {
		return "未知（从未成功采集）"
	}
	return t.UTC().Format(time.RFC3339)
}

func describeCurrency(currency string) string {
	if currency == "" {
		return ""
	}
	return "，" + currency
}
