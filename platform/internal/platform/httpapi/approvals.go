package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// ApprovalService 是 HTTP 层依赖的最小审批能力（*approval.Service 满足）。
type ApprovalService interface {
	Get(ctx context.Context, approvalID string) (approval.Request, error)
	List(ctx context.Context, status approval.Status, limit int) ([]approval.Request, error)
	Vote(ctx context.Context, approvalID string, approver principal.Principal,
		verdict approval.Verdict, comment string) (approval.Request, error)
	Cancel(ctx context.Context, approvalID string, requester principal.Principal) error
	Policy() approval.Policy
}

// ApprovalExecutor 触发一张已批准的单（*action.Kernel 满足）。
type ApprovalExecutor interface {
	ExecuteApproved(ctx context.Context, approvalID, requestID string, params map[string]any) (action.Result, error)
}

// defaultApprovalLimit / maxApprovalLimit：审批队列的取数上限。
//
// 上限压得低是有代价意识的：Store.List 为了带上每张单的票，对每个 id 单独取一次
// （单页 50 条 = 51 次查询）。审批队列本就该是个位数到几十条的量级——真到了两百条
// 说明有人不看单了，那是要告警的信号，不是要翻页的信号。
const (
	defaultApprovalLimit = 50
	maxApprovalLimit     = 100
)

type approvalDecisionDTO struct {
	ApproverID   string    `json:"approver_id"`
	ApproverType string    `json:"approver_type"`
	Verdict      string    `json:"verdict"`
	Comment      string    `json:"comment,omitempty"`
	Privileged   bool      `json:"privileged"`
	CreatedAt    time.Time `json:"created_at"`
}

type approvalDTO struct {
	ID            string                `json:"id"`
	ActionID      string                `json:"action_id"`
	ActionVersion string                `json:"action_version"`
	RiskLevel     string                `json:"risk_level"`
	Params        map[string]any        `json:"params"`
	ParamsHash    string                `json:"params_hash"`
	RequesterID   string                `json:"requester_id"`
	RequesterType string                `json:"requester_type"`
	Reason        string                `json:"reason"`
	Status        string                `json:"status"`
	CreatedAt     time.Time             `json:"created_at"`
	ExpiresAt     time.Time             `json:"expires_at"`
	DecidedAt     *time.Time            `json:"decided_at,omitempty"`
	ExecutedAt    *time.Time            `json:"executed_at,omitempty"`
	ActionRunID   string                `json:"action_run_id,omitempty"`
	Decisions     []approvalDecisionDTO `json:"decisions"`
	// VotesRequired / VotesCast 让界面能说「还差一票」而不是让人自己数。
	VotesRequired int `json:"votes_required"`
	VotesCast     int `json:"votes_cast"`
	// PrivilegedVoteRequired 为真且尚未收到特权票时，界面要说清楚「票数够了但
	// 还缺一张特权票」——否则「两票齐了却还没批」看起来像故障。
	PrivilegedVoteRequired bool `json:"privileged_vote_required"`
	PrivilegedVoteCast     bool `json:"privileged_vote_cast"`
}

func toApprovalDTO(req approval.Request, p approval.Policy) approvalDTO {
	dto := approvalDTO{
		ID: req.ID.String(), ActionID: req.ActionID, ActionVersion: req.ActionVersion,
		RiskLevel: req.RiskLevel, Params: req.Params, ParamsHash: req.ParamsHash,
		RequesterID: req.RequesterID, RequesterType: string(req.RequesterType),
		Reason: req.Reason, Status: string(req.Status),
		CreatedAt: req.CreatedAt, ExpiresAt: req.ExpiresAt,
		DecidedAt: req.DecidedAt, ExecutedAt: req.ExecutedAt,
		Decisions:              make([]approvalDecisionDTO, 0, len(req.Decisions)),
		VotesRequired:          p.VotesRequired[req.RiskLevel],
		PrivilegedVoteRequired: p.PrivilegedVoteRequired[req.RiskLevel],
	}
	if dto.Params == nil {
		dto.Params = map[string]any{}
	}
	if req.ExecutionRunID != nil {
		dto.ActionRunID = req.ExecutionRunID.String()
	}
	for _, d := range req.Decisions {
		if d.Verdict == approval.VerdictApprove {
			dto.VotesCast++
			if d.Privileged {
				dto.PrivilegedVoteCast = true
			}
		}
		dto.Decisions = append(dto.Decisions, approvalDecisionDTO{
			ApproverID: d.ApproverID, ApproverType: string(d.ApproverType),
			Verdict: string(d.Verdict), Comment: d.Comment,
			Privileged: d.Privileged, CreatedAt: d.CreatedAt,
		})
	}
	return dto
}

// ListApprovalsHandler 返回审批队列。
func ListApprovalsHandler(svc ApprovalService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := parseApprovalStatus(r.URL.Query().Get("status"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseApprovalLimit(r.URL.Query().Get("limit"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		reqs, err := svc.List(r.Context(), status, limit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		policy := svc.Policy()
		items := make([]approvalDTO, 0, len(reqs))
		for _, req := range reqs {
			items = append(items, toApprovalDTO(req, policy))
		}
		// truncated 让界面能说出「这一屏可能不是全部」。不说的话，一张看起来
		// 空空如也的队列会让人以为没有待办（同 XM-WORKBENCH-TRUNCATION 的教训）。
		WriteJSON(w, http.StatusOK, map[string]any{
			"items":     items,
			"limit":     limit,
			"truncated": len(items) >= limit,
		})
	}
}

// GetApprovalHandler 返回一张单的详情（含每一票）。
func GetApprovalHandler(svc ApprovalService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := svc.Get(r.Context(), chi.URLParam(r, "approvalID"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, toApprovalDTO(req, svc.Policy()))
	}
}

type decideApprovalBody struct {
	Verdict string `json:"verdict"`
	Comment string `json:"comment"`
}

// DecideApprovalHandler 投一票。
//
// 本 handler **不判断投票资格**——是不是自然人、有没有 approval.decide、是不是
// 提交人本人、是不是投过了，全部由领域层裁决（与 Action 内核同一个分工）。
// 这里只做协议转换。
func DecideApprovalHandler(svc ApprovalService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body decideApprovalBody
		dec := json.NewDecoder(io.LimitReader(r.Body, maxActionBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "请求体不是合法 JSON 对象", err))
			return
		}
		var verdict approval.Verdict
		switch body.Verdict {
		case string(approval.VerdictApprove):
			verdict = approval.VerdictApprove
		case string(approval.VerdictReject):
			verdict = approval.VerdictReject
		default:
			WriteError(w, r, action.NewError(action.CodeInvalidParams,
				"verdict 必须是 APPROVE 或 REJECT", nil))
			return
		}
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		req, err := svc.Vote(r.Context(), chi.URLParam(r, "approvalID"), p, verdict, body.Comment)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, toApprovalDTO(req, svc.Policy()))
	}
}

// CancelApprovalHandler 撤回自己的单。
func CancelApprovalHandler(svc ApprovalService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		id := chi.URLParam(r, "approvalID")
		if err := svc.Cancel(r.Context(), id, p); err != nil {
			WriteError(w, r, err)
			return
		}
		req, err := svc.Get(r.Context(), id)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, toApprovalDTO(req, svc.Policy()))
	}
}

type executeApprovalBody struct {
	// Params 可选：给了就必须与审批时冻结的一致，让客户端能证明自己执行的
	// 正是它看到的那份。不给表示「按单上的来」——执行用的参数**始终**取自
	// 单上，这个字段不会改变要跑什么。
	Params map[string]any `json:"params"`
}

// ExecuteApprovalHandler 触发一张已批准的单。
//
// 路由上**不挂 RequireScope**：要什么权限取决于单上那个 Action 要什么权限，
// 而那只有内核知道（它按 def.Permission 判）。在这里再挂一道只会挂错——要么
// 太松形同虚设，要么太紧把有权跑该 Action 的人挡在外面。
func ExecuteApprovalHandler(exec ApprovalExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body executeApprovalBody
		if r.ContentLength != 0 {
			dec := json.NewDecoder(io.LimitReader(r.Body, maxActionBodyBytes))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&body); err != nil {
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "请求体不是合法 JSON 对象", err))
				return
			}
		}
		res, err := exec.ExecuteApproved(r.Context(), chi.URLParam(r, "approvalID"),
			RequestIDFrom(r.Context()), body.Params)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, executeActionResponse{
			ActionRunID: res.RunID.String(), Result: res.Value,
		})
	}
}

func parseApprovalStatus(raw string) (approval.Status, error) {
	switch raw {
	case "":
		return "", nil
	case string(approval.StatusPending), string(approval.StatusApproved),
		string(approval.StatusRejected), string(approval.StatusExecuted),
		string(approval.StatusExpired), string(approval.StatusCancelled):
		return approval.Status(raw), nil
	default:
		// 不认识的状态**拒绝**而不是当作「不过滤」：静默忽略一个拼错的过滤条件，
		// 会让人看着一屏全量数据以为那就是筛选结果。
		return "", action.NewError(action.CodeInvalidParams,
			"status 必须是 PENDING / APPROVED / REJECTED / EXECUTED / EXPIRED / CANCELLED 之一", nil)
	}
}

func parseApprovalLimit(raw string) (int, error) {
	if raw == "" {
		return defaultApprovalLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须是正整数", err)
	}
	if n > maxApprovalLimit {
		return 0, action.NewError(action.CodeInvalidParams,
			"limit 不得超过 "+strconv.Itoa(maxApprovalLimit), nil)
	}
	return n, nil
}
