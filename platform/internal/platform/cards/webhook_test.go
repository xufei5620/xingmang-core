package cards

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

const testWebhookSecret = "whsec_test_0123456789abcdef"

func signPayload(t *testing.T, secret, timestamp, eventID, payload string, encodeHex bool) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + eventID + "." + payload))
	sum := mac.Sum(nil)
	if encodeHex {
		return hex.EncodeToString(sum)
	}
	return base64.StdEncoding.EncodeToString(sum)
}

func verifierAt(now time.Time) *WebhookVerifier {
	return NewWebhookVerifier(func() time.Time { return now })
}

var webhookNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func validHeaders(t *testing.T, payload string, encodeHex bool) WebhookHeaders {
	t.Helper()
	ts := "1788609600" // webhookNow 的 Unix 秒
	id := "b7ef2c62-6177-4ea8-84ec-3080f2db58f0"
	return WebhookHeaders{
		Signature: signPayload(t, testWebhookSecret, ts, id, payload, encodeHex),
		Timestamp: ts,
		EventID:   id,
	}
}

// 正确的十六进制签名必须通过。
//
// 编码是 hex 而不是 base64——依据是官方参考实现（infini-skill 的
// WEBHOOKS.md 用 .hexdigest()）。base64 的签名必须被拒：多认一种编码
// 就是多一条我们没有依据的信任路径。
func TestWebhookVerifierAcceptsHexOnly(t *testing.T) {
	payload := `{"id":"b7ef2c62-6177-4ea8-84ec-3080f2db58f0","event":"card.status_change"}`

	if err := verifierAt(webhookNow).Verify(testWebhookSecret,
		validHeaders(t, payload, true), []byte(payload)); err != nil {
		t.Fatalf("十六进制签名应通过: %v", err)
	}
	if err := verifierAt(webhookNow).Verify(testWebhookSecret,
		validHeaders(t, payload, false), []byte(payload)); err == nil {
		t.Fatal("base64 编码的签名应被拒——文档口径是 hex")
	}
}

// 篡改载荷必须被拒。这是这个端点唯一的闸：它对公网开放，没有会话、没有 IP
// 限制，签名不过就等于任何人都能改我们的卡片投影。
func TestWebhookVerifierRejectsTamperedPayload(t *testing.T) {
	payload := `{"id":"x","event":"card.status_change","data":{"amount":"1"}}`
	h := validHeaders(t, payload, true)
	tampered := strings.Replace(payload, `"1"`, `"100000"`, 1)

	if err := verifierAt(webhookNow).Verify(testWebhookSecret, h, []byte(tampered)); err == nil {
		t.Fatal("载荷被改过必须拒绝")
	}
}

// 换一把密钥签的必须被拒。
func TestWebhookVerifierRejectsWrongSecret(t *testing.T) {
	payload := `{"id":"x"}`
	ts, id := "1788609600", "x"
	h := WebhookHeaders{
		Signature: signPayload(t, "another-secret", ts, id, payload, true),
		Timestamp: ts, EventID: id,
	}
	if err := verifierAt(webhookNow).Verify(testWebhookSecret, h, []byte(payload)); err == nil {
		t.Fatal("错误密钥签的必须拒绝")
	}
}

// 过老或来自未来的时间戳必须被拒（重放防护的第一道）。
//
// 文档没有规定容忍窗口，我们自己定 300 秒——与请求签名那一侧同一个数，
// 免得两处各有一个窗口、排查时要记两个数。
func TestWebhookVerifierRejectsStaleTimestamp(t *testing.T) {
	payload := `{"id":"x"}`
	for name, skew := range map[string]time.Duration{
		"太旧":   -webhookSkewTolerance - time.Second,
		"来自未来": webhookSkewTolerance + time.Second,
	} {
		at := webhookNow.Add(skew)
		ts := formatUnix(at)
		id := "x"
		h := WebhookHeaders{
			Signature: signPayload(t, testWebhookSecret, ts, id, payload, true),
			Timestamp: ts, EventID: id,
		}
		// 签名本身是对的，只有时间戳偏了——必须仍然拒绝。
		if err := verifierAt(webhookNow).Verify(testWebhookSecret, h, []byte(payload)); err == nil {
			t.Fatalf("%s 的时间戳必须拒绝", name)
		}
	}
}

// 缺任何一个头都必须拒，且不能因为缺头就跳过校验。
func TestWebhookVerifierRequiresAllHeaders(t *testing.T) {
	payload := `{"id":"x"}`
	full := validHeaders(t, payload, true)
	for name, h := range map[string]WebhookHeaders{
		"缺签名":    {Timestamp: full.Timestamp, EventID: full.EventID},
		"缺时间戳":   {Signature: full.Signature, EventID: full.EventID},
		"缺事件 id": {Signature: full.Signature, Timestamp: full.Timestamp},
	} {
		if err := verifierAt(webhookNow).Verify(testWebhookSecret, h, []byte(payload)); err == nil {
			t.Fatalf("%s 时必须拒绝", name)
		}
	}
}

// 空密钥必须拒绝，而不是拿空串去算 HMAC。
//
// 没配 webhook 密钥的部署，这个端点应当整个不可用——空串是一个所有人都
// 知道的密钥，用它验签等于不验。
func TestWebhookVerifierFailsClosedWithoutSecret(t *testing.T) {
	payload := `{"id":"x"}`
	h := validHeaders(t, payload, true)
	if err := verifierAt(webhookNow).Verify("", h, []byte(payload)); err == nil {
		t.Fatal("没有密钥时必须拒绝")
	}
}

// 事件信封要能解析出「哪个事件、哪张卡」——这两样决定后续做什么。
func TestParseWebhookEventReadsEnvelope(t *testing.T) {
	payload := []byte(`{
	  "id": "b7ef2c62-6177-4ea8-84ec-3080f2db58f0",
	  "event": "card.status_change",
	  "version": 1,
	  "occurred_at": 1763513200,
	  "data": {"card": {"card_id": "a441831c-a5c7-4bed-8f61-793738afd5bc",
	    "alias": "Travel card", "last_four": "1234", "status": "active", "currency": "USD"}}
	}`)

	ev, err := ParseWebhookEvent(payload, "hdr-1")
	if err != nil {
		t.Fatal(err)
	}
	// 事件 id 取自请求头（载荷里的 id 是卡片信封才有的，订单事件没有）。
	if ev.ID != "hdr-1" || ev.Type != "card.status_change" {
		t.Fatalf("信封解析错: %+v", ev)
	}
	if ev.CardID != "a441831c-a5c7-4bed-8f61-793738afd5bc" {
		t.Fatalf("卡 id = %q", ev.CardID)
	}
	if ev.OccurredAt.IsZero() {
		t.Fatal("occurred_at 应解析成时刻")
	}
}

// 交易事件同样只取「哪张卡」——金额一律不从回调里读。
//
// 回调签名只证明来源，不证明我们对字段形状的理解是对的：卡 API 文档里
// 交易类型写作 "Consume"、状态写作 "Completed"，webhook 文档里却是
// "consume" / "authorized"，同一个概念两套大小写。把金额按字面写进账，
// 一处理解错就是账目错。回调只当触发器，金额永远以主动读取为准。
func TestParseWebhookEventOnTransactionTakesOnlyCardID(t *testing.T) {
	payload := []byte(`{"id":"f72f6cd7","event":"card.transaction","version":1,
	  "occurred_at":1786417200,
	  "data":{"card":{"card_id":"c-1"},"transaction_id":"t-1","type":"consume",
	    "status":"authorized","amount":"10","currency":"USD"}}`)

	ev, err := ParseWebhookEvent(payload, "hdr-1")
	if err != nil {
		t.Fatal(err)
	}
	if ev.CardID != "c-1" || ev.Type != "card.transaction" {
		t.Fatalf("%+v", ev)
	}
}

// 认不出的事件类型不算错误，但要能被识别出「不用处理」。
// 上游以后加新事件类型时，我们应当安静忽略而不是每次都报错刷屏。
func TestParseWebhookEventMarksUnknownTypes(t *testing.T) {
	ev, err := ParseWebhookEvent([]byte(`{"id":"x","event":"order.completed","data":{}}`), "hdr-1")
	if err != nil {
		t.Fatal(err)
	}
	if ev.NeedsCardRefresh() {
		t.Fatal("订单类事件不该触发卡片刷新")
	}
}

// 缺事件类型或不是 JSON 的载荷必须报错。
func TestParseWebhookEventRejectsIncomplete(t *testing.T) {
	for name, payload := range map[string]string{
		"缺事件类型":   `{"data":{"card":{"card_id":"c"}}}`,
		"不是 JSON": `not json`,
	} {
		if _, err := ParseWebhookEvent([]byte(payload), "hdr-1"); err == nil {
			t.Fatalf("%s 应报错", name)
		}
	}
}

// 卡片事件缺 card_id 必须报错——没有它就不知道该刷新哪张卡，
// 静默通过会让一次真实的状态变更被丢掉。
func TestParseWebhookEventRequiresCardIDForCardEvents(t *testing.T) {
	if _, err := ParseWebhookEvent([]byte(`{"id":"x","event":"card.status_change","data":{}}`), "hdr-1"); err == nil {
		t.Fatal("卡片事件缺 card_id 应报错")
	}
}

// 事件 id 以**请求头**为准，不是载荷里的 id。
//
// 文档明写「Use X-Webhook-Event-Id for idempotency」。更要紧的是订单/订阅
// 事件的信封是扁平的、**根本没有 id 字段**（只有 event、order_id…），
// 而卡片事件用的是带 id 的版本化信封。要求载荷里必须有 id，会让订阅了
// 全部事件的端点在每个订单事件上回 400，然后被上游重试 8 次——噪音很大，
// 而且掩盖真正的失败。
func TestParseWebhookEventUsesHeaderEventID(t *testing.T) {
	flat := []byte(`{"event":"order.completed","order_id":"o-1","status":"paid"}`)

	ev, err := ParseWebhookEvent(flat, "hdr-evt-1")
	if err != nil {
		t.Fatalf("扁平信封（订单事件）不该报错: %v", err)
	}
	if ev.ID != "hdr-evt-1" {
		t.Fatalf("事件 id 应取自请求头, got %q", ev.ID)
	}
	if ev.NeedsCardRefresh() {
		t.Fatal("订单事件不该触发卡片刷新")
	}
}

// 请求头缺事件 id 时必须报错：它是去重的唯一依据。
func TestParseWebhookEventRequiresHeaderEventID(t *testing.T) {
	if _, err := ParseWebhookEvent([]byte(`{"event":"card.status_change"}`), ""); err == nil {
		t.Fatal("缺请求头事件 id 必须报错")
	}
}

// 诊断必须能分开「密钥解释方式错了」与「密钥值不对」。
//
// 两者的失败症状一模一样（都是签名不匹配），而修法相反：一个改代码，
// 一个让人重填密钥。上线当天在这上面绕了很久，所以把判据本身也钉住。
func TestDiagnoseSecretFormTellsTheTwoCausesApart(t *testing.T) {
	// 44 字符的 base64，与真实 webhook 密钥同形。
	secret := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	content := []byte("1788609600.evt-1.{}")

	decoded, _ := base64.StdEncoding.DecodeString(secret)
	if got := diagnoseSecretForm(secret, content, webhookMAC(decoded, content)); !strings.Contains(got, "解码后的密钥算能对上") {
		t.Fatalf("按解码密钥能对上时应如实指出, got %q", got)
	}

	// base64url 字母表：真实密钥里出现过 `_`，标准解码器会拒绝它。
	// 第一版诊断只试标准 base64，于是把这种情况误报成「不是 base64」。
	urlSecret := base64.URLEncoding.EncodeToString([]byte("ÿþ0123456789abcdef0123456789abcd"))
	urlDecoded, err := base64.URLEncoding.DecodeString(urlSecret)
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnoseSecretForm(urlSecret, content, webhookMAC(urlDecoded, content)); !strings.Contains(got, "base64url") {
		t.Fatalf("base64url 密钥应被识别出来, got %q", got)
	}

	if got := diagnoseSecretForm(secret, content, []byte("完全不相干")); !strings.Contains(got, "密钥值本身") {
		t.Fatalf("都对不上时应指向密钥值, got %q", got)
	}

	// 完全不是 base64 的密钥：如实说只能按原文用。
	if got := diagnoseSecretForm("not base64!!", content, nil); !strings.Contains(got, "只可能按原文用") {
		t.Fatalf("非 base64 密钥应如实说明, got %q", got)
	}
}

// 交易类回调要能解析出交易身份与语义。
//
// 五种形态来自官方 webhook 文档：授权、结算（带 auth_settle_adjustment）、
// 授权失败、冲正、退款。全部只当触发器——金额一律以主动读取为准——但
// transaction_id 要留下来：REST 的流水接口**不给交易 id**，去重键是我们
// 自己派生的；回调给了真 id，存下来才能把两边对上。
func TestParseWebhookEventReadsTransactionIdentity(t *testing.T) {
	cases := map[string]struct {
		payload  string
		wantType string
		wantStat string
		wantTxID string
		wantRel  string
	}{
		"授权": {
			payload: `{"event":"card.transaction","data":{"card":{"card_id":"c-1"},
			  "transaction_id":"t-1","type":"consume","status":"authorized","direction":"debit"}}`,
			wantType: "consume", wantStat: "authorized", wantTxID: "t-1",
		},
		"结算": {
			payload: `{"event":"card.transaction","data":{"card":{"card_id":"c-1"},
			  "transaction_id":"t-1","type":"consume","status":"completed","direction":"debit",
			  "auth_settle_adjustment":{"authorized_amount":"10","settled_amount":"8"}}}`,
			wantType: "consume", wantStat: "completed", wantTxID: "t-1",
		},
		"授权失败": {
			payload: `{"event":"card.transaction","data":{"card":{"card_id":"c-1"},
			  "transaction_id":"t-2","type":"consume","status":"failed","direction":"none",
			  "failure":{"reason":"Insufficient balance"}}}`,
			wantType: "consume", wantStat: "failed", wantTxID: "t-2",
		},
		"冲正": {
			payload: `{"event":"card.transaction","data":{"card":{"card_id":"c-1"},
			  "transaction_id":"t-3","related_transaction_id":"t-1","type":"reversal",
			  "status":"completed","direction":"credit"}}`,
			wantType: "reversal", wantStat: "completed", wantTxID: "t-3", wantRel: "t-1",
		},
		"退款": {
			payload: `{"event":"card.transaction","data":{"card":{"card_id":"c-1"},
			  "transaction_id":"t-4","related_transaction_id":"t-1","type":"refund",
			  "status":"completed","direction":"credit"}}`,
			wantType: "refund", wantStat: "completed", wantTxID: "t-4", wantRel: "t-1",
		},
	}

	for name, tc := range cases {
		ev, err := ParseWebhookEvent([]byte(tc.payload), "hdr-"+name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !ev.NeedsCardRefresh() {
			t.Fatalf("%s: 交易事件应触发定向刷新", name)
		}
		if ev.TransactionID != tc.wantTxID || ev.RelatedTransactionID != tc.wantRel {
			t.Fatalf("%s: 交易 id 解析错 %+v", name, ev)
		}
		if ev.TransactionType != tc.wantType || ev.TransactionStatus != tc.wantStat {
			t.Fatalf("%s: 类型/状态解析错 %+v", name, ev)
		}
	}
}

// 状态变更事件没有交易字段，解析出来该是空的，不能编造。
func TestParseWebhookEventLeavesTransactionFieldsEmptyOnStatusChange(t *testing.T) {
	ev, err := ParseWebhookEvent([]byte(
		`{"event":"card.status_change","data":{"card":{"card_id":"c-1","status":"suspend"}}}`), "hdr-1")
	if err != nil {
		t.Fatal(err)
	}
	if ev.TransactionID != "" || ev.TransactionType != "" {
		t.Fatalf("状态变更不该有交易字段: %+v", ev)
	}
}

// 3DS 挑战事件：解析出挑战身份与过期时间。
//
// **验证码本身可能不在载荷里。** 官方 webhook 文档的示例带 "challenge":"123456"，
// 但 2026-09-05 生产收到的真实事件里只有 challenge_id / challenge_type /
// expires_at，没有验证码。所以这里把验证码当作**可选**：有就存下来给页面用，
// 没有就只提示「有一笔待验证」——按文档示例假定它一定存在，会做出一个
// 永远显示空白的验证码栏位。
func TestParseWebhookEventReadsChallenge(t *testing.T) {
	withCode := `{"event":"card.challenge","data":{"card":{"card_id":"c-1"},
	  "challenge_id":"ch-1","challenge_type":"authorization_code",
	  "challenge":"123456","expires_at":1763513700}}`

	ev, err := ParseWebhookEvent([]byte(withCode), "hdr-1")
	if err != nil {
		t.Fatal(err)
	}
	if ev.ChallengeID != "ch-1" || ev.ChallengeType != "authorization_code" {
		t.Fatalf("挑战身份解析错: %+v", ev)
	}
	if ev.ChallengeCode != "123456" {
		t.Fatalf("验证码没解析出来: %+v", ev)
	}
	if ev.ChallengeExpiresAt.IsZero() {
		t.Fatalf("过期时间没解析出来: %+v", ev)
	}
	// 挑战事件不该触发卡片刷新：它与卡的状态、余额都无关。
	if ev.NeedsCardRefresh() {
		t.Fatal("挑战事件不该触发定向刷新")
	}
}

// 真实生产事件的形状：没有验证码字段，其余照常解析。
func TestParseWebhookEventToleratesChallengeWithoutCode(t *testing.T) {
	real := `{"event":"card.challenge","data":{"card":{"card_id":"c-1","status":"active"},
	  "challenge_id":"c6bdb09b-037c-51f6-ad8b-ff1eb16acfa6",
	  "challenge_type":"authorization_code","expires_at":1788553063}}`

	ev, err := ParseWebhookEvent([]byte(real), "hdr-2")
	if err != nil {
		t.Fatalf("没有验证码的挑战事件不该报错: %v", err)
	}
	if ev.ChallengeID == "" || ev.ChallengeExpiresAt.IsZero() {
		t.Fatalf("其余字段仍应解析: %+v", ev)
	}
	if ev.ChallengeCode != "" {
		t.Fatalf("没有验证码时不能编一个出来: %+v", ev)
	}
}
