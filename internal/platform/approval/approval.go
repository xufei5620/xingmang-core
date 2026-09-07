// Package approval 实现 Action Advanced Controls 的审批中心（XM-0030a）。
//
// Foundation-A 的内核对 L2 及以上一律返回 ADVANCED_CONTROLS_REQUIRED（fail
// closed）。这挡住了 Connector/Connection 登记、Kill Switch 拉闸，以及
// Foundation-B 的全部写场景——业务侧被迫把本该 L2/L3 的 Action 声明成 L1 绕开。
// 那批降级已由 XM-RISK-RESTORE 恢复（cards 的开卡/关停/提现、sms 的三个
// 采购动作），恢复的前提正是本包。
//
// 本包给内核补上「先批准、后执行」的通道，**不给任何绕过 Action 的口子**：
// 审批通过后仍然走内核原有的 Execute 全链，权限、环境、Schema 一项不少。
// 审批只是在执行之前多了一段可追溯的前置事件。
//
// 设计稿：docs/superpowers/plans/2026-08-27-xm-0030-action-advanced-controls-design.md
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Status 是审批单状态。终态是 REJECTED / EXECUTED / EXPIRED / CANCELLED。
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusApproved  Status = "APPROVED"
	StatusRejected  Status = "REJECTED"
	StatusExecuted  Status = "EXECUTED"
	StatusExpired   Status = "EXPIRED"
	StatusCancelled Status = "CANCELLED"
)

// Terminal 报告该状态是否已经落定——落定的单不再接受投票、撤回或执行。
func (s Status) Terminal() bool {
	return s == StatusRejected || s == StatusExecuted || s == StatusExpired || s == StatusCancelled
}

// Verdict 是一票的内容。
type Verdict string

const (
	VerdictApprove Verdict = "APPROVE"
	VerdictReject  Verdict = "REJECT"
)

// ScopeDecide 是投票所需的权限；ScopeL4 是 L4 的特权票。
//
// 特权票持有人映射进 RoleScopeMap，**不进 Keycloak**——审批权属于人的组织
// 决策，不该藏在身份提供方的角色树里（设计稿 §3）。
const (
	ScopeDecide = "approval.decide"
	ScopeL4     = "approval.l4"
)

var (
	// ErrNotPending：单已落定，不能再投票/撤回。
	ErrNotPending = errors.New("approval request is no longer pending")
	// ErrExpired：单已过期。过期的单一票也不能投，更不能执行。
	ErrExpired = errors.New("approval request has expired")
	// ErrApproverNotHuman 是**宪法 10 条的代码侧红线**：AI 不作为审批人。
	// 库层另有 CHECK (approver_type = 'HUMAN')；两道都在，因为库层只在写到
	// decision 表时生效，而这里要在更早的地方给出可读的错误。
	ErrApproverNotHuman = errors.New("approval decisions must come from a HUMAN principal")
	// ErrApproverIsRequester：L3 及以上不允许自批。
	ErrApproverIsRequester = errors.New("approver must differ from requester at this risk level")
	// ErrDuplicateVote：一人一单只能投一票。改主意要走驳回后重提。
	ErrDuplicateVote = errors.New("approver has already voted on this request")
	// ErrMissingDecideScope：缺少 approval.decide。
	ErrMissingDecideScope = errors.New("approver lacks the approval.decide scope")
	// ErrNotApproved：只有 APPROVED 的单能执行。
	ErrNotApproved = errors.New("approval request is not approved")
	// ErrParamsDrifted：执行时的参数与单上冻结的不一致。
	ErrParamsDrifted = errors.New("execution params differ from the approved params")
	// ErrAlreadyExecuted：一单最多一跑。
	ErrAlreadyExecuted = errors.New("approval request has already been executed")
	// ErrNotCancellableByOther：只有提交人能撤回自己的单。
	ErrNotCancellableByOther = errors.New("only the requester may cancel the request")
)

// Request 是一张审批单。参数在提交那一刻冻结。
type Request struct {
	ID             uuid.UUID
	ActionID       string
	ActionVersion  string
	Params         map[string]any
	ParamsHash     string
	RiskLevel      string
	Environment    string
	RequesterID    string
	RequesterType  principal.Type
	Reason         string
	Status         Status
	ExpiresAt      time.Time
	CreatedAt      time.Time
	DecidedAt      *time.Time
	ExecutedAt     *time.Time
	ExecutionRunID *uuid.UUID
	Decisions      []Decision
}

// Decision 是一票。
type Decision struct {
	ID           uuid.UUID
	RequestID    uuid.UUID
	ApproverID   string
	ApproverType principal.Type
	Verdict      Verdict
	Comment      string
	// Privileged 记录投票那一刻此人是否持 approval.l4。
	//
	// 冻结成事实而不是执行时再查：权限会变动，而「这一票当时算不算特权票」
	// 是审计事实，不能被日后的授权变更改写。一个人今天被收回 approval.l4，
	// 不该让他昨天投出的那张特权票追溯失效——那张票当时是有效的。
	Privileged bool
	CreatedAt  time.Time
}

// Policy 是审批规则。**这四项正是设计稿 §7 待产品负责人拍板的问题**，所以它们
// 是配置而不是写死的常量：负责人日后给出裁定，改 Policy 即可，不必改代码，也
// 不会让已经建好的机制返工。DefaultPolicy 用的是设计稿自己给的建议默认值。
type Policy struct {
	// VotesRequired 是每个等级需要的票数。
	VotesRequired map[string]int
	// SelfApprovalAllowed 说明该等级是否允许提交人给自己的单投票。
	// 设计稿建议：L2 可以（单票即自批，等级本意如此）；L3+ 不可。
	SelfApprovalAllowed map[string]bool
	// PrivilegedVoteRequired 说明该等级是否需要至少一名 approval.l4 持有人。
	PrivilegedVoteRequired map[string]bool
	// TTL 是审批单有效期，按等级。过期自动 EXPIRED，防陈年审批单复活。
	TTL map[string]time.Duration
	// ExecutionWindow 是批准之后必须执行的时限；超时回 EXPIRED。
	// 批准的是「此刻做这件事」，隔了太久环境已经不是当初那个环境。
	ExecutionWindow time.Duration
}

// DefaultPolicy 是设计稿 §7 每一行的「建议默认」。
//
// 待产品负责人拍板的四项（登记在晨间清单）：
//  1. L3/L4 票数与 approval.l4 持有人 —— 这里取 L3=2 票、L4=2 票含 1 特权票；
//  2. 审批单有效期 —— 这里取 24h，L4 缩短到 4h（设计稿留的问号，取更严的一侧）；
//  3. 提交人可否自批 —— 这里取 L2 可、L3+ 不可；
//  4. APPROVED 后的执行窗口 —— 这里取 24h。
func DefaultPolicy() Policy {
	return Policy{
		VotesRequired:          map[string]int{"L2": 1, "L3": 2, "L4": 2},
		SelfApprovalAllowed:    map[string]bool{"L2": true, "L3": false, "L4": false},
		PrivilegedVoteRequired: map[string]bool{"L2": false, "L3": false, "L4": true},
		TTL: map[string]time.Duration{
			"L2": 24 * time.Hour,
			"L3": 24 * time.Hour,
			"L4": 4 * time.Hour,
		},
		ExecutionWindow: 24 * time.Hour,
	}
}

// HashParams 是 params_hash 的唯一定义：SHA256(canonical JSON)。
//
// 用 encoding/json 对 map[string]any 编码——Go 的 json 包对 map 的键**保证按
// 字典序输出**，所以同一份参数总是同一个哈希，不需要另写一套规范化。执行时
// 内核重算并与单上比对：审批过的是这份参数，不是这个意图的任意版本。
func HashParams(params map[string]any) (string, error) {
	if params == nil {
		params = map[string]any{}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("canonicalize approval params: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// CanVote 判断一个身份能不能给这张单投票，以及为什么不能。
//
// 顺序有讲究：先查身份类别（宪法红线），再查权限，再查自批限制，最后查重复
// 投票。这样 AI 拿到的永远是「AI 不能投票」，而不是「你缺 approval.decide」
// ——后者会让人以为补个 scope 就行。
func (p Policy) CanVote(req Request, approver principal.Principal, now time.Time) error {
	if approver.Type != principal.TypeHuman {
		return ErrApproverNotHuman
	}
	if req.Status != StatusPending {
		return ErrNotPending
	}
	if !now.Before(req.ExpiresAt) {
		return ErrExpired
	}
	if !approver.HasScope(ScopeDecide) {
		return ErrMissingDecideScope
	}
	if approver.ID == req.RequesterID && !p.SelfApprovalAllowed[req.RiskLevel] {
		return ErrApproverIsRequester
	}
	for _, d := range req.Decisions {
		if d.ApproverID == approver.ID {
			return ErrDuplicateVote
		}
	}
	return nil
}

// Settle 计算加上这一票之后单该处于什么状态。
//
// 一票 REJECT 即驳回——审批是「所有人都同意」而不是「多数同意」：任何一个
// 审批人看出问题，这件事就不该做。
func (p Policy) Settle(req Request, decisions []Decision) Status {
	approvals := 0
	privileged := false
	for _, d := range decisions {
		if d.Verdict == VerdictReject {
			return StatusRejected
		}
		approvals++
		if d.Privileged {
			privileged = true
		}
	}
	need := p.VotesRequired[req.RiskLevel]
	if need <= 0 {
		// 未知等级不放行。等级表演进时新增的等级若忘了配票数，宁可卡住
		// 也不能默认零票通过。
		return StatusPending
	}
	if approvals < need {
		return StatusPending
	}
	if p.PrivilegedVoteRequired[req.RiskLevel] && !privileged {
		return StatusPending
	}
	return StatusApproved
}

// TTLFor 返回该等级的审批单有效期；未配置的等级回退到最严的一档。
func (p Policy) TTLFor(riskLevel string) time.Duration {
	if ttl, ok := p.TTL[riskLevel]; ok && ttl > 0 {
		return ttl
	}
	shortest := time.Duration(0)
	for _, ttl := range p.TTL {
		if ttl > 0 && (shortest == 0 || ttl < shortest) {
			shortest = ttl
		}
	}
	if shortest == 0 {
		return time.Hour
	}
	return shortest
}

// CanExecute 判断一张单此刻能不能执行，以及为什么不能。
//
// params 是**执行时**调用方给出的参数：内核重算哈希与单上冻结的比对，不一致
// 即拒。这道校验不能省——否则「批准一件小事、执行一件大事」就成立了。
func (p Policy) CanExecute(req Request, params map[string]any, now time.Time) error {
	if req.Status == StatusExecuted || req.ExecutionRunID != nil {
		return ErrAlreadyExecuted
	}
	if req.Status != StatusApproved {
		return ErrNotApproved
	}
	if !now.Before(req.ExpiresAt) {
		return ErrExpired
	}
	if req.DecidedAt != nil && p.ExecutionWindow > 0 &&
		!now.Before(req.DecidedAt.Add(p.ExecutionWindow)) {
		return ErrExpired
	}
	hash, err := HashParams(params)
	if err != nil {
		return err
	}
	if hash != req.ParamsHash {
		return ErrParamsDrifted
	}
	return nil
}
