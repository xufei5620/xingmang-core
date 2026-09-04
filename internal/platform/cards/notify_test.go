package cards

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func notifyNow() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }

// 3DS 验证码要出现在推送正文里。
//
// 产品负责人 2026-09-05 拍板全部推送，**明知**群机器人的消息群里所有人可见。
// 理由是验证码只有几分钟有效，页面徽章对"正在付款的人"没有用——他不可能
// 同时盯着后台刷新。所以这条推送的价值就在于把码直接送到手上。
func TestFormatChallengeIncludesCode(t *testing.T) {
	got := FormatNotification(Notification{
		Kind: NotifyChallenge, Account: "LINFENG", CardMask: "****9228",
		ChallengeCode: "123456", ExpiresAt: notifyNow().Add(3 * time.Minute),
	}, notifyNow())

	for _, want := range []string{"验证码", "123456", "LINFENG", "9228"} {
		if !strings.Contains(got, want) {
			t.Fatalf("推送正文应含 %q: %s", want, got)
		}
	}
	// 剩余有效期要写出来：一条过期的验证码和一条有效的长得一样，
	// 而人看到消息时往往已经过了一分钟。
	if !strings.Contains(got, "分钟") {
		t.Fatalf("应给出剩余有效期: %s", got)
	}
}

// 上游没给验证码时（生产实测就有这种），推送要说清楚去哪儿看，
// 而不是推一条空的"验证码："。
func TestFormatChallengeWithoutCodeTellsWhereToLook(t *testing.T) {
	got := FormatNotification(Notification{
		Kind: NotifyChallenge, Account: "LINFENG", CardMask: "****9228",
		ExpiresAt: notifyNow().Add(3 * time.Minute),
	}, notifyNow())

	if strings.Contains(got, "验证码：") || strings.Contains(got, "验证码:") {
		t.Fatalf("没有码时不该留一个空的验证码字段: %s", got)
	}
	if !strings.Contains(got, "待验证") {
		t.Fatalf("应提示有一笔待验证: %s", got)
	}
}

// 消费通知要带上够判断"这笔是不是我"的信息：商户、金额、状态。
func TestFormatTransactionCarriesMerchantAndAmount(t *testing.T) {
	got := FormatNotification(Notification{
		Kind: NotifyTransaction, Account: "CHRIS", CardMask: "****2650",
		Merchant: "Amazon DE", Amount: "10.50", Currency: "USD",
		TransactionType: "consume", TransactionStatus: "authorized",
	}, notifyNow())

	for _, want := range []string{"Amazon DE", "10.50", "USD", "消费"} {
		if !strings.Contains(got, want) {
			t.Fatalf("推送正文应含 %q: %s", want, got)
		}
	}
}

// 失败的授权要显眼——余额不足这类是要立刻处理的。
func TestFormatTransactionHighlightsFailure(t *testing.T) {
	got := FormatNotification(Notification{
		Kind: NotifyTransaction, Account: "CHRIS", CardMask: "****2650",
		TransactionStatus: "failed", FailureReason: "Insufficient balance",
	}, notifyNow())

	if !strings.Contains(got, "失败") {
		t.Fatalf("失败要说出来: %s", got)
	}
	if !strings.Contains(got, "Insufficient balance") {
		t.Fatalf("失败原因来自上游，直接转述有用: %s", got)
	}
}

// 卡号只推掩码。**完整卡号绝不进推送**——群机器人的消息会留在聊天记录里，
// 而那不是我们能控制的存储。
func TestFormatNeverIncludesFullPAN(t *testing.T) {
	got := FormatNotification(Notification{
		Kind: NotifyStatusChange, Account: "CHRIS",
		CardMask: "****2650", Status: "suspend",
	}, notifyNow())

	if strings.Contains(got, "4413576524979228") {
		t.Fatal("完整卡号绝不能进推送")
	}
	if !strings.Contains(got, "已锁定") {
		t.Fatalf("状态要翻成中文: %s", got)
	}
}

// 推送真的发得出去，且失败时如实报错。
func TestWeComNotifierPostsMarkdown(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	n := &WeComNotifier{endpoint: func(context.Context) (string, error) { return srv.URL, nil }, client: srv.Client()}
	if err := n.Notify(context.Background(), Notification{
		Kind: NotifyChallenge, Account: "LINFENG", CardMask: "****9228", ChallengeCode: "123456",
	}); err != nil {
		t.Fatal(err)
	}
	if got["msgtype"] != "markdown" {
		t.Fatalf("应发 markdown 消息: %v", got)
	}
}

// 上游返回业务错误码时要报错，不能因为 HTTP 200 就当成功——
// 企微的错误是放在响应体里的。
func TestWeComNotifierFailsOnBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook url"}`))
	}))
	defer srv.Close()

	n := &WeComNotifier{endpoint: func(context.Context) (string, error) { return srv.URL, nil }, client: srv.Client()}
	err := n.Notify(context.Background(), Notification{Kind: NotifyChallenge, Account: "X"})
	if err == nil {
		t.Fatal("errcode 非 0 必须报错——HTTP 200 不代表送达")
	}
	// 错误里不该带 webhook 地址：它本身就是凭据。
	if strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("错误信息泄漏了 webhook 地址: %v", err)
	}
}

func TestWeComNotifierReportsCredentialFailure(t *testing.T) {
	n := &WeComNotifier{endpoint: func(context.Context) (string, error) {
		return "", errors.New("凭据未配置")
	}}
	if err := n.Notify(context.Background(), Notification{Kind: NotifyChallenge}); err == nil {
		t.Fatal("取不到凭据必须报错")
	}
}
