package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
)

const hookSecret = "whsec_endpoint_test"

var hookNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

type fakeWebhookProcessor struct {
	secrets   map[string]string
	records   []cards.WebhookEvent
	accounts  []string
	refreshed []string
	recordRes cards.WebhookRecord
	recordErr error
	refreshErr error
	markedDone   []string
	challenges   []cards.CardChallenge
	challengeErr error
}

func (f *fakeWebhookProcessor) WebhookSecret(ctx context.Context, account string) (string, error) {
	s, ok := f.secrets[account]
	if !ok {
		return "", errors.New("no secret")
	}
	return s, nil
}

func (f *fakeWebhookProcessor) RecordWebhookEvent(ctx context.Context, account string, ev cards.WebhookEvent) (cards.WebhookRecord, error) {
	f.records = append(f.records, ev)
	f.accounts = append(f.accounts, account)
	return f.recordRes, f.recordErr
}

func (f *fakeWebhookProcessor) RefreshCard(ctx context.Context, account, cardID string, withTx bool) error {
	f.refreshed = append(f.refreshed, account+"/"+cardID)
	return f.refreshErr
}

func (f *fakeWebhookProcessor) MarkWebhookEventProcessed(ctx context.Context, account, eventID string) error {
	f.markedDone = append(f.markedDone, eventID)
	return nil
}

func (f *fakeWebhookProcessor) RecordWebhookEventFailure(ctx context.Context, account, eventID, reason string) error {
	return nil
}

func (f *fakeWebhookProcessor) RecordCardChallenge(ctx context.Context, c cards.CardChallenge) error {
	f.challenges = append(f.challenges, c)
	return f.challengeErr
}

func newProcessor() *fakeWebhookProcessor {
	return &fakeWebhookProcessor{
		secrets:   map[string]string{"CHRIS": hookSecret},
		recordRes: cards.WebhookRecord{Fresh: true},
	}
}

func hookRequest(t *testing.T, account, payload string, mutate func(*http.Request)) *http.Request {
	t.Helper()
	ts := strconv.FormatInt(hookNow.Unix(), 10)
	id := "evt-1"
	mac := hmac.New(sha256.New, []byte(hookSecret))
	mac.Write([]byte(ts + "." + id + "." + payload))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/infini/"+account, strings.NewReader(payload))
	req.Header.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Webhook-Timestamp", ts)
	req.Header.Set("X-Webhook-Event-Id", id)
	if mutate != nil {
		mutate(req)
	}
	return req
}

const statusChangePayload = `{"id":"evt-1","event":"card.status_change","occurred_at":1788609600,` +
	`"data":{"card":{"card_id":"card-1","status":"active"}}}`

func serveHook(p *fakeWebhookProcessor, req *http.Request) *httptest.ResponseRecorder {
	h := CardWebhookHandler(p, []string{"CHRIS", "LINFENG"}, func() time.Time { return hookNow }, nil)
	rec := httptest.NewRecorder()
	// 路由参数由 chi 提供；这里直接用测试路由挂上去。
	router := newWebhookTestRouter(h)
	router.ServeHTTP(rec, req)
	return rec
}

// 签名正确的状态变更事件：应触发定向刷新并标记已处理。
func TestCardWebhookAcceptsSignedStatusChange(t *testing.T) {
	p := newProcessor()
	rec := serveHook(p, hookRequest(t, "CHRIS", statusChangePayload, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200（否则上游会重投）: %s", rec.Code, rec.Body.String())
	}
	if len(p.refreshed) != 1 || p.refreshed[0] != "CHRIS/card-1" {
		t.Fatalf("应定向刷新 CHRIS/card-1, got %v", p.refreshed)
	}
	if len(p.markedDone) != 1 {
		t.Fatalf("处理成功后应标记完成, got %v", p.markedDone)
	}
}

// 签名不对必须拒，且**绝不能**触发任何刷新。
//
// 这个端点对公网开放，没有会话、没有 IP 限制——签名是唯一的闸。
func TestCardWebhookRejectsBadSignature(t *testing.T) {
	p := newProcessor()
	req := hookRequest(t, "CHRIS", statusChangePayload, func(r *http.Request) {
		r.Header.Set("X-Webhook-Signature", "00")
	})
	rec := serveHook(p, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401", rec.Code)
	}
	if len(p.refreshed) != 0 {
		t.Fatalf("签名不过绝不能触发刷新, got %v", p.refreshed)
	}
}

// 未配置的账号必须拒绝，而不是回落到某个账号。
func TestCardWebhookRejectsUnknownAccount(t *testing.T) {
	p := newProcessor()
	rec := serveHook(p, hookRequest(t, "NOPE", statusChangePayload, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want 404", rec.Code)
	}
	if len(p.refreshed) != 0 {
		t.Fatal("未知账号不该触发任何刷新")
	}
}

// 已经处理成功过的事件：直接回 200，不重复刷新。
func TestCardWebhookSkipsAlreadyProcessed(t *testing.T) {
	p := newProcessor()
	p.recordRes = cards.WebhookRecord{Fresh: false, AlreadyProcessed: true}

	rec := serveHook(p, hookRequest(t, "CHRIS", statusChangePayload, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	if len(p.refreshed) != 0 {
		t.Fatalf("已处理过的事件不该再刷新一次, got %v", p.refreshed)
	}
}

// **处理失败必须回非 200**，让上游重投。
//
// 回 200 等于告诉 Infini「收到并处理好了」，它就不会再重试——
// 一次真实的状态变更就此丢失，而我们这边没有任何痕迹说它丢了。
func TestCardWebhookReturnsErrorSoUpstreamRetries(t *testing.T) {
	p := newProcessor()
	p.refreshErr = errors.New("上游超时")

	rec := serveHook(p, hookRequest(t, "CHRIS", statusChangePayload, nil))
	if rec.Code < 500 {
		t.Fatalf("状态码 = %d, 处理失败必须回 5xx 让上游重投", rec.Code)
	}
	if len(p.markedDone) != 0 {
		t.Fatal("处理失败绝不能标记完成——那会让后续重试全被当成重复事件丢掉")
	}
}

// 与卡无关的事件（订单/订阅）安静接受，不刷新任何卡。
func TestCardWebhookIgnoresNonCardEvents(t *testing.T) {
	p := newProcessor()
	payload := `{"id":"evt-1","event":"order.completed","data":{}}`
	ts := strconv.FormatInt(hookNow.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(hookSecret))
	mac.Write([]byte(ts + ".evt-1." + payload))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/infini/CHRIS", strings.NewReader(payload))
	req.Header.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Webhook-Timestamp", ts)
	req.Header.Set("X-Webhook-Event-Id", "evt-1")

	rec := serveHook(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	if len(p.refreshed) != 0 {
		t.Fatalf("订单事件不该刷新卡片, got %v", p.refreshed)
	}
}

// 挑战事件落库但**不刷新卡片**：它与卡的状态、余额都无关，
// 重读一次上游只是白打一个受 IP 白名单限制的接口。
func TestCardWebhookStoresChallengeWithoutRefreshing(t *testing.T) {
	p := newProcessor()
	payload := `{"event":"card.challenge","data":{"card":{"card_id":"card-1"},` +
		`"challenge_id":"ch-1","challenge_type":"authorization_code","expires_at":1788553063}}`

	ts := strconv.FormatInt(hookNow.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(hookSecret))
	mac.Write([]byte(ts + ".evt-1." + payload))
	req := httptest.NewRequest(http.MethodPost, "/webhooks/infini/CHRIS", strings.NewReader(payload))
	req.Header.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Webhook-Timestamp", ts)
	req.Header.Set("X-Webhook-Event-Id", "evt-1")

	rec := serveHook(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	if len(p.challenges) != 1 || p.challenges[0].ID != "ch-1" {
		t.Fatalf("挑战应落库, got %+v", p.challenges)
	}
	if len(p.refreshed) != 0 {
		t.Fatalf("挑战事件不该刷新卡片, got %v", p.refreshed)
	}
}
