package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
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
	// RuleUpstreamVersionChanged：某个上游的自报版本变了（XM-UPSTREAM-VERSION-ALERT）。
	//
	// 2026-09-06 的教训：运营在 Sub2API 后台点了升级，上游二进制从 0.1.179 变成
	// 0.2.1，而平台、开票代理与运维手册里三处钉子仍然写着旧值。没有人被告知，
	// 直到几天后按手册抬钉子时四条采集流崩掉、readyz 503 两小时才发现。
	//
	// 版本变化本身不是故障，所以它是 warning 而不是 critical：它是一个**要去核对
	// 的信号**——桥接契约、连接器兼容矩阵、各处运行时钉子都要重新过一遍。真正的
	// 故障（如果有）会以别的规则报出来。
	RuleUpstreamVersionChanged = "upstream.version.changed"
	// RuleUpstreamRunwayLow：某个计量型上游的可用天数低于告警档（XM-0049）。
	//
	// UI 交接 §10.4 的最后一条要求：「低于阈值时进入告警和待处理队列」。
	// 前四条规则读的都是 ops 观测，这一条读的是 finance 的可用天数——
	// 它是本包第一条**不来自 ops.metric_observation** 的规则，理由见 RunwaySource。
	RuleUpstreamRunwayLow = "upstream.runway.low"
	// RuleApprovalPendingTooLong：有审批单挂了太久没人决定（XM-0030c）。
	//
	// 报的是**最久那一张**等了多久，不是队列有多长：二十张刚提交的单不是
	// 问题，一张挂了六小时的才是。审批是人对人的等待，没有「上游」可以责怪
	// ——这条告警的收件人就是该去点那两个按钮的人。
	//
	// warning 而不是 critical：一张单挂着不等于生产出事，它等于**一件本该
	// 发生的变更还没发生**。真正的故障会由别的规则报出来。
	RuleApprovalPendingTooLong = "approval.pending.too_long"
	// RuleCardSyncFailed：某个账号的某一步卡片同步连续 N 轮失败
	// （XM-CARD-VISIBILITY）。规则声明与判定在 rules_cards.go；
	// **常量必须留在本文件**——前端的 labels.reconcile.test.ts 只读
	// internal/platform/alerts/rules.go 抽 ^Rule[A-Z]，挪走会让那道门禁
	// 看不见新规则从而恒绿，规则就带着一个静默下拉里选不到的键上线。
	RuleCardSyncFailed = "cards.sync.failed"
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
	// DefaultApprovalPendingThreshold 是「挂太久」的门槛（设计稿 §4：PENDING 超 4h）。
	//
	// 4 小时是**半个工作日**：短于它会在午休和会议里误报，长于它就等不到
	// 当天处理。它同时短于 L4 单 4 小时的有效期——告警必须在单过期之前响，
	// 否则人赶到时那张单已经作废、只能让提交人重来一遍。
	DefaultApprovalPendingThreshold = 4 * time.Hour
	// DefaultApprovalQueueMetricKey 是审批规则读的那条观测。
	//
	// 与 DefaultChannelBalanceMetricKey 同一条纪律：写字面量而不是 import
	// jobs（那会造成 alerts ↔ jobs 的环），重复的代价由外部包里的一致性
	// 测试兜住——它同时看得见 jobs.MetricApprovalQueue 和这一行。
	DefaultApprovalQueueMetricKey = "platform.approval.queue"
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

// versionLookback 是 R6 往回找「最近一个不同版本」的窗口。
//
// 七天：一次上游升级要让人有整整一周看得见它，而不是几个小时。2026-09-06
// 那次，版本变了几天之后才在一次运维操作里被发现——窗口短于「有人休假回来」
// 就等于没有。
const versionLookback = 7 * 24 * time.Hour

// maxVersionSamples 是一次回看的样本数上限。
//
// 探测每 5 分钟一条，七天约 2000 条；这里只需要找到最近一个**不同**的版本，
// 而版本在一周里变一次都算多，所以 200 条（约 16 小时）之内几乎必然命中。
// 真的没命中就当没变化——宁可漏报一次陈年变更，也不要为了它把整段历史拖回来。
const maxVersionSamples int32 = 200

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
//
// RunwayThresholds 是静态兼容构造的初始值；生产 worker 通过
// NewEvaluatorWithThresholdProvider 注入 finance.RunwayThresholdProvider，
// 每轮从 DB 快照取值并覆盖该字段。这样 RuleConfig 的默认值不会成为 DB
// 缺行时的隐式运行时 fallback。
type RuleConfig struct {
	// CollectionInterval 是采集周期，R1b 的「2 采集周期」以它为单位。
	CollectionInterval time.Duration
	// BalanceThresholdMinorUnits 是渠道余额阈值（最小货币单位）。
	BalanceThresholdMinorUnits int64
	// ConsecutiveFailureThreshold 是 R4 的连续失败轮数阈值。
	ConsecutiveFailureThreshold int
	// ChannelBalanceMetricKey 是渠道规则读的聚合指标键。
	ChannelBalanceMetricKey string
	// RunwayThresholds 是可用天数的三档阈值（XM-0049）。
	//
	// 静态构造时它必须与 `/finance/upstreams/summary` 使用的档位完全相同；
	// 生产 provider 路径会在每轮读取同一个 DB revision，避免两个进程漂移。
	RunwayThresholds finance.RunwayThresholds
	// ApprovalPendingThreshold 是审批单「挂太久」的门槛（XM-0030c）。
	ApprovalPendingThreshold time.Duration
	// ApprovalQueueMetricKey 是审批规则读的观测键。
	ApprovalQueueMetricKey string
	// Owner 是这批规则的负责人（§9.3 要求每条规则都有）。
	Owner string
}

// DefaultRuleConfig 返回兼容构造使用的默认阈值。
// 生产 worker 仍须通过 provider 读取数据库快照，不能把此值当作 DB 缺行
// 时的运行时 fallback。
func DefaultRuleConfig() RuleConfig {
	return RuleConfig{
		CollectionInterval:          DefaultCollectionInterval,
		BalanceThresholdMinorUnits:  DefaultBalanceThresholdMinorUnits,
		ConsecutiveFailureThreshold: DefaultConsecutiveFailureThreshold,
		ChannelBalanceMetricKey:     DefaultChannelBalanceMetricKey,
		RunwayThresholds:            finance.DefaultRunwayThresholds(),
		ApprovalPendingThreshold:    DefaultApprovalPendingThreshold,
		ApprovalQueueMetricKey:      DefaultApprovalQueueMetricKey,
		Owner:                       "platform-ops",
	}
}

func (c RuleConfig) normalized() RuleConfig {
	// 该归一化只作用于静态兼容构造。DB provider 在 Evaluate 开始时先读取
	// 并校验快照，故不会把这里的默认值用于生产 runway 判档。
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
	if err := c.RunwayThresholds.Validate(); err != nil {
		// 未初始化或不递增的阈值会让 levelFor 的兜底把**每一条**上游判成
		// critical——一次配置手滑变成满屏红。回落到默认档而不是照单全收
		// （同 BalanceThresholdMinorUnits 的理由，只是失效方向相反：
		// 那个是永不触发，这个是永远触发）。
		c.RunwayThresholds = d.RunwayThresholds
	}
	if c.ApprovalPendingThreshold <= 0 {
		// 零与负都等于「立刻告警」（条件是 age >= threshold），也就是每一张
		// 刚提交的单都会当场响一次。回落到默认值——与余额阈值同一条道理，
		// 只是失效方向相反：那个是永不触发，这个是永远触发。
		c.ApprovalPendingThreshold = d.ApprovalPendingThreshold
	}
	if strings.TrimSpace(c.ApprovalQueueMetricKey) == "" {
		c.ApprovalQueueMetricKey = d.ApprovalQueueMetricKey
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
	// XM-CARD-VISIBILITY 的合并点之一：卡片同步规则接在末尾。
	// 必须进这个切片——RuleKeys() / KnownRuleKey() 都由它派生，而
	// KnownRuleKey 正是静默窗口的存在性校验；不在这里的规则**静默不了**。
	return append([]Rule{
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
			Key:   RuleUpstreamVersionChanged,
			Title: "上游版本变化",
			Source: "connector probe 观测（*.connector.health）里上游自报的 version，" +
				"与同一条指标的历史样本比较",
			Condition: "最新观测的上游版本与上一次观测到的版本不同",
			// 变化是一个瞬时事实，没有「持续多久」可言：第一次看见就该说。
			For:      0,
			Severity: SeverityWarning,
			Recovery: "下一轮评估里版本不再变化（同一条告警会随去重键里的新版本" +
				"停在那一次变化上，不会因为版本稳定下来就假装没发生过）",
			// 去重键带**新版本**：每一次不同的变化各自成一条告警。
			// 只带指标键的话，0.1.179→0.2.1 之后再 0.2.1→0.3.0 会复用同一行，
			// 而人已经确认过前一条了——第二次变化就被静悄悄吞掉。
			DedupKey:      RuleUpstreamVersionChanged + ":<environment>:<metric_key>:<new_version>",
			Channels:      defaultChannels,
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},

		{
			Key:   RuleUpstreamRunwayLow,
			Title: "上游可用天数不足",
			Source: "finance.balance_history（余额）÷ finance.profit_daily" +
				"（近 7 个完整业务日的日均消耗），见设计稿 §10.4",
			Condition: fmt.Sprintf(
				"计量型上游的可用天数算得出来且 ≤ %d 天（≤ %d 天升为 critical）",
				cfg.RunwayThresholds.WarningDays, cfg.RunwayThresholds.CriticalDays),
			For: 0,
			// 声明的是**进入告警的那一档**；实际严重度逐条按天数算
			// （Finding.Severity 才是落库的那个）。
			Severity: SeverityWarning,
			Recovery: "可用天数回到告警档之上，或该上游不再算得出天数" +
				"（余额读不到 / 无消耗 / 停用）",
			DedupKey:      RuleUpstreamRunwayLow + ":<environment>:<upstream_account_id>",
			Channels:      defaultChannels,
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},
		{
			Key:    RuleApprovalPendingTooLong,
			Title:  "审批单挂太久",
			Source: cfg.ApprovalQueueMetricKey + " 的 value_json.oldest_pending_age_seconds",
			Condition: fmt.Sprintf("最久那张未过期的 PENDING 审批单已等待 ≥ %s",
				cfg.ApprovalPendingThreshold),
			// For 是 0 而不是「持续多久」：观测本身就是一个持续量——
			// 「已经等了 4 小时」自带持续时间，再要求它持续几轮等于把门槛
			// 悄悄抬到 4 小时加几分钟。
			For:      0,
			Severity: SeverityWarning,
			Recovery: "最久那张降回门槛以内（有人批了或驳了），或队列清空",
			// 去重键的主体是**队列本身**（固定 "queue"），不带单号：一条
			// 「有人在等」就够了。带单号的话，一个没人看的队列会一张单一条
			// 告警刷屏，而它们说的是同一件事、要做的也是同一件事。
			DedupKey:      RuleApprovalPendingTooLong + ":<environment>:queue",
			Channels:      defaultChannels,
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
	}, cardSyncRules(cfg)...)
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

// RunwaySource 是可用天数规则用到的 finance 子集（*finance.SummaryStore 满足）。
//
// ⚠️ **本包第一次依赖一个领域包**（前四条规则只读 ops 观测）。值得说清为什么
// 这次没有沿用「字面量指标键 + 一致性测试」那条老路：
//
// 那条路解决的是「别让平台层绑死某一个 Connector」——上游是可换的。
// 可用天数不是从上游读来的数，它是**平台自己算出来的**（余额 ÷ 近 7 日日均消耗，
// 两侧都在平台的库里）。把它塞进一条 ops 指标再由本包解 JSON，
// 会多出一处「谁来算」与一处「怎么解」，而算它的代码本来就在 finance 里。
//
// 接口只声明一个方法，返回的也是最瘦的那个类型（不含收入/成本窗口）：
// 告警只关心「哪条上游快见底了」。finance 不 import alerts，没有环。
type RunwaySource interface {
	UpstreamRunways(
		ctx context.Context, environment string, thresholds finance.RunwayThresholds,
	) ([]finance.UpstreamRunway, error)
}

// Evaluator 按第一批规则评估某个环境的当前状态。
//
// 它**只算不写**：产出 Finding 清单，落库、去重、静默判定与自动恢复由
// Reconciler 负责（见 reconcile.go）。分开是为了让规则本身可以在没有库的
// 情况下被完整测试——规则是这个模块里最容易写错也最该被密集测试的部分。
type Evaluator struct {
	cfg        RuleConfig
	source     MetricSource
	runway     RunwaySource
	thresholds finance.RunwayThresholdProvider
	// lastThresholdMu protects the non-sensitive snapshot metadata exposed to
	// the worker log after a completed evaluation. The values are never used as
	// classifier input; each Evaluate call still holds its own local snapshot.
	lastThresholdMu       sync.RWMutex
	lastThresholdRevision int64
	lastThresholdSource   string
}

// NewEvaluator 创建无 DB provider 的静态兼容评估器。
//
// 两个来源都是**必填**：缺哪一个 Evaluate 都会报错，而不是静默少跑几条规则。
// 一条因为装配漏项而永远不响的告警规则，只会在真出事那天才被发现；
// 因此静态兼容构造仍会归一化非 runway 阈值，而 DB provider 错误则直接
// 让评估轮次失败闭合。
func NewEvaluator(source MetricSource, runway RunwaySource, cfg RuleConfig) *Evaluator {
	return &Evaluator{cfg: cfg.normalized(), source: source, runway: runway}
}

// NewEvaluatorWithThresholdProvider keeps the test-friendly constructor above
// while allowing production workers to read one DB snapshot per evaluation
// round. The provider is consulted before any runway findings are generated;
// provider errors fail the whole round closed.
func NewEvaluatorWithThresholdProvider(
	source MetricSource, runway RunwaySource, provider finance.RunwayThresholdProvider, cfg RuleConfig,
) *Evaluator {
	return &Evaluator{cfg: cfg.normalized(), source: source, runway: runway, thresholds: provider}
}

// Config 返回归一化后的静态兼容配置（供日志与测试打印实际生效值）。
// 当 evaluator 使用 threshold provider 时，Evaluate 返回前会以该轮 DB
// snapshot 覆盖 runway 档位；调用方应使用 finding detail 中的 revision。
func (e *Evaluator) Config() RuleConfig { return e.cfg }

// LastThresholdSnapshot reports the metadata consumed by the most recent
// successful Evaluate call. It intentionally returns only revision/source,
// never actor/reason or credentials, so operational logs can prove the DB
// snapshot was used without widening their disclosure surface.
func (e *Evaluator) LastThresholdSnapshot() (revision int64, source string, ok bool) {
	e.lastThresholdMu.RLock()
	defer e.lastThresholdMu.RUnlock()
	if e.lastThresholdRevision <= 0 || e.lastThresholdSource == "" {
		return 0, "", false
	}
	return e.lastThresholdRevision, e.lastThresholdSource, true
}

// Evaluate 跑一轮评估，返回此刻命中的全部规则。
//
// 返回顺序稳定（按 dedup_key 升序）：让日志、测试与 Handoff 里的清单可比对。
func (e *Evaluator) Evaluate(ctx context.Context, environment string, now time.Time) ([]Finding, error) {
	if e.source == nil {
		return nil, fmt.Errorf("alerts: evaluator 没有指标来源")
	}
	if e.runway == nil {
		return nil, fmt.Errorf("alerts: evaluator 没有可用天数来源")
	}
	if environment == "" {
		return nil, fmt.Errorf("environment: %w", ErrMissingField)
	}
	now = now.UTC()
	e.lastThresholdMu.Lock()
	e.lastThresholdRevision = 0
	e.lastThresholdSource = ""
	e.lastThresholdMu.Unlock()
	thresholds := e.cfg.RunwayThresholds
	var thresholdSnapshot *finance.RunwayThresholdSnapshot
	if e.thresholds != nil {
		snapshot, err := e.thresholds.Current(ctx, environment)
		if err != nil {
			return nil, fmt.Errorf("读取 runway 阈值快照: %w", err)
		}
		thresholds = snapshot.Thresholds
		thresholdSnapshot = &snapshot
		e.lastThresholdMu.Lock()
		e.lastThresholdRevision = snapshot.Revision
		e.lastThresholdSource = snapshot.Source
		e.lastThresholdMu.Unlock()
	}

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
		if o.MetricKey == e.cfg.ApprovalQueueMetricKey {
			if finding, ok := e.approvalFinding(o, f, environment); ok {
				findings = append(findings, finding)
			}
		}
		// XM-CARD-VISIBILITY 的合并点之一：卡片同步的按账号/步骤连续失败。
		if o.MetricKey == DefaultCardSyncMetricKey {
			cardFindings, cardErr := e.cardSyncFindings(ctx, o, environment, now)
			if cardErr != nil {
				return nil, cardErr
			}
			findings = append(findings, cardFindings...)
		}
		// R6：上游自报版本变了。判据是这条观测里**有没有 version**，不是它的
		// 指标键叫什么——将来多一个连接器探测，它自动就被覆盖，不必回来改这里。
		versionFinding, versionErr := e.versionChangeFinding(ctx, o, f, environment, now)
		if versionErr != nil {
			return nil, versionErr
		}
		if versionFinding != nil {
			findings = append(findings, *versionFinding)
		}
	}

	runwayFindings, err := e.runwayFindings(ctx, environment, thresholds, thresholdSnapshot)
	if err != nil {
		return nil, err
	}
	findings = append(findings, runwayFindings...)

	sort.Slice(findings, func(i, j int) bool { return findings[i].DedupKey < findings[j].DedupKey })
	return findings, nil
}

// runwayFindings 算 R5 的命中：可用天数低于告警档（§10.4 最后一条要求）。
//
// 三条判据上的选择：
//
//  1. **只看计量型上游**。订阅型的成本是固定摊销，可用天数对它没有意义
//     （§7 末段），ComputeRunway 对它直接返回 not_applicable。
//  2. **算不出天数的一律不告警**。「余额还没读到」是采集覆盖率的问题
//     （§7 的覆盖率边界，当前是常态），不是「快见底了」。把它报成告警，
//     每个环境一上来就是满屏红，然后这条规则就被静默掉了——
//     那才是真正把预警关掉的方式。
//  3. **严重度逐条按天数算**，而不是每档一条规则。两条规则的话，
//     一个 3 天的上游会同时命中「< 10」与「< 5」两条，
//     于是一个条件产出两条告警、要静默两次。
func (e *Evaluator) runwayFindings(ctx context.Context, environment string, thresholds finance.RunwayThresholds, snapshot *finance.RunwayThresholdSnapshot) ([]Finding, error) {
	items, err := e.runway.UpstreamRunways(ctx, environment, thresholds)
	if err != nil {
		return nil, fmt.Errorf("读取可用天数: %w", err)
	}

	var out []Finding
	for _, item := range items {
		if !item.AccessMethod.IsMetered() {
			continue
		}
		days := item.Runway.Days
		if days == nil {
			continue
		}
		level, classifyErr := thresholds.Classify(*days)
		if classifyErr != nil {
			return nil, fmt.Errorf("分类 runway 阈值: %w", classifyErr)
		}
		if level != finance.RunwayCritical && level != finance.RunwayWarning {
			continue
		}
		severity := SeverityWarning
		if level == finance.RunwayCritical {
			severity = SeverityCritical
		}
		detail := fmt.Sprintf(
			"按近 %d 个完整业务日的日均消耗估算（实到 %d 天）。告警档 ≤%d 天，critical 档 ≤%d 天。%s请及时充值，或核对上游余额读数是否还在更新。",
			item.Runway.WindowDays, item.Runway.CoveredDays,
			thresholds.WarningDays, thresholds.CriticalDays,
			describeObservedAt(item.Runway.BalanceObservedAt))
		if snapshot != nil {
			detail += fmt.Sprintf(" threshold_revision=%d; critical_days=%d; warning_days=%d; serious_days=%d",
				snapshot.Revision, thresholds.CriticalDays, thresholds.WarningDays, thresholds.SeriousDays)
		}
		out = append(out, Finding{
			RuleKey: RuleUpstreamRunwayLow,
			// 去重键含 upstream_account_id：同一环境下几条上游各自成一条告警，
			// 而同一条上游连续几轮命中只合并成一条。
			DedupKey: dedupKey(RuleUpstreamRunwayLow, environment, item.AccountID.String()),
			Severity: severity,
			Title:    fmt.Sprintf("上游 %s 可用天数仅剩 %d 天", item.Name, *days),
			Detail:   detail,
			// 可用天数不来自某一条 ops 指标，留空而不是编一个键——
			// 一个指向不存在指标的告警会让人点进去看到空白页。
			SourceMetricKey: "",
		})
	}
	return out, nil
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

// versionChangeUnknown 是版本读不出来时的占位。
//
// **读不出来不告警**：一条没有 version 字段的观测（探测失败、连接器还没实现
// Version、或者上游根本不报版本）不是「版本变了」。把缺失当成一次变化会在
// 每次探测失败时都响一遍，而那时候 R1 已经以 critical 说清「你现在是瞎的」。
const versionChangeUnknown = ""

// observedVersion 从观测值里取上游自报版本。
func observedVersion(value map[string]any) string {
	if value == nil {
		return versionChangeUnknown
	}
	raw, ok := value["version"].(string)
	if !ok {
		return versionChangeUnknown
	}
	return strings.TrimSpace(raw)
}

// observedSupported 报告连接器的兼容矩阵是否声明支持这个版本。
//
// 第二个返回值是「这条观测到底说没说」：没说与说了 false 是两回事，
// 前者只是探测没给这个字段，不该被写成「不支持」。
func observedSupported(value map[string]any) (bool, bool) {
	if value == nil {
		return false, false
	}
	supported, ok := value["supported"].(bool)
	return supported, ok
}

// versionChangeFinding 判断这条观测的上游版本是不是刚变过（R6）。
//
// 判据是「最新样本的版本」与「历史样本里最近一个**不同**的版本」不一致。
// 不跟「上一条样本」比：探测每轮都写一条样本，版本变化后第二轮起上一条就
// 已经是新版本了，那样这条告警只在变化后的一轮里存在，评估周期错开一次就
// 永远看不见。往回找最近一个不同值，则这条告警会一直在，直到人处理它——
// 这正是「要去核对」类信号该有的行为。
func (e *Evaluator) versionChangeFinding(
	ctx context.Context, o ops.Observation, f ops.Freshness, environment string, now time.Time,
) (*Finding, error) {
	// 同步失败时不看：value_json 里留的是上一次成功的旧值（见各 sync worker 的
	// failureObservation），拿它去判「版本刚变了」是在用过期数据下现在的结论。
	if f.State == ops.StateFailed || f.State == ops.StateUninitialized {
		return nil, nil
	}
	current := observedVersion(o.Value)
	if current == versionChangeUnknown {
		return nil, nil
	}
	samples, _, err := e.source.ListSamples(ctx, environment, o.MetricKey, now.Add(-versionLookback), maxVersionSamples)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 历史样本: %w", o.MetricKey, err)
	}
	previous := versionChangeUnknown
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].Status == ops.SyncFailed {
			continue
		}
		seen := observedVersion(samples[i].Value)
		if seen == versionChangeUnknown || seen == current {
			continue
		}
		previous = seen
		break
	}
	if previous == versionChangeUnknown {
		return nil, nil
	}

	detail := fmt.Sprintf("上游自报版本从 %s 变成 %s。数据时间 %s，来源 %s。",
		previous, current, describeObservedAt(f.ObservedAt), o.Source)
	if supported, said := observedSupported(o.Value); said && !supported {
		detail += "连接器的兼容矩阵没有声明支持这个版本——先核对桥接契约与各处运行时钉子再动生产。"
	} else {
		detail += "核对桥接契约、连接器兼容矩阵与各处运行时钉子是否仍然对得上。"
	}
	return &Finding{
		RuleKey:         RuleUpstreamVersionChanged,
		DedupKey:        dedupKey(RuleUpstreamVersionChanged, environment, o.MetricKey+":"+current),
		Severity:        SeverityWarning,
		Title:           fmt.Sprintf("%s 上游版本变化：%s → %s", o.Source, previous, current),
		Detail:          detail,
		SourceMetricKey: o.MetricKey,
	}, nil
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
		// Converting an out-of-range float64 to int64 is implementation
		// dependent. Check the half-open representable range before converting;
		// otherwise +Inf/2^63 can turn into MinInt64 and look like a valid
		// balance. math.Trunc also rejects fractional values without a lossy
		// round-trip through int64.
		const maxInt64Exclusive = float64(1 << 63)
		if n < -maxInt64Exclusive || n >= maxInt64Exclusive || n != math.Trunc(n) {
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

// approvalFinding 判「有没有人等太久」（XM-0030c 的 R8）。
//
// 三处讲究，都是**不误报**的分寸：
//
//  1. **failed / uninitialized 不判。** 观测失败时 value_json 里是上一轮的
//     数字，拿它算「等了多久」会报出一个早已不成立的事实。这一类由既有的
//     「指标同步失败」规则负责——那才是当下真正出问题的东西。
//  2. **队列为空或字段缺失时不判。** 判据是 `pending_count > 0`：缺字段解出
//     0，与空队列走同一条出口。这里**只留这一道**——曾经还有一道
//     `oldest_pending_age_seconds` 的缺失判断，但缺字段同样解出 0，而 0 低于
//     任何合法门槛（normalized 挡住了 0 与负数），那道判断永远不会改变结果。
//     一段读起来像保护、实际永不生效的代码比没有它更糟：它会让人不再去找
//     真正的保护在哪。
func (e *Evaluator) approvalFinding(o ops.Observation, f ops.Freshness, environment string) (Finding, bool) {
	if f.State == ops.StateFailed || f.State == ops.StateUninitialized {
		return Finding{}, false
	}
	pending, _ := numericField(o.Value, "pending_count")
	if pending <= 0 {
		return Finding{}, false
	}
	ageSeconds, _ := numericField(o.Value, "oldest_pending_age_seconds")
	age := time.Duration(ageSeconds) * time.Second
	if age < e.cfg.ApprovalPendingThreshold {
		return Finding{}, false
	}
	return Finding{
		RuleKey:  RuleApprovalPendingTooLong,
		DedupKey: dedupKey(RuleApprovalPendingTooLong, environment, "queue"),
		Severity: SeverityWarning,
		Title:    fmt.Sprintf("有审批单已等待 %s 无人决定", age.Round(time.Minute)),
		Detail: fmt.Sprintf(
			"队列里有 %d 张待审批，最久的一张已等待 %s（门槛 %s）。"+
				"处置见 docs/runbooks/APPROVAL-QUEUE.md：先确认审批人在不在，"+
				"再决定是批、是驳，还是让提交人撤回重提。数据时间 %s。",
			pending, age.Round(time.Minute), e.cfg.ApprovalPendingThreshold,
			describeObservedAt(f.ObservedAt)),
		SourceMetricKey: o.MetricKey,
	}, true
}

// numericField 从观测的 value_json 里取一个整数字段。
//
// 返回第二个值区分「没有这个字段」与「值是 0」——两者在告警里是完全不同的
// 事实（见 approvalFinding 的第 2 条）。JSON 解出来的数字可能是 float64、
// json.Number 或整型，逐个认。
func numericField(value map[string]any, key string) (int64, bool) {
	raw, ok := value[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return n, true
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
