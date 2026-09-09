package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/postgresstore"
)

func testNotice() postgresstore.InvoiceNotice {
	return postgresstore.InvoiceNotice{
		ID: "11111111-2222-3333-4444-555555555555", Kind: postgresstore.NoticeKindRequestSubmitted,
		AttemptCount: 1, RequestNo: "INV-20260906-0007", Status: "pending_review",
		AmountMinor: 123456, Currency: "CNY", SourceType: "sub2api",
		SubmittedAt: time.Date(2026, 9, 6, 3, 4, 5, 0, time.UTC),
	}
}

func TestEnvelopeCarriesTheBadgeCodeAndAction(t *testing.T) {
	content := RenderWeComMarkdown(EnvelopeFor(testNotice(), "production"))
	for _, want := range []string{
		"【星芒·开票】", "通知", " · production",
		"> 标题：新的开票申请 INV-20260906-0007",
		"> 申请单号：INV-20260906-0007",
		"> 金额：1234.56 CNY",
		"> 来源：sub2api",
		"> 状态：待审核",
		"> 提交时间：2026-09-06T03:04:05Z",
		"> 编号：XM-INVOICE-request.submitted",
		"> 处理：管理后台 → 开票 → 申请审核",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("message is missing %q:\n%s", want, content)
		}
	}
	if !strings.HasPrefix(content, "【星芒·开票】") {
		t.Fatalf("the badge must lead: %q", strings.SplitN(content, "\n", 2)[0])
	}
}

// 这是本片最重要的一条测试：群机器人的消息留在聊天记录里，那不是我们能控制
// 的存储。开票申请里有抬头、税号、银行账号、地址、电话——它们**在结构体里
// 就不存在**，所以这条测试真正守的是"将来有人往 InvoiceNotice 上加字段时，
// 必须先撞到这里"。
func TestNoticePayloadCannotCarryInvoicePII(t *testing.T) {
	fields := map[string]bool{
		"ID": true, "Kind": true, "AttemptCount": true, "RequestNo": true,
		"Status": true, "AmountMinor": true, "Currency": true, "SourceType": true,
		"SubmittedAt": true,
	}
	typ := reflect.TypeOf(postgresstore.InvoiceNotice{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !fields[name] {
			t.Fatalf("InvoiceNotice gained the field %q: a notice may only carry non-PII columns "+
				"(no title, tax id, bank account, address or phone). Add it to this list only after "+
				"confirming it is not PII.", name)
		}
	}
}

func TestAmountNeverGoesThroughFloat(t *testing.T) {
	cases := map[int64]string{
		0: "0.00 CNY", 5: "0.05 CNY", 20000: "200.00 CNY",
		199999999999: "1999999999.99 CNY", -250: "-2.50 CNY",
	}
	for minor, want := range cases {
		if got := formatMinor(minor, "CNY"); got != want {
			t.Fatalf("formatMinor(%d)=%q want %q", minor, got, want)
		}
	}
	if got := formatMinor(100, ""); got != "1.00 CNY" {
		t.Fatalf("an empty currency must default to CNY, got %q", got)
	}
}

func TestUnknownStatusIsShownVerbatim(t *testing.T) {
	notice := testNotice()
	notice.Status = "some_new_upstream_state"
	content := RenderWeComMarkdown(EnvelopeFor(notice, "staging"))
	if !strings.Contains(content, "> 状态：some_new_upstream_state") {
		t.Fatalf("an unmapped status must be shown as-is rather than guessed at:\n%s", content)
	}
}

func TestTruncationKeepsTheHeaderAndCode(t *testing.T) {
	notice := testNotice()
	notice.RequestNo = strings.Repeat("很长的单号", 2000)
	content := RenderWeComMarkdown(EnvelopeFor(notice, "production"))
	if len(content) > weComMaxContentBytes {
		t.Fatalf("content is %d bytes, over the %d-byte WeCom limit", len(content), weComMaxContentBytes)
	}
	for _, want := range []string{"【星芒·开票】", "> 编号：XM-INVOICE-request.submitted", truncationMarker} {
		if !strings.Contains(content, want) {
			t.Fatalf("truncated message lost %q", want)
		}
	}
}

func TestMultiLineValueCannotEscapeTheQuoteBlock(t *testing.T) {
	notice := testNotice()
	notice.RequestNo = "INV-1\n**注入**\n> 假的一行"
	content := RenderWeComMarkdown(EnvelopeFor(notice, "production"))
	for _, line := range strings.Split(content, "\n")[1:] {
		if line != "" && !strings.HasPrefix(line, "> ") {
			t.Fatalf("a multi-line value escaped the quote block: %q\n%s", line, content)
		}
	}
}

// 地址本身就是凭据：形状不对的要被拒，且任何错误都不得回显它。
//
// XM-INV-NOTICE-WEBHOOK-SETTING 起，校验从构造期挪到了**保存那一刻**
// （管理端点保存时调 ValidateWebhookAddress）——那时人就在页面前，比等
// api 重启才在日志里报错强得多。
func TestValidateWebhookAddressRejectsBadShapesWithoutEchoingThem(t *testing.T) {
	secretish := "https://qyapi.example.test/webhook/send?key=super-secret-value"
	for _, address := range []string{"", "   ", "http://qyapi.example.test/send?key=super-secret-value", "://bad"} {
		err := ValidateWebhookAddress(address)
		if err == nil {
			t.Fatalf("address %q must be rejected", address)
		}
		if strings.Contains(err.Error(), "super-secret-value") {
			t.Fatalf("error echoed the credential: %v", err)
		}
	}
	if err := ValidateWebhookAddress(secretish); err != nil {
		t.Fatalf("a well-formed https address must be accepted: %v", err)
	}
}

// 地址每次投递现取：换了地址，下一条就用新的，不必重启。
func TestSenderResolvesTheAddressOnEverySend(t *testing.T) {
	hits := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer server.Close()
	calls := 0
	sender := NewWeComSender(func(context.Context) (string, error) {
		calls++
		return server.URL + "/webhook/send?key=k", nil
	}, server.Client())
	for i := 0; i < 3; i++ {
		if err := sender.Send(context.Background(), "hello"); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	if calls != 3 {
		t.Fatalf("地址应当每次现取，实际只取了 %d 次", calls)
	}
	if hits != 3 {
		t.Fatalf("上游收到 %d 条，want 3", hits)
	}
}

// 取不到地址（没配、或解密失败）不是崩溃，是"这条发不出去"——发件箱会重试，
// 而错误里不能夹带任何地址。
func TestSenderFailsClosedWhenTheAddressIsUnavailable(t *testing.T) {
	sender := NewWeComSender(func(context.Context) (string, error) {
		return "", errors.New("secret missing")
	}, nil)
	err := sender.Send(context.Background(), "hello")
	if err == nil {
		t.Fatal("取不到地址必须算失败")
	}
	if strings.Contains(err.Error(), "secret missing") {
		t.Fatalf("不该把底层错误原样带出来：%v", err)
	}
}

func TestSenderPostsMarkdownAndReadsErrcode(t *testing.T) {
	var got map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer server.Close()
	sender := NewWeComSender(func(context.Context) (string, error) { return server.URL + "/webhook/send?key=k", nil }, server.Client())
	if err := sender.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got["msgtype"] != "markdown" {
		t.Fatalf("msgtype=%v want markdown", got["msgtype"])
	}
}

// HTTP 200 不等于送达：企微把业务错误放在响应体里。
func TestSenderFailsOnBusinessErrorAndHidesUpstreamText(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":93000,"errmsg":"invalid webhook url https://qyapi/send?key=leak"}`)
	}))
	defer server.Close()
	sender := NewWeComSender(func(context.Context) (string, error) { return server.URL + "/webhook/send?key=k", nil }, server.Client())
	err := sender.Send(context.Background(), "hello")
	if err == nil {
		t.Fatal("errcode 93000 must be an error even though HTTP was 200")
	}
	if !strings.Contains(err.Error(), "93000") {
		t.Fatalf("the error code must be reported: %v", err)
	}
	// errmsg 是上游自由文本，会落进发件箱与管理端——不转述。
	if strings.Contains(err.Error(), "leak") || strings.Contains(err.Error(), "invalid webhook url") {
		t.Fatalf("upstream free text must not be relayed: %v", err)
	}
}

type stubRepository struct {
	claimed  []postgresstore.InvoiceNotice
	claimErr error
	sent     []string
	failed   []struct {
		id       string
		code     string
		attempts int
	}
}

func (r *stubRepository) ClaimInvoiceNotices(context.Context, int, time.Time) ([]postgresstore.InvoiceNotice, error) {
	return r.claimed, r.claimErr
}

func (r *stubRepository) MarkInvoiceNoticeSent(_ context.Context, id string, _ time.Time) error {
	r.sent = append(r.sent, id)
	return nil
}

func (r *stubRepository) MarkInvoiceNoticeFailed(_ context.Context, id, code string, attempts int, _ time.Time) error {
	r.failed = append(r.failed, struct {
		id       string
		code     string
		attempts int
	}{id, code, attempts})
	return nil
}

type stubSender struct {
	err  error
	sent []string
}

func (s *stubSender) Send(_ context.Context, content string) error {
	s.sent = append(s.sent, content)
	return s.err
}

func TestWorkerMarksSentOnSuccess(t *testing.T) {
	repo := &stubRepository{claimed: []postgresstore.InvoiceNotice{testNotice()}}
	sender := &stubSender{}
	sent, failed, err := Worker{Repository: repo, Sender: sender, Environment: "production"}.RunOnce(context.Background())
	if err != nil || sent != 1 || failed != 0 {
		t.Fatalf("sent=%d failed=%d err=%v, want 1/0/nil", sent, failed, err)
	}
	if len(repo.sent) != 1 || repo.sent[0] != testNotice().ID {
		t.Fatalf("the delivered notice must be marked sent: %+v", repo.sent)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0], "XM-INVOICE-request.submitted") {
		t.Fatalf("the rendered message must carry the code: %+v", sender.sent)
	}
}

// 投递失败不让整轮失败：它已经如实落进发件箱，下一轮会重试。
func TestWorkerRecordsDeliveryFailureWithoutFailingTheRound(t *testing.T) {
	repo := &stubRepository{claimed: []postgresstore.InvoiceNotice{testNotice()}}
	sender := &stubSender{err: errors.New("wecom: errcode 93000")}
	sent, failed, err := Worker{Repository: repo, Sender: sender}.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("a delivery failure must not fail the round: %v", err)
	}
	if sent != 0 || failed != 1 {
		t.Fatalf("sent=%d failed=%d, want 0/1", sent, failed)
	}
	if len(repo.failed) != 1 || repo.failed[0].code != "wecom: errcode 93000" || repo.failed[0].attempts != 1 {
		t.Fatalf("the failure must be recorded with its attempt count: %+v", repo.failed)
	}
	if len(repo.sent) != 0 {
		t.Fatalf("a failed delivery must never be marked sent: %+v", repo.sent)
	}
}

// 取件失败是数据库故障，要往上冒。
func TestWorkerReturnsClaimErrors(t *testing.T) {
	repo := &stubRepository{claimErr: errors.New("database is down")}
	if _, _, err := (Worker{Repository: repo, Sender: &stubSender{}}).RunOnce(context.Background()); err == nil {
		t.Fatal("a claim failure must be reported")
	}
}
