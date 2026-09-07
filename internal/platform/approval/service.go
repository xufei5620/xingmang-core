package approval

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// ScopeRead 是查看审批队列所需的权限。
//
// 与 approval.decide 分开：能看见「有哪些单在等」的人应当远多于能投票的人——
// 提交人要能查自己的单的进度，值班的人要能看见积压。合成一个 scope 会逼着
// 把投票权发给所有需要旁观的人。
const ScopeRead = "approval.read"

// ErrParamsCorrupted：单上存的 params_json 与 params_hash 对不上。
//
// 这不是「调用方给错了参数」，是**库里那一行自己内部矛盾**——两列之中有一列被
// 改过而另一列没有。正常写路径不可能产生它（Create 同时写两列），所以出现即
// 视为篡改或数据损坏，一律拒绝执行。
var ErrParamsCorrupted = errors.New("stored approval params do not match the stored hash")

// Service 把 Store 与票数策略接成审批中心，并实现 action.ApprovalGateway。
//
// 它是**领域错误到 Action 错误码的唯一翻译处**：内核不该知道「单还没批」和
// 「单已过期」的区别，HTTP 层更不该。翻译放在这里，两边都只管透传。
type Service struct {
	store  Store
	policy Policy
	now    func() time.Time
}

func NewService(store Store, policy Policy, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: store, policy: policy, now: now}
}

var _ action.ApprovalGateway = (*Service)(nil)

// Submit 为一次被内核拦下的 L2+ 调用落一张待批单。
func (s *Service) Submit(ctx context.Context, in action.ApprovalSubmission) (string, error) {
	hash, err := HashParams(in.Params)
	if err != nil {
		return "", action.NewError(action.CodeInvalidParams, "参数无法规范化", err)
	}
	now := s.now().UTC()
	level := string(in.RiskLevel)
	// 未配 TTL 的等级由 TTLFor 退化到**最短**的那一档（不是最长，也不是无限）。
	// 配合 Settle 对未配票数的等级一律返回 PENDING，一个漏配的等级的结局是
	// 「落得下单、批不了、很快过期」——三处都朝安全的一侧倒。
	ttl := s.policy.TTLFor(level)
	req := Request{
		ID:            uuid.New(),
		ActionID:      in.ActionID,
		ActionVersion: in.ActionVersion,
		Params:        in.Params,
		ParamsHash:    hash,
		RiskLevel:     level,
		RequesterID:   in.Requester.ID,
		RequesterType: in.Requester.Type,
		Reason:        in.Reason,
		Status:        StatusPending,
		ExpiresAt:     now.Add(ttl),
		CreatedAt:     now,
	}
	if err := s.store.Create(ctx, req); err != nil {
		return "", action.NewError(action.CodeInternal, "审批单落库失败", err)
	}
	return req.ID.String(), nil
}

// Peek 只读地取出单上冻结的内容，顺带做一次**存储完整性校验**。
//
// 校验的是「params_json 重算出的哈希 == params_hash」。执行时用的参数取自单上，
// 所以「批准 A 执行 B」在结构上已经不成立；这道校验防的是另一件事——有人只改了
// 两列中的一列。它跟调用方给什么参数无关，因此放在只读路径上，每次都跑。
func (s *Service) Peek(ctx context.Context, approvalID string) (action.ApprovalClaim, error) {
	req, err := s.load(ctx, approvalID)
	if err != nil {
		return action.ApprovalClaim{}, err
	}
	hash, hashErr := HashParams(req.Params)
	if hashErr != nil {
		return action.ApprovalClaim{}, action.NewError(action.CodeInternal, "审批单参数无法规范化", hashErr)
	}
	if hash != req.ParamsHash {
		return action.ApprovalClaim{}, action.NewError(action.CodeConflict,
			"审批单参数与其哈希不一致，拒绝执行", ErrParamsCorrupted)
	}
	return action.ApprovalClaim{
		ActionID:      req.ActionID,
		ActionVersion: req.ActionVersion,
		RiskLevel:     action.RiskLevel(req.RiskLevel),
		Params:        req.Params,
		Reason:        req.Reason,
		RequesterID:   req.RequesterID,
	}, nil
}

// Claim 校验单此刻可执行并原子地占用它。
//
// params 非 nil 时额外比对调用方声明的参数——让客户端能证明自己执行的正是它
// 看到的那份。传 nil 表示「按单上的来」，此时 Peek 的完整性校验已经把住了。
func (s *Service) Claim(ctx context.Context, approvalID string, runID uuid.UUID, params map[string]any) error {
	req, err := s.load(ctx, approvalID)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	// 用单上冻结的参数走状态机校验（状态/过期/执行窗口）——这里传 req.Params
	// 而不是调用方给的，是因为 CanExecute 的哈希那一项在这个位置只应当校验
	// 存储自洽；调用方声明的那份单独比对，好让两类问题的错误码不同。
	if err := s.policy.CanExecute(req, req.Params, now); err != nil {
		return s.executeError(err)
	}
	if params != nil {
		hash, hashErr := HashParams(params)
		if hashErr != nil {
			return action.NewError(action.CodeInvalidParams, "参数无法规范化", hashErr)
		}
		if hash != req.ParamsHash {
			return action.NewError(action.CodeConflict,
				"执行参数与审批时冻结的不一致", ErrParamsDrifted)
		}
	}
	if err := s.store.MarkExecuted(ctx, req.ID, runID, now); err != nil {
		if errors.Is(err, ErrAlreadyExecuted) {
			// 走到这里说明 CanExecute 之后、MarkExecuted 之前有人抢先跑了。
			// 库层的 WHERE 条件是最终裁判，这个分支正是它存在的理由。
			return action.NewError(action.CodeConflict, "审批单已经执行过", err)
		}
		return action.NewError(action.CodeInternal, "占用审批单失败", err)
	}
	return nil
}

// Vote 记一票。approver 的特权与否由**投票那一刻持有的 scope** 决定并冻结在票上。
func (s *Service) Vote(ctx context.Context, approvalID string, approver principal.Principal, verdict Verdict, comment string) (Request, error) {
	id, err := parseID(approvalID)
	if err != nil {
		return Request{}, err
	}
	// 身份三项交给 Store 按 approver 覆写（见 Store.Vote 的注释），这里只给
	// 这一票自身的内容。
	d := Decision{ID: uuid.New(), RequestID: id, Verdict: verdict, Comment: comment}
	req, err := s.store.Vote(ctx, id, approver, d, s.policy)
	if err != nil {
		return Request{}, s.voteError(err)
	}
	return req, nil
}

// Cancel 撤回自己的单。
func (s *Service) Cancel(ctx context.Context, approvalID string, requester principal.Principal) error {
	id, err := parseID(approvalID)
	if err != nil {
		return err
	}
	if err := s.store.Cancel(ctx, id, requester.ID, s.now().UTC()); err != nil {
		if errors.Is(err, ErrNotCancellableByOther) {
			return action.NewError(action.CodePreconditionFailed,
				"只有提交人能撤回，且只能撤回还在等待中的单", err)
		}
		return action.NewError(action.CodeInternal, "撤回审批单失败", err)
	}
	return nil
}

// Get 取一张单（含票）。
func (s *Service) Get(ctx context.Context, approvalID string) (Request, error) {
	id, err := parseID(approvalID)
	if err != nil {
		return Request{}, err
	}
	req, err := s.store.Get(ctx, id)
	if err != nil {
		return Request{}, s.loadError(err)
	}
	return req, nil
}

// List 列出审批单。status 为空表示不过滤。
func (s *Service) List(ctx context.Context, status Status, limit int) ([]Request, error) {
	reqs, err := s.store.List(ctx, status, limit)
	if err != nil {
		return nil, action.NewError(action.CodeInternal, "读取审批队列失败", err)
	}
	return reqs, nil
}

// ExpirePending 把到期的 PENDING 单落 EXPIRED，返回条数。由定时任务调用。
func (s *Service) ExpirePending(ctx context.Context) (int64, error) {
	return s.store.ExpirePending(ctx, s.now().UTC())
}

// Policy 暴露当前策略，供 HTTP 层告诉前端「这张单还差几票」。
func (s *Service) Policy() Policy { return s.policy }

func (s *Service) load(ctx context.Context, approvalID string) (Request, error) {
	id, err := parseID(approvalID)
	if err != nil {
		return Request{}, err
	}
	req, err := s.store.Get(ctx, id)
	if err != nil {
		return Request{}, s.loadError(err)
	}
	return req, nil
}

func parseID(approvalID string) (uuid.UUID, error) {
	id, err := uuid.Parse(approvalID)
	if err != nil {
		// 形态不对与不存在给同一个错误码：区分开等于给了一个探测器，
		// 能用「格式对但不存在」和「格式就不对」的差别去猜单号空间。
		return uuid.Nil, action.NewError(action.CodeApprovalNotFound, "审批单不存在", err)
	}
	return id, nil
}

func (s *Service) loadError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return action.NewError(action.CodeApprovalNotFound, "审批单不存在", err)
	}
	return action.NewError(action.CodeInternal, "读取审批单失败", err)
}

// executeError 把执行前的状态判定翻译成 Action 错误码。
func (s *Service) executeError(err error) error {
	switch {
	case errors.Is(err, ErrAlreadyExecuted):
		return action.NewError(action.CodeConflict, "审批单已经执行过", err)
	case errors.Is(err, ErrNotApproved):
		return action.NewError(action.CodePreconditionFailed, "审批单尚未通过", err)
	case errors.Is(err, ErrExpired):
		return action.NewError(action.CodePreconditionFailed, "审批单已过期，请重新提交", err)
	case errors.Is(err, ErrParamsDrifted):
		return action.NewError(action.CodeConflict, "执行参数与审批时冻结的不一致", err)
	default:
		return action.NewError(action.CodeInternal, "审批单执行前校验失败", err)
	}
}

// voteError 把投票判定翻译成 Action 错误码。
func (s *Service) voteError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return action.NewError(action.CodeApprovalNotFound, "审批单不存在", err)
	case errors.Is(err, ErrApproverNotHuman):
		// 宪法 24 条。给 403 而不是 400：这不是请求写错了，是这个身份没有
		// 投票资格，且永远不会有。
		return action.NewError(action.CodePrincipalTypeNotAllowed,
			"审批决定必须由自然人做出", err)
	case errors.Is(err, ErrMissingDecideScope):
		return action.NewError(action.CodePermissionDenied, "缺少权限 "+ScopeDecide, err)
	case errors.Is(err, ErrApproverIsRequester):
		return action.NewError(action.CodePermissionDenied,
			"该风险等级不允许提交人给自己的单投票", err)
	case errors.Is(err, ErrDuplicateVote):
		return action.NewError(action.CodeConflict, "同一张单只能投一票", err)
	case errors.Is(err, ErrNotPending):
		return action.NewError(action.CodePreconditionFailed, "审批单已经落定，不再接受投票", err)
	case errors.Is(err, ErrExpired):
		return action.NewError(action.CodePreconditionFailed, "审批单已过期", err)
	default:
		return action.NewError(action.CodeInternal, "投票失败", err)
	}
}
