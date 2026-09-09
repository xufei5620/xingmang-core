package infini

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 脱敏是**缺席型断言**，最容易写成恒真：夹具里本来就没有卡号时，
// 「不含卡号」永远成立。所以每一条都配一条正向断言（脱敏器确实跑过、
// 并且产出了预期的掩码形态），两个方向合起来才算判据在判事。
//
// 变异验证见 handoff 的变异表：把 redactUpstreamText 改成 return s
// 与改成 return "" 各跑一次，两组断言分别发红。
func TestRedactUpstreamTextRemovesCredentialsAndMasksCardNumbers(t *testing.T) {
	cases := map[string]struct {
		in      string
		absent  []string
		present []string
	}{
		"卡号留末 4 位": {
			in:      "invalid card_number 5332281234561234 for this product",
			absent:  []string{"5332281234561234", "533228"},
			present: []string{"****1234", "invalid card_number", "for this product"},
		},
		"分组排版的卡号也要打掉": {
			in:      "declined for 5332 2812 3456 1234",
			absent:  []string{"5332 2812 3456 1234", "3456 1234"},
			present: []string{"****1234"},
		},
		"密钥整个去掉而不是打码": {
			in:      "signature mismatch for api_key sk-live-9f2a7c31",
			absent:  []string{"sk-live-9f2a7c31", "9f2a7c31"},
			present: []string{redactedMarker, "api_key"},
		},
		"CVV 不留": {
			in:      "bad cvv 123 supplied",
			absent:  []string{"cvv 123"},
			present: []string{redactedMarker},
		},
		"JSON 形状的 token": {
			in:      `{"code":40001,"token":"abcdef123456","message":"nope"}`,
			absent:  []string{"abcdef123456"},
			present: []string{redactedMarker, "40001", "nope"},
		},
		// 全角冒号：本仓库的上游文案与注释按约定用全角标点，而只认 ASCII
		// 冒号的掩码在这个仓库里已经漏过一次（记忆「绝不查看密钥文件」）。
		"全角冒号的凭据键": {
			in:      `上游拒绝，"密钥"：不重要，token：sk-live-deadbeef99`,
			absent:  []string{"sk-live-deadbeef99", "deadbeef99"},
			present: []string{redactedMarker, "上游拒绝"},
		},
		"没有敏感内容时原样保留": {
			in:      "card_id not in batch scope",
			absent:  []string{redactedMarker},
			present: []string{"card_id not in batch scope"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := redactUpstreamText(tc.in)
			// **先查缺席再查存在**：顺序反过来的话，把脱敏器改成恒等函数
			// 时会先在「存在」那一组停下，缺席那一组一次都没被执行到，
			// 于是变异验证证明不了缺席断言真的在判事。
			for _, bad := range tc.absent {
				if strings.Contains(got, bad) {
					t.Errorf("脱敏后不该出现 %q，实际 %q", bad, got)
				}
			}
			for _, want := range tc.present {
				if !strings.Contains(got, want) {
					t.Errorf("脱敏后应保留 %q，实际 %q", want, got)
				}
			}
		})
	}
}

// 先脱敏再截断，不是反过来。
//
// 反过来的话，一个正好跨在 300 字节边界上的卡号会被切成两截，
// 两截都短于 12 位于是都逃过 digitRun——一条只在长响应体上才发作的泄漏。
func TestBodyPrefixRedactsBeforeTruncating(t *testing.T) {
	// 让卡号的头几位落在 300 字节以内、尾几位落在边界之外。
	const pan = "5332281234561234"
	padding := strings.Repeat("x", bodyPrefixLimit-len(pan)+8)
	raw := []byte(padding + pan + " tail")

	got := bodyPrefix(raw)
	if strings.Contains(got, pan) {
		t.Fatalf("跨截断边界的卡号漏了: %q", got)
	}
	if strings.Contains(got, "3456") {
		t.Fatalf("卡号的中段不该出现: %q", got)
	}
	if !strings.Contains(got, "****1234") {
		t.Fatalf("应看到掩码形态（否则说明只是被截断切没了，不是被脱敏）: %q", got)
	}
}

// 上游 200 + 非零 code：对外错误文本里必须同时看得见码与脱敏后的 message，
// 但一个字节的卡号/凭据都不许出现。
//
// 这两件事必须在同一条用例里断言：只断言「看得见」会让脱敏被顺手删掉，
// 只断言「看不见」会让整个 Detail 被删掉之后仍然全绿。
func TestDoSurfacesRedactedUpstreamMessage(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":30013,"message":"invalid card_number 5332281234561234 (cvv 123) for api_key sk-live-9f2a7c31","data":null}`))
	})

	err := c.do(context.Background(), http.MethodPost, "/v2/cards/apply", []byte(`{}`), nil)
	if err == nil {
		t.Fatal("code != 0 必须报错")
	}
	text := err.Error()

	// 缺席在前，理由同上：两个方向要能被两次不同的变异分别打红。
	for _, bad := range []string{"5332281234561234", "533228", "sk-live-9f2a7c31", "cvv 123"} {
		if strings.Contains(text, bad) {
			t.Errorf("对外错误文本不该含 %q，实际 %q", bad, text)
		}
	}
	for _, want := range []string{"30013", "invalid card_number", "****1234", "rejected"} {
		if !strings.Contains(text, want) {
			t.Errorf("对外错误文本应含 %q，实际 %q", want, text)
		}
	}
}

// 非 2xx 走的是 bodyPrefix 那条路，与 message 那条路是**两个入口**。
// 只给 message 脱敏、漏掉响应体，泄漏面还整个留着。
func TestDoRedactsRawBodyOnHTTPError(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`<html>upstream error for card 5332281234561234</html>`))
	})

	err := c.do(context.Background(), http.MethodGet, "/v2/cards/list", nil, nil)
	if err == nil {
		t.Fatal("502 必须报错")
	}
	text := err.Error()
	if !strings.Contains(text, "502") {
		t.Fatalf("状态码仍要可诊断: %q", text)
	}
	if !strings.Contains(text, "****1234") {
		t.Fatalf("响应体里的卡号应被打成掩码而不是整段消失: %q", text)
	}
	if strings.Contains(text, "5332281234561234") {
		t.Fatalf("响应体里的卡号漏进对外文本: %q", text)
	}
	// 原文仍留在 Unwrap 链里供服务端排查（ADR-004 禁的是透传给用户）。
	if chain := unwrapAll(err); !strings.Contains(chain, "5332281234561234") {
		t.Fatalf("Unwrap 链里应保留原始响应体, got %q", chain)
	}
}

// 本地前置拒绝与上游拒绝必须能分辨。
//
// 两者的 Kind 都是 rejected、op 逐字相同；此前对外文本一模一样，
// 运维分不出是谁说的「不」。
func TestLocalPreRejectionIsDistinguishableFromUpstreamRejection(t *testing.T) {
	var called bool
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"code":30013,"message":"card_id not in batch scope","data":null}`))
	})

	tooMany := make([]string, BatchStatusMax+1)
	for i := range tooMany {
		tooMany[i] = "card_x"
	}
	_, localErr := c.BatchCardStatus(context.Background(), tooMany)
	if localErr == nil {
		t.Fatal("超过上限必须本地拒绝")
	}
	if called {
		t.Fatal("注定被拒的请求不该发出去")
	}
	if connector.KindOf(localErr) != connector.KindRejected {
		t.Fatalf("本地前置拒绝的分类 = %q, want rejected", connector.KindOf(localErr))
	}
	if !strings.Contains(localErr.Error(), "平台侧前置拒绝") {
		t.Fatalf("本地拒绝要说清是平台侧拒的: %q", localErr.Error())
	}
	if !strings.Contains(localErr.Error(), "101") {
		t.Fatalf("本地拒绝要说清收到了多少张: %q", localErr.Error())
	}
	if strings.Contains(localErr.Error(), "upstream code") {
		t.Fatalf("本地拒绝不该看起来像上游拒绝: %q", localErr.Error())
	}

	_, upstreamErr := c.BatchCardStatus(context.Background(), []string{"card_1"})
	if upstreamErr == nil {
		t.Fatal("上游 code != 0 必须报错")
	}
	if !called {
		t.Fatal("正常张数的请求应该真的发出去（否则上一段的 called==false 是恒真）")
	}
	if !strings.Contains(upstreamErr.Error(), "upstream code 30013") {
		t.Fatalf("上游拒绝要带上游码: %q", upstreamErr.Error())
	}
	if strings.Contains(upstreamErr.Error(), "平台侧") {
		t.Fatalf("上游拒绝不该被标成平台侧: %q", upstreamErr.Error())
	}
}

// 其余六个连接器共用 connector.Error：NewError 的对外文本必须一个字节没变。
func TestNewErrorTextUnchangedForOtherConnectors(t *testing.T) {
	err := connector.NewError(connector.KindRejected, "sub2api.users.read",
		context.Canceled)
	if got, want := err.Error(), "rejected: sub2api.users.read"; got != want {
		t.Fatalf("NewError 的对外文本 = %q, want %q（改了它就等于改了另外六个连接器）", got, want)
	}
}
