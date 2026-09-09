package cards

// 一轮同步的结构化结果（XM-CARD-VISIBILITY）。
//
// 为什么需要它：RunOnce 此前只返回一个 error，于是「两个账号六个步骤里
// 有一步失败」与「全线崩了」在调用方看来一模一样——River 照 3 次重试烧完，
// 落一条 discarded，而卡状态其实一直在被兜底路径刷新。2026-09-08 的
// 24 小时里这样产生了 288 条 discarded 作业（见
// docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md 第 37 行）。
//
// 有了它，三件事才说得出口：哪个账号的哪一步失败了、失败的类别值不值得
// 重试、兜底路径这一轮救回了多少张卡。

import (
	"sort"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 步骤名。进结果、进日志、进告警的去重键，因此**只增不改**：
// 改名等于让历史告警与已存在的静默窗口一起失配。
const (
	StepReconcile = "reconcile"
	StepDiscover  = "discover"
	// StepBatchStatus 是批量查状态；StepBatchStatusFallback 是它失败之后
	// 「退回逐张查」的兜底。两者分开记是本片的重点之一：批量红了而兜底
	// 接住了，与两条都断了，是完全不同的两件事——前者不该叫人起床。
	StepBatchStatus         = "batch_status"
	StepBatchStatusFallback = "batch_status_fallback"
	StepFetchSecrets        = "fetch_secrets"
	StepTransactions        = "transactions"
	StepWithdrawals         = "withdrawals"
)

// StepOutcome 是「某个账号的某一步，这一轮的结果」。
type StepOutcome struct {
	// Account 为空表示这一步不是按账号做的（比如读台账本身失败了）。
	Account string
	Step    string
	OK      bool
	// Skipped 为真表示这一步**没有做**（账号被暂停），既不算成功也不算失败。
	Skipped bool
	// SkipReason 是给人看的中文说明，含运营填的暂停理由。
	SkipReason string
	// Kind 是失败的连接器分类；非连接器错误为 connector.KindInternal。
	Kind connector.ErrorKind
	// Detail 是**已脱敏**的失败简述（连接器的 Detail + 领域层的定位）。
	// 它会进日志、进 ops 观测的 value_json、进告警正文，所以绝不能装原始响应体。
	Detail string
	// Recovered 是这一步救回来的条目数，目前只有兜底逐张查在用。
	Recovered int
}

// RoundResult 是一轮同步的全部结果。
type RoundResult struct {
	Steps []StepOutcome
}

func (r *RoundResult) add(o StepOutcome) { r.Steps = append(r.Steps, o) }

// ok 记一次成功。
func (r *RoundResult) ok(account, step string) {
	r.add(StepOutcome{Account: account, Step: step, OK: true})
}

// fail 记一次失败，Detail 取**脱敏后的**连接器简述加上领域层的定位。
func (r *RoundResult) fail(account, step string, err error) {
	r.add(StepOutcome{
		Account: account,
		Step:    step,
		Kind:    connector.KindOf(err),
		Detail:  err.Error(),
	})
}

// skip 记一次「因为暂停而没做」。
func (r *RoundResult) skip(account, step, reason string) {
	r.add(StepOutcome{
		Account: account, Step: step, Skipped: true, SkipReason: reason,
	})
}

// stepTally 汇总一个步骤里**按账号**的成败，最后一次性写进 RoundResult。
//
// 为什么要汇总而不是每条错误各记一条：一个账号 200 张卡全失败会写出 200 条
// 一模一样的结果，把日志与 ops 观测的 value_json 撑爆，而告警要数的是
// 「这个账号这一步这一轮成没成」——200 和 1 是同一件事。
// 第一条错误保留下来做 Detail：同一步里后面的错误几乎必然同因。
type stepTally struct {
	step  string
	order []string
	state map[string]*stepState
}

type stepState struct {
	firstErr  error
	ok        int
	skipped   string
	recovered int
}

func newStepTally(step string) *stepTally {
	return &stepTally{step: step, state: make(map[string]*stepState)}
}

func (t *stepTally) at(account string) *stepState {
	st, ok := t.state[account]
	if !ok {
		st = &stepState{}
		t.state[account] = st
		t.order = append(t.order, account)
	}
	return st
}

func (t *stepTally) markOK(account string)      { t.at(account).ok++ }
func (t *stepTally) markSkip(account, r string) { t.at(account).skipped = r }

func (t *stepTally) markErr(account string, err error) {
	st := t.at(account)
	if st.firstErr == nil {
		st.firstErr = err
	}
}

func (t *stepTally) addRecovered(account string, n int) { t.at(account).recovered += n }

// flush 把汇总写进结果。
//
// 一个账号同时有成功与失败时记成**失败**：这一步对它没有干完，
// 而「干了一半」按成功记会让告警永远数不到连续失败。
func (t *stepTally) flush(res *RoundResult) {
	for _, account := range t.order {
		st := t.state[account]
		switch {
		case st.skipped != "":
			res.skip(account, t.step, st.skipped)
		case st.firstErr != nil:
			o := StepOutcome{
				Account: account, Step: t.step,
				Kind:   connector.KindOf(st.firstErr),
				Detail: st.firstErr.Error(),
			}
			o.Recovered = st.recovered
			res.add(o)
		default:
			res.add(StepOutcome{
				Account: account, Step: t.step, OK: true, Recovered: st.recovered,
			})
		}
	}
}

// AnySucceeded 报告这一轮有没有任何一件事真的做成了。
//
// 被跳过的步骤不算成功：它什么都没做。这一条很要紧——两个账号都被暂停时
// 整轮既不是成功也不是失败，而是「本来就没打算干活」。
func (r RoundResult) AnySucceeded() bool {
	for _, s := range r.Steps {
		if s.OK && !s.Skipped {
			return true
		}
	}
	return false
}

// Failures 返回全部失败的步骤（顺序即发生顺序）。
func (r RoundResult) Failures() []StepOutcome {
	var out []StepOutcome
	for _, s := range r.Steps {
		if !s.OK && !s.Skipped {
			out = append(out, s)
		}
	}
	return out
}

// AllFailed 报告「这一轮该做的都做砸了」。
//
// 判据刻意不是「有失败」而是「有失败且没有任何一件事做成」：
// 六步里砸了一步就把整轮判失败，正是此前那条把 288 个作业烧成 discarded 的规则。
func (r RoundResult) AllFailed() bool {
	return len(r.Failures()) > 0 && !r.AnySucceeded()
}

// Retryable 报告这一轮的失败里**还有没有值得再试的**。
//
// 判据是错误分类而不是失败次数：rejected 在全仓的定义就是「上游收下了但
// 业务上拒绝，重试没有意义」（connector/types.go）。此前没有任何一处代码
// 读过这个分类来决定重不重试，于是一条 rejected 也要烧满 3 次。
//
// 只要还有一件事值得再试，整轮就按可重试处理——宁可多跑一轮，
// 也不要因为同轮里另一个账号被拒而把一次真的网络抖动直接终结掉。
func (r RoundResult) Retryable() bool {
	failures := r.Failures()
	if len(failures) == 0 {
		return false
	}
	for _, f := range failures {
		if retryableKind(f.Kind) {
			return true
		}
	}
	return false
}

// retryableKind 把连接器分类分成「再试一次有没有意义」。
//
// 列的是**不值得重试**的那一侧并且显式枚举：默认可重试是安全的方向
// （多打一次上游 vs 把一次真故障静默终结），而新增一个分类时忘了登记
// 只会退化成「照旧重试」，不会退化成「静默放弃」。
func retryableKind(kind connector.ErrorKind) bool {
	switch kind {
	case connector.KindRejected,
		connector.KindAuth,
		connector.KindIPNotAllowed,
		connector.KindNotSupported,
		connector.KindForbiddenTarget,
		connector.KindWriteAttempt,
		connector.KindMethodNotAllowed:
		return false
	default:
		// unavailable / rate_limited / bad_response / internal 以及未来新增的
		// 分类都落这里：再试一次可能不同。
		return true
	}
}

// PausedAccountIDs 返回这一轮被跳过的账号（升序、去重）。
func (r RoundResult) PausedAccountIDs() []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range r.Steps {
		if s.Skipped && s.Account != "" && !seen[s.Account] {
			seen[s.Account] = true
			out = append(out, s.Account)
		}
	}
	sort.Strings(out)
	return out
}

// Summary 是给日志与 ops 观测用的扁平摘要。
//
// 形状刻意是「按账号分组的步骤清单」而不是一串错误文本：告警规则要按
// (账号, 步骤) 数连续失败轮数，一串文本没法数。
//
// value_json 会进 ops.metric_observation_sample 并被管理端读到，所以这里
// 放的 Detail 必须是**已经脱敏**的那一份（由 StepOutcome.Detail 保证）。
func (r RoundResult) Summary() map[string]any {
	type acc struct {
		steps []map[string]any
	}
	byAccount := make(map[string]*acc)
	var order []string
	for _, s := range r.Steps {
		account := s.Account
		if account == "" {
			account = "-"
		}
		if _, ok := byAccount[account]; !ok {
			byAccount[account] = &acc{}
			order = append(order, account)
		}
		step := map[string]any{
			"step":    s.Step,
			"ok":      s.OK,
			"skipped": s.Skipped,
		}
		if s.Kind != "" {
			step["kind"] = string(s.Kind)
		}
		if s.Detail != "" {
			step["detail"] = s.Detail
		}
		if s.SkipReason != "" {
			step["skip_reason"] = s.SkipReason
		}
		if s.Recovered > 0 {
			step["recovered"] = s.Recovered
		}
		byAccount[account].steps = append(byAccount[account].steps, step)
	}
	sort.Strings(order)

	accounts := make([]any, 0, len(order))
	for _, id := range order {
		accounts = append(accounts, map[string]any{
			"account": id,
			"steps":   byAccount[id].steps,
		})
	}
	return map[string]any{
		"account_count":  len(order),
		"accounts":       accounts,
		"all_failed":     r.AllFailed(),
		"any_succeeded":  r.AnySucceeded(),
		"failure_count":  len(r.Failures()),
		"paused_account": strings.Join(r.PausedAccountIDs(), ","),
	}
}
