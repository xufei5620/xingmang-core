package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"invoice-system/backend/internal/postgresstore"
)

// Repository 是投递循环需要的发件箱操作。用接口而不是直接吃 *Store：
// 这一层的判定（渲染什么、失败怎么记）能在替身上完整测到，而在真库上测
// "第 8 次失败后停在 failed"要造八轮时间。
type Repository interface {
	ClaimInvoiceNotices(ctx context.Context, limit int, now time.Time) ([]postgresstore.InvoiceNotice, error)
	MarkInvoiceNoticeSent(ctx context.Context, id string, now time.Time) error
	MarkInvoiceNoticeFailed(ctx context.Context, id, errorCode string, attemptCount int, now time.Time) error
}

// Sender 投递一条渲染好的消息。
type Sender interface {
	Send(ctx context.Context, content string) error
}

// Worker 跑一轮发件箱投递。
//
// 与 mailer.Worker 同一形状（本仓库已有的发件箱循环），刻意照抄：一轮取一批、
// 逐条投递、成败各自落库，单条失败不影响同批其它条，整轮也不因此失败——
// 下一轮会重试。
type Worker struct {
	Repository  Repository
	Sender      Sender
	Environment string
	Logger      *slog.Logger
	// BatchLimit 是一轮取件上限；0 用默认值。
	BatchLimit int
	Now        func() time.Time
}

const defaultNoticeBatchLimit = 20

// RunOnce 取一批并投递，返回成功与失败条数。
//
// 返回 error 只在**取件本身**失败时——那是数据库故障，需要往上冒。投递失败
// 不是：它已经如实落进发件箱，下一轮会重试，让整个循环报错只会把日志刷满。
func (w Worker) RunOnce(ctx context.Context) (sent int, failed int, err error) {
	if w.Repository == nil || w.Sender == nil {
		return 0, 0, fmt.Errorf("invoice notice worker is not configured")
	}
	now := time.Now().UTC
	if w.Now != nil {
		now = func() time.Time { return w.Now().UTC() }
	}
	limit := w.BatchLimit
	if limit <= 0 {
		limit = defaultNoticeBatchLimit
	}
	notices, err := w.Repository.ClaimInvoiceNotices(ctx, limit, now())
	if err != nil {
		return 0, 0, err
	}
	for _, notice := range notices {
		content := RenderWeComMarkdown(EnvelopeFor(notice, w.Environment))
		if sendErr := w.Sender.Send(ctx, content); sendErr != nil {
			failed++
			if markErr := w.Repository.MarkInvoiceNoticeFailed(ctx, notice.ID, sendErr.Error(), notice.AttemptCount, now()); markErr != nil {
				// 投递失败**且**记不下来：这一条会在租约到期后重来，日志是
				// 唯一的现场。
				w.log().WarnContext(ctx, "invoice_notice_mark_failed_failed",
					slog.String("notice_id", notice.ID), slog.String("err", markErr.Error()))
			}
			continue
		}
		if markErr := w.Repository.MarkInvoiceNoticeSent(ctx, notice.ID, now()); markErr != nil {
			// 发出去了但状态写不进去：下一轮会重发，收件人看到重复消息。
			// 这比反过来（写成已发但其实没发）好得多。
			w.log().WarnContext(ctx, "invoice_notice_mark_sent_failed",
				slog.String("notice_id", notice.ID), slog.String("err", markErr.Error()))
		}
		sent++
	}
	return sent, failed, nil
}

func (w Worker) log() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

// EnvelopeFor 把一条发件箱记录变成消息信封。
//
// **只读非 PII 字段**：申请单号、金额、币种、来源平台、状态、提交时刻。抬头、
// 税号、银行账号、地址、电话在 profile_snapshot_ciphertext 里加密存着，本函数
// 拿不到，也永远不该拿到——群机器人的消息留在聊天记录里，那不是我们能控制的
// 存储（与卡片推送只带卡号掩码同一条纪律）。
//
// 严重度是 info：一条"有人提交了开票申请"是周知，不是要立刻动手的事；真正
// 需要动手的是"待审核积压超过 12 小时"，那是另一条规则、另一个严重度。
func EnvelopeFor(notice postgresstore.InvoiceNotice, environment string) Envelope {
	return Envelope{
		Kind:        notice.Kind,
		Severity:    SeverityInfo,
		Environment: environment,
		Title:       "新的开票申请 " + notice.RequestNo,
		Lines: []Line{
			{Label: "申请单号", Value: notice.RequestNo},
			{Label: "金额", Value: formatMinor(notice.AmountMinor, notice.Currency)},
			{Label: "来源", Value: notice.SourceType},
			{Label: "状态", Value: statusLabel(notice.Status)},
			{Label: "提交时间", Value: notice.SubmittedAt.UTC().Format(time.RFC3339)},
		},
		Action: "管理后台 → 开票 → 申请审核",
	}
}

// formatMinor 把最小货币单位渲染成人看的金额。整数进整数出，**不经过
// float**（宪法 13 条）：一笔 20000 分要显示成 200.00，用浮点会在某些数上
// 得到 199.99999999999997。
func formatMinor(minor int64, currency string) string {
	if currency == "" {
		currency = "CNY"
	}
	negative := minor < 0
	if negative {
		minor = -minor
	}
	text := fmt.Sprintf("%s.%02d", strconv.FormatInt(minor/100, 10), minor%100)
	if negative {
		text = "-" + text
	}
	return text + " " + currency
}

// statusLabel 是给人看的中文状态。没见过的取值原样显示——上游加了新状态时，
// 显示一个陌生的英文词，好过把它归到某个猜出来的中文档里。
func statusLabel(status string) string {
	labels := map[string]string{
		"pending_review":           "待审核",
		"needs_changes":            "需修改后重提",
		"approved":                 "审核通过，待开具",
		"rejected":                 "审核驳回",
		"user_cancelled":           "用户已撤销",
		"manual_issuing":           "人工开具中",
		"issued_awaiting_document": "已开具，待回传票据",
		"issued":                   "已开具",
		"refund_attention":         "涉退款，需关注",
	}
	if label, ok := labels[status]; ok {
		return label
	}
	return status
}
