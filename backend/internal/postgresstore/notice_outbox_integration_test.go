package postgresstore

import (
	"context"
	"testing"
	"time"
)

// XM-INV-SUBMIT-NOTICE：发件箱的三件事——入队与提交同事务、取件带租约、
// 失败按次退避且到上限停住。

func seedNoticeRequest(t *testing.T, store *Store, ctx context.Context, requestNo string, submittedAt time.Time) string {
	t.Helper()
	userID := randomUUID()
	profileID := randomUUID()
	requestID := randomUUID()
	sourceID := randomUUID()
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test',$2)`, userID, "notice-"+userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name)
		VALUES($1,'sub2api',$2)`, sourceID, "notice-"+sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_profiles(
		id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,revision)
		VALUES($1,$2,'enterprise',$3,$3,1)`, profileID, userID, []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	// 政策快照必须与不可变的单例逐字一致（0011 的触发器会拒绝别的值）。
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_requests(
		id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,
		currency,issuer_code,amount_minor,status,idempotency_key,version,
		eligibility_policy_start_at,eligibility_policy_version,submitted_at,updated_at)
		SELECT $1,$2,$3,$4,$5,$6,'CNY','default',$7,'pending_review',$9,1,
			policy.eligibility_start_at,policy.policy_version,$8,$8
		FROM invoice_eligibility_policy policy WHERE policy.singleton_id=1`,
		requestID, requestNo, userID, sourceID, profileID, []byte("snapshot"), int64(123456), submittedAt, requestID); err != nil {
		t.Fatal(err)
	}
	return requestID
}

func enqueueNotice(t *testing.T, store *Store, ctx context.Context, requestID string) {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = EnqueueInvoiceNoticeTx(ctx, tx, requestID, NoticeKindRequestSubmitted); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInvoiceNoticeIsClaimedWithTheRequestFieldsItNeeds(t *testing.T) {
	store, ctx := integrationStore(t)
	submittedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	requestID := seedNoticeRequest(t, store, ctx, "INV-NOTICE-0001", submittedAt)
	enqueueNotice(t, store, ctx, requestID)

	notices, err := store.ClaimInvoiceNotices(ctx, 10, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("claimed %d notices, want 1", len(notices))
	}
	notice := notices[0]
	if notice.Kind != NoticeKindRequestSubmitted || notice.RequestNo != "INV-NOTICE-0001" ||
		notice.Status != "pending_review" || notice.AmountMinor != 123456 || notice.Currency != "CNY" ||
		notice.AttemptCount != 1 || !notice.SubmittedAt.Equal(submittedAt) {
		t.Fatalf("claimed notice does not carry the request's own fields: %+v", notice)
	}
}

// 取件带租约：同一条不会被同一轮的第二次取件重复拿走，但租约到期后会自己
// 回到队列（投递中途崩掉的那一行不需要人工干预）。
func TestClaimLeasesTheNoticeAndReleasesItAfterTheLease(t *testing.T) {
	store, ctx := integrationStore(t)
	requestID := seedNoticeRequest(t, store, ctx, "INV-NOTICE-0002", time.Now().UTC())
	enqueueNotice(t, store, ctx, requestID)
	now := time.Now().UTC()

	first, err := store.ClaimInvoiceNotices(ctx, 10, now)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %d %v", len(first), err)
	}
	again, err := store.ClaimInvoiceNotices(ctx, 10, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a leased notice must not be claimed twice: %+v", again)
	}
	expired, err := store.ClaimInvoiceNotices(ctx, 10, now.Add((noticeLeaseSeconds+1)*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].AttemptCount != 2 {
		t.Fatalf("an expired lease must return the notice with a bumped attempt count: %+v", expired)
	}
}

func TestMarkSentIsTerminalAndMarkFailedReschedules(t *testing.T) {
	store, ctx := integrationStore(t)
	sentID := seedNoticeRequest(t, store, ctx, "INV-NOTICE-0003", time.Now().UTC())
	failedID := seedNoticeRequest(t, store, ctx, "INV-NOTICE-0004", time.Now().UTC().Add(time.Second))
	enqueueNotice(t, store, ctx, sentID)
	enqueueNotice(t, store, ctx, failedID)
	now := time.Now().UTC()
	notices, err := store.ClaimInvoiceNotices(ctx, 10, now)
	if err != nil || len(notices) != 2 {
		t.Fatalf("claimed %d want 2: %v", len(notices), err)
	}
	byRequestNo := map[string]InvoiceNotice{}
	for _, notice := range notices {
		byRequestNo[notice.RequestNo] = notice
	}
	if err = store.MarkInvoiceNoticeSent(ctx, byRequestNo["INV-NOTICE-0003"].ID, now); err != nil {
		t.Fatal(err)
	}
	failed := byRequestNo["INV-NOTICE-0004"]
	if err = store.MarkInvoiceNoticeFailed(ctx, failed.ID, "wecom: errcode 93000", failed.AttemptCount, now); err != nil {
		t.Fatal(err)
	}

	var status, code string
	var delivered *time.Time
	if err = store.pool.QueryRow(ctx, `SELECT status,last_error_code,delivered_at FROM invoice_notice_outbox
		WHERE id=$1::uuid`, byRequestNo["INV-NOTICE-0003"].ID).Scan(&status, &code, &delivered); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || code != "" || delivered == nil {
		t.Fatalf("a delivered notice: status=%q code=%q delivered=%v", status, code, delivered)
	}
	var nextAttempt time.Time
	if err = store.pool.QueryRow(ctx, `SELECT status,last_error_code,next_attempt_at FROM invoice_notice_outbox
		WHERE id=$1::uuid`, failed.ID).Scan(&status, &code, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || code != "wecom: errcode 93000" || !nextAttempt.After(now) {
		t.Fatalf("a failed notice must be requeued with its code: status=%q code=%q next=%v", status, code, nextAttempt)
	}
	// 已送达的那条不会被再取一次。
	rest, err := store.ClaimInvoiceNotices(ctx, 10, now.Add((noticeLeaseSeconds+1)*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, notice := range rest {
		if notice.RequestNo == "INV-NOTICE-0003" {
			t.Fatalf("a sent notice must never be claimed again: %+v", notice)
		}
	}
}

// 到达上限后停在 failed，不再无休止重试——一条两小时前的"有人提交了申请"
// 已经不是通知而是历史；行留着，看得见它失败了。
func TestNoticeStopsRetryingAtTheAttemptCeiling(t *testing.T) {
	store, ctx := integrationStore(t)
	requestID := seedNoticeRequest(t, store, ctx, "INV-NOTICE-0005", time.Now().UTC())
	enqueueNotice(t, store, ctx, requestID)
	now := time.Now().UTC()
	var id string
	for attempt := 1; attempt <= noticeMaxAttempts; attempt++ {
		notices, err := store.ClaimInvoiceNotices(ctx, 10, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(notices) != 1 {
			t.Fatalf("attempt %d claimed %d notices, want 1", attempt, len(notices))
		}
		id = notices[0].ID
		if err = store.MarkInvoiceNoticeFailed(ctx, id, "wecom: request failed", notices[0].AttemptCount, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Hour)
	}
	var status string
	var attempts int
	if err := store.pool.QueryRow(ctx, `SELECT status,attempt_count FROM invoice_notice_outbox WHERE id=$1::uuid`,
		id).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != noticeMaxAttempts {
		t.Fatalf("status=%q attempts=%d, want failed/%d", status, attempts, noticeMaxAttempts)
	}
	if notices, err := store.ClaimInvoiceNotices(ctx, 10, now.Add(24*time.Hour)); err != nil || len(notices) != 0 {
		t.Fatalf("a notice at the ceiling must not be claimed again: %d %v", len(notices), err)
	}
}

// 一份申请的同一种事件只入队一次（重复提交走的是"返回已存在的申请"分支）。
func TestEnqueueIsIdempotentPerRequestAndKind(t *testing.T) {
	store, ctx := integrationStore(t)
	requestID := seedNoticeRequest(t, store, ctx, "INV-NOTICE-0006", time.Now().UTC())
	enqueueNotice(t, store, ctx, requestID)
	enqueueNotice(t, store, ctx, requestID)
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM invoice_notice_outbox
		WHERE invoice_request_id=$1::uuid`, requestID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("enqueued %d rows for one request, want 1", count)
	}
}
