package alerts

// cards.sync.failed：某个账号的某一步卡片同步连续 N 轮失败（XM-CARD-VISIBILITY）。
//
// 为什么要单独一条而不是复用 metric.sync.consecutive_failed：那条数的是
// **一条指标**的连续失败，而卡片同步现在对部分成功返回 nil、观测状态记 ok，
// 于是它一条都数不到。真正需要被看见的粒度是 (账号, 步骤)——
// 「LINFENG 的批量查状态连着 12 轮被拒」与「同步这条指标偶尔抖一下」
// 是两件事，混成一条会让前者永远被后者的沉默盖住。
//
// 本文件只放规则声明与命中判定；规则键常量 RuleCardSyncFailed 必须留在
// rules.go——web/apps/admin-web/src/lib/labels.reconcile.test.ts 只读那一个
// 文件抽 ^Rule[A-Z] 常量。把常量挪到这里会让那道门禁**看不见新规则从而恒绿**，
// 于是规则带着一个没有中文名、在静默下拉里选不到的键上线。
// 宁可让门禁红着（合并点已写进 handoff），也不要用文件位置把它骗绿。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// DefaultCardSyncMetricKey 是本规则读的那条观测。
//
// 写成字面量而不是 import internal/platform/jobs：jobs 已经 import alerts，
// 反向引用会成环。字面量重复的代价由 consistency_test.go 里的
// TestCardSyncMetricKeyMatchesJob 兜住——那个测试在外部测试包里，
// 同时看得见两边，任何一边改了键名都会当场失败（同
// DefaultApprovalQueueMetricKey 的既有做法）。
const DefaultCardSyncMetricKey = "cards.sync.status"

const (
	// cardSyncRecoverySamples 是**迟滞**：要连续几轮成功才算恢复。
	//
	// 取 2 而不是 1：一个还在 80% 失败率上的账号很容易撞上一轮走运，
	// 而 1 轮成功就清掉告警意味着这条告警会在开与关之间抖动，
	// 抖几次之后它就会被人整条静默掉——那才是真正把预警关掉的方式。
	cardSyncRecoverySamples = 2

	// cardSyncLookback / cardSyncMaxSamples 是一次回看的窗口与条数上限，
	// 与 R4 的 consecutiveLookback / maxConsecutiveSamples 同一量级：
	// 5 分钟一轮，24 小时最多 288 轮，取 20 条足以覆盖任何合理的阈值。
	cardSyncLookback         = 24 * time.Hour
	cardSyncMaxSamples int32 = 20
)

// cardSyncRules 返回本片新增的规则声明。
//
// Rules() 里以 append 的形式接上（见 rules.go 的合并点）：本文件由
// XM-CARD-VISIBILITY 拥有，而 rules.go 由 XM-OPS-TRUTH 并行持有，
// 声明放这里能把那个文件的改动压到两处边界。
func cardSyncRules(cfg RuleConfig) []Rule {
	return []Rule{
		{
			Key:    RuleCardSyncFailed,
			Title:  "卡片同步连续失败",
			Source: DefaultCardSyncMetricKey + " 的 value_json.accounts[].steps[]",
			Condition: fmt.Sprintf("同一账号的同一步骤连续 %d 轮失败（被暂停的账号不计）",
				cfg.ConsecutiveFailureThreshold),
			For:      time.Duration(cfg.ConsecutiveFailureThreshold) * cfg.CollectionInterval,
			Severity: SeverityCritical,
			Recovery: fmt.Sprintf("同一账号同一步骤连续 %d 轮成功，或该账号被暂停同步",
				cardSyncRecoverySamples),
			// 去重键带账号与步骤：两个账号同时坏掉要得到两条告警。
			// 只带账号的话，一个账号的两种失败会塌成一条，静默掉批量查状态
			// 的同时把拉明文的失败也一起静默了。
			DedupKey:      RuleCardSyncFailed + ":<environment>:<account>/<step>",
			Channels:      defaultChannels,
			SilencePolicy: silencePolicyText,
			Owner:         cfg.Owner,
		},
	}
}

// cardSyncStepKey 是告警的最小粒度：某个账号的某一步。
type cardSyncStepKey struct {
	account string
	step    string
}

// cardSyncStep 是一轮里某个 (账号, 步骤) 的结果。
type cardSyncStep struct {
	ok      bool
	skipped bool
	kind    string
	detail  string
}

// cardSyncFindings 算出 cards.sync.failed 的命中。
//
// 三条判据上的选择：
//
//  1. **每次评估都回看一次历史**，即便这一轮全绿。这条曾经反过来写（照抄
//     R4「当前没坏就不回看」的成本纪律），而那是个只在迟滞是死代码时才成立
//     的省法：迟滞的全部意义就是「这一轮成功了但还不算恢复」，而那种状态
//     只能从历史里看出来。当前这一轮全绿恰恰是**必须**回看的时刻。
//     代价是每个评估周期多一次 ListSamples——一次，不是每个 (账号,步骤)
//     一次，由 TestCardSyncFailedReadsSamplesOncePerRound 钉住。
//  2. **被暂停的账号一条都不报**。运营刚把一个坏账号停掉，紧接着收到一条
//     永远清不掉的告警，结果一定是把整条规则静默——那会连带丢掉其余账号的
//     信号。代价是「忘了恢复的暂停」看不见，由 Query 侧的 paused 徽标
//     与 paused_at 承担（两半缺一不可）。
//  3. **判据是「同一账号同一步骤」连续失败，不是「失败了 N 次」**。
//     批量查状态与拉明文交替失败是两个不同的毛病，凑不成一条连续串。
func (e *Evaluator) cardSyncFindings(
	ctx context.Context, o ops.Observation, environment string, now time.Time,
) ([]Finding, error) {
	paused := cardSyncPausedAccounts(o.Value)

	samples, _, err := e.source.ListSamples(
		ctx, environment, o.MetricKey, now.Add(-cardSyncLookback), cardSyncMaxSamples)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 历史样本: %w", o.MetricKey, err)
	}
	// 样本按 (synced_at, id) 升序返回（见 ops.Store.ListSamples）。
	// 当前这条观测正常情况下**本身就是最新的那条样本**（UpsertWithSample 在
	// 同一个事务里写最新态与样本），所以只在它确实更新时才补进去——
	// 无条件追加会把最近一轮数成两轮。
	rounds := make([]map[cardSyncStepKey]cardSyncStep, 0, len(samples)+1)
	for _, s := range samples {
		rounds = append(rounds, cardSyncSteps(s.Value))
	}
	if len(samples) == 0 || samples[len(samples)-1].SyncedAt.Before(o.SyncedAt) {
		rounds = append(rounds, cardSyncSteps(o.Value))
	}

	var out []Finding
	for _, key := range cardSyncKeysInWindow(rounds) {
		if paused[key.account] {
			continue
		}
		streak := cardSyncStreakOf(rounds, key)
		if !streak.open(e.cfg.ConsecutiveFailureThreshold) {
			continue
		}
		out = append(out, Finding{
			RuleKey:         RuleCardSyncFailed,
			DedupKey:        dedupKey(RuleCardSyncFailed, environment, key.account+"/"+key.step),
			Severity:        SeverityCritical,
			Title:           cardSyncTitle(key, streak),
			Detail:          cardSyncDetail(key, streak, e.cfg.ConsecutiveFailureThreshold),
			SourceMetricKey: o.MetricKey,
		})
	}
	return out, nil
}

// cardSyncKeysInWindow 收集窗口里出现过的所有 (账号, 步骤)，按字典序返回。
//
// 候选键取自**整个窗口**而不是「这一轮正在失败的那些」。后者是这条规则
// 第一版的写法，而它让迟滞成了死代码：只有这一轮正在失败的键才会去数连续串，
// 于是「失败 N 轮之后成功 1 轮」这种状态压根进不了循环，Reconciler 当轮就把
// 告警恢复掉了——声明上写着「要连续 2 轮成功才算恢复」，实际是 1 轮。
// 一个还在 80% 失败率上的账号撞上一轮走运就会让告警开-关-开地抖，
// 抖几次之后运维会把整条规则静默掉，那才是真正把预警关掉。
func cardSyncKeysInWindow(rounds []map[cardSyncStepKey]cardSyncStep) []cardSyncStepKey {
	seen := make(map[cardSyncStepKey]bool)
	var keys []cardSyncStepKey
	for _, round := range rounds {
		for key := range round {
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].account != keys[j].account {
			return keys[i].account < keys[j].account
		}
		return keys[i].step < keys[j].step
	})
	return keys
}

// cardSyncStreak 是一个 (账号, 步骤) 在窗口末尾的状态。
type cardSyncStreak struct {
	// failures 是恢复窗口之前的连续失败轮数。
	failures int
	// okTail 是末尾已经攒到的连续成功轮数；攒够 cardSyncRecoverySamples 才算恢复。
	okTail int
	// last 是最近一次失败那一轮的结果，用来取上游原话。
	last cardSyncStep
	// seen 为假表示窗口里根本没有这个键的失败。
	seen bool
}

// open 判断这条告警此刻是否该开着。
func (s cardSyncStreak) open(threshold int) bool {
	return s.seen && s.failures >= threshold && s.okTail < cardSyncRecoverySamples
}

// cardSyncStreakOf 数一个 (账号, 步骤) 的连续失败轮数与末尾的成功轮数。
//
// 迟滞在这里实现：末尾允许有最多 cardSyncRecoverySamples-1 轮成功而不算恢复，
// 攒够 cardSyncRecoverySamples 轮成功才把连续串清掉。
//
// **样本里没有这个 (账号, 步骤)** 一律当作串的终点，不当作成功也不当作失败：
// 中间那几轮可能是账号被暂停了，把暂停期两侧的失败接成一条长串，等于把
// 「停过又开」说成「一直在坏」。
func cardSyncStreakOf(rounds []map[cardSyncStepKey]cardSyncStep, key cardSyncStepKey) cardSyncStreak {
	i := len(rounds) - 1
	okRun := 0
	for ; i >= 0; i-- {
		step, present := rounds[i][key]
		if !present || step.skipped {
			return cardSyncStreak{}
		}
		if !step.ok {
			break
		}
		okRun++
		if okRun >= cardSyncRecoverySamples {
			return cardSyncStreak{}
		}
	}
	if i < 0 {
		// 窗口里全是成功（只是还没攒够恢复轮数）：没有失败串可言。
		return cardSyncStreak{okTail: okRun}
	}

	out := cardSyncStreak{okTail: okRun, last: rounds[i][key], seen: true}
	for ; i >= 0; i-- {
		step, present := rounds[i][key]
		if !present || step.skipped || step.ok {
			break
		}
		out.failures++
	}
	return out
}

func cardSyncTitle(key cardSyncStepKey, streak cardSyncStreak) string {
	if streak.okTail > 0 {
		return fmt.Sprintf("卡片同步失败：账号 %s 的%s连续 %d 轮（刚成功 %d 轮，未达恢复条件）",
			key.account, cardSyncStepLabel(key.step), streak.failures, streak.okTail)
	}
	return fmt.Sprintf("卡片同步失败：账号 %s 的%s连续 %d 轮",
		key.account, cardSyncStepLabel(key.step), streak.failures)
}

func cardSyncDetail(key cardSyncStepKey, streak cardSyncStreak, threshold int) string {
	if streak.okTail > 0 {
		return fmt.Sprintf(
			"账号 %s 的「%s」连续 %d 轮失败后刚成功 %d 轮，还差 %d 轮才算恢复。最近一次失败时上游答复：%s",
			key.account, cardSyncStepLabel(key.step), streak.failures, streak.okTail,
			cardSyncRecoverySamples-streak.okTail, cardSyncDetailOrPlaceholder(streak.last))
	}
	return fmt.Sprintf(
		"账号 %s 的「%s」已连续 %d 轮失败（阈值 %d 轮，需连续 %d 轮成功才算恢复）。最近一次上游答复：%s",
		key.account, cardSyncStepLabel(key.step), streak.failures,
		threshold, cardSyncRecoverySamples, cardSyncDetailOrPlaceholder(streak.last))
}

// cardSyncSteps 把一条观测的 value_json 解成 (账号, 步骤) → 结果。
//
// 解得**宽容**：字段缺失、类型对不上一律跳过那一条，不让整轮评估崩掉。
// 观测是从库里读回来的 jsonb，形状漂了的话该由写它的那一侧的测试抓住，
// 而不是在告警评估里 panic——那会把所有规则一起弄哑。
func cardSyncSteps(value map[string]any) map[cardSyncStepKey]cardSyncStep {
	out := make(map[cardSyncStepKey]cardSyncStep)
	for _, raw := range cardSyncAccountEntries(value) {
		account, _ := raw["account"].(string)
		if account == "" {
			continue
		}
		steps, _ := raw["steps"].([]any)
		for _, rawStep := range steps {
			m, ok := rawStep.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["step"].(string)
			if name == "" {
				continue
			}
			okFlag, _ := m["ok"].(bool)
			skipped, _ := m["skipped"].(bool)
			kind, _ := m["kind"].(string)
			detail, _ := m["detail"].(string)
			out[cardSyncStepKey{account: account, step: name}] = cardSyncStep{
				ok: okFlag, skipped: skipped, kind: kind, detail: detail,
			}
		}
	}
	return out
}

// cardSyncPausedAccounts 返回这一轮被暂停的账号。
func cardSyncPausedAccounts(value map[string]any) map[string]bool {
	out := make(map[string]bool)
	for _, raw := range cardSyncAccountEntries(value) {
		account, _ := raw["account"].(string)
		if account == "" {
			continue
		}
		steps, _ := raw["steps"].([]any)
		for _, rawStep := range steps {
			m, ok := rawStep.(map[string]any)
			if !ok {
				continue
			}
			if skipped, _ := m["skipped"].(bool); skipped {
				out[account] = true
				break
			}
		}
	}
	return out
}

func cardSyncAccountEntries(value map[string]any) []map[string]any {
	if value == nil {
		return nil
	}
	raw, ok := value["accounts"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// cardSyncStepLabel 把步骤名翻成中文。
//
// 认不出的步骤名原样返回**而不是写「未知步骤」**：一个刚加的步骤忘了翻译时，
// 运维至少还能看到它的英文名去代码里搜；写成「未知」就什么线索都没有了。
func cardSyncStepLabel(step string) string {
	switch step {
	case "reconcile":
		return "不确定态对账"
	case "discover":
		return "发现卡片"
	case "batch_status":
		return "批量查卡状态"
	case "batch_status_fallback":
		return "逐张查卡状态（批量失败后的兜底）"
	case "fetch_secrets":
		return "拉取卡面明文"
	case "transactions":
		return "同步流水"
	case "withdrawals":
		return "推进提现"
	case "account_sync_pause":
		return "读取账号同步开关"
	default:
		return step
	}
}

// cardSyncDetailOrPlaceholder 给告警正文取那句上游原话。
//
// **这是整个切片的收口**：连接器把上游的业务码与脱敏后的 message 带了出来，
// 同步作业把它记进了每一步的结果，观测把它带上了时间线，最后由这里送到
// 人眼前。少了这一段，这条告警就退回成「同步又失败了」——正是 2026-09-08
// 那三天里唯一能看到的信息。
func cardSyncDetailOrPlaceholder(step cardSyncStep) string {
	detail := strings.TrimSpace(step.detail)
	if detail == "" {
		if step.kind != "" {
			return "（观测里没有带上游答复，分类 " + step.kind + "）"
		}
		return "（观测里没有带上游答复）"
	}
	return detail
}
