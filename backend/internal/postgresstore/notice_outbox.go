package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 通知发件箱（XM-INV-SUBMIT-NOTICE）。与 email_outbox 同一手法、不同的表：
// 那张是发票邮件专用的（收件人哈希、文档、模板版本），字段一个都不适用。
const (
	// NoticeKindRequestSubmitted 是稳定事件键，进消息编号，只增不改。
	NoticeKindRequestSubmitted = "request.submitted"
	// noticeMaxAttempts：超过就停在 failed，不再重试。
	//
	// 8 次配合下面的退避大约覆盖两个多小时。再往后重试没有意义：一条两小时
	// 前的"有人提交了开票申请"已经不是通知而是历史，而真正的故障（地址填错、
	// 机器人被移出群）不会因为多试几次就好。行留着，管理端看得见它失败了。
	noticeMaxAttempts = 8
	// noticeLeaseSeconds 是取件后把 next_attempt_at 推后的秒数。
	//
	// 它就是租约：进程崩在投递中途时，这一行会在租约到期后重新到期、被下一
	// 轮取走。不另开 lease_token 列——发件箱里同一时刻只有一个投递循环
	// （API 进程内的后台循环），SKIP LOCKED 加这个推后已经足够，多一列就多
	// 一处要解释的状态。
	noticeLeaseSeconds = 300
)

// noticeBackoffSeconds 是第 N 次失败后的等待秒数，指数退避并封顶。
//
// 封顶而不是无限翻倍：一条通知的价值随时间衰减，等到第 8 次时再拉长间隔
// 只是让"它到底发没发出去"更晚才有结论。
func noticeBackoffSeconds(attempt int) int {
	seconds := 30
	for i := 1; i < attempt; i++ {
		seconds *= 2
		if seconds >= 1800 {
			return 1800
		}
	}
	return seconds
}

// InvoiceNotice 是一条待投递的通知，**正文在取件时现取**。
//
// 表里不存正文（见迁移 0027 的注释）：业务数据不复制第二份，通知永远不会与
// 记录漂移；更要紧的是，申请里的抬头、税号、银行账号在 profile_snapshot_ciphertext
// 里加密存着，这里只读那几列非 PII 字段，于是任何 PII 都不会因为"要发通知"
// 而落进一张新表。
type InvoiceNotice struct {
	ID           string
	Kind         string
	AttemptCount int
	RequestNo    string
	Status       string
	AmountMinor  int64
	Currency     string
	SourceType   string
	SubmittedAt  time.Time
}

// EnqueueInvoiceNoticeTx 在**调用方的事务里**入队。
//
// 同事务是这条设计的全部意义：通知行存在当且仅当申请真的提交成功了。放在
// 事务外的话，提交回滚而通知已入队会推出一条不存在的申请；反过来先提交后
// 入队，进程在两步之间崩掉就静默丢一条通知。
//
// ON CONFLICT DO NOTHING 配合唯一约束：一份申请的同一种事件只通知一次。
func EnqueueInvoiceNoticeTx(ctx context.Context, tx pgx.Tx, requestID, kind string) error {
	if strings.TrimSpace(requestID) == "" {
		return errors.New("notice requires an invoice request id")
	}
	if kind != NoticeKindRequestSubmitted {
		return fmt.Errorf("unsupported invoice notice kind %q", kind)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO invoice_notice_outbox(id,invoice_request_id,kind)
		VALUES($1,$2,$3)
		ON CONFLICT (invoice_request_id,kind) DO NOTHING`, randomUUID(), requestID, kind)
	if err != nil {
		return fmt.Errorf("enqueue invoice notice: %w", err)
	}
	return nil
}

// ClaimInvoiceNotices 取走到期的通知并置为 sending。
//
// 取件与投递分开的理由与 email_outbox 一致：投递是一次跨网络调用，不能占着
// 数据库事务。这里把 next_attempt_at 推后一个租约长度，于是投递中途崩掉的
// 那一行会自己回到队列，不需要人工干预。
func (s *Store) ClaimInvoiceNotices(ctx context.Context, limit int, now time.Time) ([]InvoiceNotice, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM invoice_notice_outbox
			WHERE status IN ('queued','sending') AND attempt_count < $1 AND next_attempt_at <= $2
			ORDER BY next_attempt_at,id
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		), claimed AS (
			UPDATE invoice_notice_outbox outbox
			SET status='sending',
				attempt_count=outbox.attempt_count+1,
				next_attempt_at=$2::timestamptz + make_interval(secs => $4::int),
				updated_at=$2
			FROM due WHERE outbox.id=due.id
			RETURNING outbox.id,outbox.kind,outbox.attempt_count,outbox.invoice_request_id
		)
		SELECT claimed.id::text,claimed.kind,claimed.attempt_count,
			request.request_no,request.status,request.amount_minor,request.currency,
			COALESCE(source.source_type,''),request.submitted_at
		FROM claimed
		JOIN invoice_requests request ON request.id=claimed.invoice_request_id
		LEFT JOIN source_instances source ON source.id=request.source_instance_id
		ORDER BY request.submitted_at,claimed.id`,
		noticeMaxAttempts, now.UTC(), limit, noticeLeaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("claim invoice notices: %w", err)
	}
	defer rows.Close()
	notices := make([]InvoiceNotice, 0, limit)
	for rows.Next() {
		var notice InvoiceNotice
		if err = rows.Scan(&notice.ID, &notice.Kind, &notice.AttemptCount, &notice.RequestNo,
			&notice.Status, &notice.AmountMinor, &notice.Currency, &notice.SourceType,
			&notice.SubmittedAt); err != nil {
			return nil, fmt.Errorf("scan invoice notice: %w", err)
		}
		notice.SubmittedAt = notice.SubmittedAt.UTC()
		notices = append(notices, notice)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invoice notices: %w", err)
	}
	return notices, nil
}

// MarkInvoiceNoticeSent 记下送达。
func (s *Store) MarkInvoiceNoticeSent(ctx context.Context, id string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE invoice_notice_outbox
		SET status='sent',delivered_at=$2,last_error_code='',updated_at=$2
		WHERE id=$1::uuid AND status='sending'`, id, now.UTC())
	if err != nil {
		return fmt.Errorf("mark invoice notice sent: %w", err)
	}
	return nil
}

// MarkInvoiceNoticeFailed 记下这一次失败并安排下一次；到达上限时停在 failed。
//
// errorCode 只落**码**，不落上游返回的整段文本：企微的 errmsg 会夹带它自己的
// 诊断信息，而这张表会进管理端只读查询（宪法 7 条的同一条顾虑）。
func (s *Store) MarkInvoiceNoticeFailed(ctx context.Context, id, errorCode string, attemptCount int, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	code := strings.Join(strings.Fields(errorCode), " ")
	if len(code) > 64 {
		code = code[:64]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE invoice_notice_outbox
		SET status=CASE WHEN attempt_count >= $3 THEN 'failed' ELSE 'queued' END,
			next_attempt_at=$4::timestamptz + make_interval(secs => $5::int),
			last_error_code=$2,updated_at=$4
		WHERE id=$1::uuid AND status='sending'`,
		id, code, noticeMaxAttempts, now.UTC(), noticeBackoffSeconds(attemptCount))
	if err != nil {
		return fmt.Errorf("mark invoice notice failed: %w", err)
	}
	return nil
}

// InvoiceNoticeState 是一条通知在管理端要显示的投递事实。
//
// **与 InvoiceNotice 不是同一个东西**：那个是投递循环取件时用的，带着申请
// 自身的字段（单号、金额、状态）好去渲染消息；这个只回答"这条通知发出去
// 了没有"，不带任何申请内容——管理端那一屏本来就在申请详情里，再抄一份
// 只会有两份可能不一致的事实。
type InvoiceNoticeState struct {
	ID            string
	Kind          string
	Status        string
	AttemptCount  int
	NextAttemptAt time.Time
	// DeliveredAt 为 nil 表示还没送达（排队中或已放弃），不是"零时刻送达"。
	DeliveredAt *time.Time
	// LastErrorCode 是我方分类过的短码（如 "wecom: errcode 93000"），
	// **不含 Webhook 地址、也不含上游自由文本**——投递侧已经保证了这一点
	// （见 internal/notify 的 WeComSender.Send：net/http 的错误会带上含 key
	// 的 URL，所以那里刻意不把 err 带出来）。因此这个值可以直接给管理员看。
	LastErrorCode string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ListInvoiceNoticesForRequest 列出一份申请的全部通知投递状态，最近入队的在前。
//
// **不做 principal 归属校验**：调用方是管理端专用路由。这条信息是运维事实
// （"那条企业微信通知发出去了没有"），不是申请人的业务数据——用户看到自己
// 的申请有没有触发内部推送，既没有用，也是一次不必要的内部暴露。
func (s *Store) ListInvoiceNoticesForRequest(ctx context.Context, requestID string) ([]InvoiceNoticeState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,kind,status,attempt_count,next_attempt_at,delivered_at,
			last_error_code,created_at,updated_at
		FROM invoice_notice_outbox
		WHERE invoice_request_id=$1::uuid
		ORDER BY created_at DESC, id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := []InvoiceNoticeState{}
	for rows.Next() {
		var state InvoiceNoticeState
		if err = rows.Scan(&state.ID, &state.Kind, &state.Status, &state.AttemptCount,
			&state.NextAttemptAt, &state.DeliveredAt, &state.LastErrorCode,
			&state.CreatedAt, &state.UpdatedAt); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}
