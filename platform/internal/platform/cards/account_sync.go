package cards

// 按账号暂停卡片同步（XM-CARD-VISIBILITY 第 3 项）。
//
// **为什么进后台而不是环境变量**：这是运营在出事当下要改的开关——某个账号
// 被上游拒到底、或者正在上游后台做维护，此刻要能立刻把它摘出同步循环。
// 做成环境变量就意味着改一次要重启 worker，而重启 worker 恰好是事故当中
// 最不该做的动作。env 只留「会不会花真钱」那一类（XM_CARDS_MODE 的
// off/fake/real）；运营会调的开关一律进表 + 进 Action。
//
// 四件事缺一不可（记忆「配置归属：后台还是环境变量」）：表（迁移 000055）、
// 写路径的 Action（本文件）、读路径的 Query（httpapi/cards.go 的
// paused_accounts）、以及同步作业真的读它（sync.go）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Action 稳定 ID。
const (
	// ActionAccountSyncPause 暂停某个账号的卡片同步。
	ActionAccountSyncPause = "cards.account_sync.pause"
	// ActionAccountSyncResume 恢复某个账号的卡片同步。
	ActionAccountSyncResume = "cards.account_sync.resume"
)

// resourceCardAccount 是审计里的资源类型：这两个动作作用于**账号**，
// 不是某一张卡，所以不能沿用 resourceCard——那会让审计上「改了哪张卡」
// 指向一个不存在的卡 id。
const resourceCardAccount = "cards.account_sync_pause"

// AccountSyncPause 是一条按账号的同步暂停记录。
//
// 行缺席 = 未暂停。Paused 仍然显式存布尔而不是靠行的存在与否表达：
// resume 之后要保留 reason 与时间戳，供事后回答「上次为什么停过、停了多久」。
type AccountSyncPause struct {
	Account string
	Paused  bool
	// Reason 是运营填的自由文本，必填（Schema 强制）。
	Reason string
	// PausedBy 是 Principal id。
	PausedBy string
	// PausedAt 是最近一次状态变更时刻（UTC）。
	PausedAt time.Time
	// ExpiresAt 预留给「到点自动恢复」，本片不实现，恒为零值。
	//
	// 留列的理由见迁移 000055：自动恢复会在没人看着的时候把同步重新打向一个
	// 仍然拒绝我们的上游，而这个切片存在的意义正是不要让失败反复且无人知晓；
	// 但列先建好，将来真要做时不必再来一次迁移。
	ExpiresAt time.Time
}

// pauseSkipReason 是同步结果里给人看的那句话。
//
// 用户可见的新状态必须有中文文案，而且要带上运营当初填的理由——
// 只写「已暂停」的话，下一个人看到这一行仍然要去翻审计才知道为什么。
func pauseSkipReason(p AccountSyncPause) string {
	reason := strings.TrimSpace(p.Reason)
	if reason == "" {
		return "该账号的同步已暂停"
	}
	return "该账号的同步已暂停：" + reason
}

// accountSyncPauseDef 是暂停的声明。
//
// **风险等级 L1，且不要抬到 L2**。看到「暂停一条生产同步」第一反应是往上抬，
// 但三条理由都指向 L1：
//
//  1. 先例：assurance.probe.kill_switch.set 是同一个形状（周期探测的开关），
//     它就是 L1（internal/platform/assurance/actions.go）。
//  2. 机制：L2 及以上会把 req.Params 冻进 ApprovalSubmission 交给第二个人看
//     （action/kernel.go）。reason 是运营手打的自由文本——抬级正是「粘贴进来的
//     凭据被冻进审批单给人看」的那条路径（记忆「抬风险等级会泄漏凭据」）。
//     L1 时内核不持久化任何 params。
//  3. 语义：需要审批的暂停开关是自相矛盾的——人去够它的时刻正是「现在就出事了」。
//
// 减掉的控制一道都没有：card.manage 权限、仅人类身份、reason 必填、全程审计。
func accountSyncPauseDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionAccountSyncPause, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{
			Fields: []action.Field{
				accountField(accounts),
				// reason 必填。它不是装饰：暂停会让这个账号的卡片数据按设计
				// 停止刷新（宪法 12 条的新鲜度由 paused 徽标交代），
				// 而「为什么停」是下一个接手的人唯一能依据的东西。
				//
				// 用 Schema 字段而不是内核的 req.Reason：后者在 L2 以下是选填的
				// （action/kernel.go），而这里必须是硬性的。
				{Name: "reason", Type: action.FieldString, Required: true},
			},
		},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// accountSyncResumeDef 是恢复的声明。不要 reason：恢复是回到默认状态，
// 强行要一句理由只会让人填「恢复」两个字，把审计摊薄成噪声。
func accountSyncResumeDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionAccountSyncResume, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{
			Fields: []action.Field{accountField(accounts)},
		},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func accountSyncPauseHandler(svc *Service) action.Handler {
	return accountSyncHandler(svc, true)
}

func accountSyncResumeHandler(svc *Service) action.Handler {
	return accountSyncHandler(svc, false)
}

func accountSyncHandler(svc *Service, paused bool) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if svc == nil {
			return nil, ErrServiceUnbound
		}
		account := stringParam(params, "account")
		reason := strings.TrimSpace(stringParam(params, "reason"))

		action.RecordResource(ctx, resourceCardAccount, account)

		p, err := svc.SetAccountSyncPause(ctx, account, paused, reason)
		if err != nil {
			return nil, err
		}

		if reason != "" {
			action.RecordReason(ctx, reason)
		}
		// 审计摘要里**一个卡片字段都没有**：这两个动作作用于账号，
		// 而审计是 append-only，多写的每一个字段都删不掉。
		summary := map[string]any{
			"account":   p.Account,
			"paused":    p.Paused,
			"reason":    p.Reason,
			"paused_at": p.PausedAt.UTC().Format(time.RFC3339),
		}
		action.RecordAfter(ctx, summary)
		return summary, nil
	}
}

// SetAccountSyncPause 写一条暂停/恢复记录。
//
// 账号必须已配置：给一个不存在的账号建暂停行，会在账号清单变化之后留下一行
// 永远不参与判断的垃圾，而运营看着后台以为自己已经停掉了某个东西。
func (s *Service) SetAccountSyncPause(ctx context.Context, account string, paused bool, reason string) (AccountSyncPause, error) {
	if _, err := s.account(account); err != nil {
		return AccountSyncPause{}, err
	}
	if paused && strings.TrimSpace(reason) == "" {
		return AccountSyncPause{}, fmt.Errorf("暂停同步必须写明原因")
	}

	p := AccountSyncPause{
		Account:  account,
		Paused:   paused,
		Reason:   strings.TrimSpace(reason),
		PausedBy: principalIDFrom(ctx),
		PausedAt: s.now().UTC(),
	}
	if err := s.store.SetAccountSyncPause(ctx, p); err != nil {
		return AccountSyncPause{}, fmt.Errorf("写入账号同步开关: %w", err)
	}
	return p, nil
}

// PausedAccounts 供只读端点展示「哪些账号正停着、为什么、停了多久」。
func (s *Service) PausedAccounts(ctx context.Context) (map[string]AccountSyncPause, error) {
	return s.store.PausedAccounts(ctx)
}

// principalIDFrom 取当前身份 id；取不到留空而不是编一个。
//
// 留空是诚实的：Action 内核保证这两个动作只有人类身份能调，
// 真正的「谁做的」由内核记的审计事件回答，这一列只是台账上的便利副本。
func principalIDFrom(ctx context.Context) string {
	if p, ok := principal.FromContext(ctx); ok {
		return p.ID
	}
	return ""
}
