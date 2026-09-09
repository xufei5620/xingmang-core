package infini

// 脱敏器的第二组用例（XM-CARD-VISIBILITY 复审补丁）。
//
// 第一组（redact_test.go）只证明了「卡号与紧贴标签的密钥会被去掉」。
// 复审在真实形状上打出四个洞，这一组逐个把它们钉住：
//
//  1. 复合字段名（access_token / client_secret / x_api_key）整词匹配漏过；
//  2. `Authorization: Bearer <token>` 只吃掉 scheme，token 完整留下；
//  3. 键与值跨换行时整对逃过脱敏，随后压平又把它排成一行可读文本；
//  4. message 那条路没有长度上限。
//
// 外加一条**正向**用例：诊断散文必须活下来。第一组一条正向用例都没有，
// 于是「把答案一起吃掉」这种坏法在全绿的门禁下过了关。

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

// 复合字段名必须命中：整词匹配在这里一条都挡不住。
//
// `\btoken\b` 在 `access_token` 里因为下划线是词字符而根本匹配不到——
// 荒唐的是 `x-api-key`（连字符）挡得住而 `x_api_key`（下划线）挡不住，
// 防线成不成立取决于上游用了哪个分隔符。
func TestRedactCatchesCompoundCredentialKeys(t *testing.T) {
	cases := map[string]string{
		`{"access_token":"sk-live-aaa111"}`:  "sk-live-aaa111",
		`{"client_secret":"csec-bbb222"}`:    "csec-bbb222",
		`{"refresh_token":"rt-ccc333"}`:      "rt-ccc333",
		`{"app_secret":"as-ddd444"}`:         "as-ddd444",
		`{"x_api_key":"xk-fff666"}`:          "xk-fff666",
		`{"X-Api-Key":"XK-GGG777"}`:          "XK-GGG777",
		`keyId="ak_live_7fd21"`:              "ak_live_7fd21",
		`{"client_id":"cid-hhh888"}`:         "cid-hhh888",
		`{"passphrase":"correct-horse-42"}`:  "correct-horse-42",
		`{"cookie":"session=abcdef123456"}`:  "abcdef123456",
		`{"dsn":"postgres://u:pw@h/db"}`:     "postgres://u:pw@h/db",
		`{"private_key":"-----BEGIN-xyz"}`:   "BEGIN-xyz",
		`{"credential":"cred-iii999"}`:       "cred-iii999",
		`{"authorization":"tok-jjj000"}`:     "tok-jjj000",
		`{"password":"hunter2hunter2"}`:      "hunter2hunter2",
		`{"apikey":"ak-kkk111"}`:             "ak-kkk111",
		`{"WEBHOOK_SECRET":"whsec-lll222"}`:  "whsec-lll222",
		`{"digest":"SHA-256=abcdefgh1234"}`:  "abcdefgh1234",
		`{"secret_key":"skey-mmm333"}`:       "skey-mmm333",
		`{"user_password_hash":"pbkdf2$xy"}`: "pbkdf2$xy",
	}
	for in, leaked := range cases {
		t.Run(in, func(t *testing.T) {
			got := redactUpstreamText(in)
			if strings.Contains(got, leaked) {
				t.Errorf("凭据值漏进对外文本: 输入 %q 得到 %q", in, got)
			}
			if !strings.Contains(got, redactedMarker) {
				t.Errorf("没有看到掩码标记，说明脱敏器根本没跑: %q", got)
			}
		})
	}
}

// 本包的键名清单必须是 audit 那一份的**超集**。
//
// 这条不是抄写核对，是遍历：audit.SensitiveKeyFragments() 加了一项而这边
// 漏了，这里当场红。手列一份「我覆盖了哪些」的清单是没有用的——那样两份
// 清单会一起漂，而门禁读的是自己那一份，于是永远绿（记忆「闸的范围要发现
// 不要手列」）。
func TestRedactCoversAuditSensitiveKeys(t *testing.T) {
	fragments := audit.SensitiveKeyFragments()
	if len(fragments) == 0 {
		t.Fatal("audit 的清单是空的：这条门禁会恒真")
	}
	for _, fragment := range fragments {
		t.Run(fragment, func(t *testing.T) {
			// 造一个**带前缀**的复合键名：整词匹配的实现会在这里漏，
			// 子串匹配的不会。
			in := fmt.Sprintf(`{"x_%s":"leaked-value-9f2a7c31"}`, fragment)
			got := redactUpstreamText(in)
			if strings.Contains(got, "leaked-value-9f2a7c31") {
				t.Errorf("audit 认为敏感的键名 %q 在这里没挡住: %q", fragment, got)
			}
		})
	}
}

// scheme 后面那个 token 才是凭据本身。
//
// 第一版只吃掉 `Bearer` 这个词，产出
// `Authorization: [REDACTED] sk-live-9f8a7b6c5d4e`——一个把凭据摆在
// 掩码标记旁边的形状，读日志的人会以为这一段已经处理过了。
func TestRedactEatsCredentialAfterAuthScheme(t *testing.T) {
	cases := []struct{ in, leaked string }{
		{"Authorization: Bearer sk-live-9f8a7b6c5d4e", "sk-live-9f8a7b6c5d4e"},
		{"Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA=="},
		{"retry using Bearer sk-live-deadbeef1234 instead", "sk-live-deadbeef1234"},
		{`{"headers":{"Authorization":"Bearer eyJhbGciOiJIUzI1NiJ9.x"}}`, "eyJhbGciOiJIUzI1NiJ9.x"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := redactUpstreamText(tc.in)
			if strings.Contains(got, tc.leaked) {
				t.Errorf("scheme 后面的凭据漏了: %q", got)
			}
			if !strings.Contains(got, redactedMarker) {
				t.Errorf("没有掩码标记: %q", got)
			}
		})
	}
}

// 本连接器自己的签名头形状：HMAC 签名与 keyId 都不许留下。
//
// 用 signing.go 真正会发出去的那种 Authorization 头当夹具（上游把请求头
// 回显进错误体是常见做法）。第一版把字面量 `Signature` 这个词当成凭据吃掉，
// 而 keyId 原样留在旁边——被吃掉的是词，漏掉的是凭据标识。
func TestRedactHandlesSignatureHeaderShape(t *testing.T) {
	in := `{"message":"client request can't be validated","echo":{"Authorization":"Signature keyId=\"ak_live_7fd21\",algorithm=hmac-sha256,signature=\"Zm9vYmFyYmF6\""}}`
	got := redactUpstreamText(in)
	for _, leaked := range []string{"ak_live_7fd21", "Zm9vYmFyYmF6"} {
		if strings.Contains(got, leaked) {
			t.Errorf("签名头里的 %q 漏进对外文本: %q", leaked, got)
		}
	}
	// 正向：网关那句话必须活下来，kindForUnauthorized 就是靠它分流的。
	if !strings.Contains(got, "client request can't be validated") {
		t.Errorf("网关文案被吃掉了，401 就分不出是签名还是 IP: %q", got)
	}
	if !strings.Contains(got, "hmac-sha256") {
		t.Errorf("算法名不是凭据，不该被吃掉: %q", got)
	}
}

// **正向断言**：诊断散文必须逐字活下来。
//
// 这是整组用例里唯一能防住「脱敏器把答案一起吃掉」的东西。第一版把
// 「关键词 + 空白 + 下一个词」里的下一个词无条件当凭据吃掉，于是运维拿到的是
// `upstream code 401: authorization [REDACTED] for account LINFENG`——
// 比三天前那 40 个字符只多了一个词，答案那一格还是空的。
// token / secret / signature / authorization 恰恰是拒绝类 message 里最高频的词。
func TestRedactKeepsDiagnosticProse(t *testing.T) {
	cases := []string{
		"signature mismatch for request 88213",
		"token expired at 2026-09-08T00:00:00Z",
		"authorization denied for account LINFENG",
		"secret rotation required before 2026-10-01",
		"api_key disabled by administrator",
		"card_id not in batch scope",
		"password policy not satisfied",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got := redactUpstreamText(in)
			if got != in {
				t.Errorf("诊断原话被改动了:\n原文 %q\n实际 %q", in, got)
			}
		})
	}
}

// 键与值跨换行时不许漏。
//
// 两条分隔符字符类都不含 `\n`，于是美化过的 JSON 与网关错误页（响应体最
// 常见的两种形状）里的键值对整个逃过脱敏，随后压平换行又把漏出来的密钥
// 排成一行漂亮的可读文本。压平必须发生在脱敏**之前**。
func TestRedactFlattensBeforeMatching(t *testing.T) {
	cases := []string{
		"{\n  \"error\": \"denied\",\n  \"api_key\":\n    \"sk-live-CAFEBABE1234\"\n}",
		"upstream said:\n\ttoken\n\t=\n\tsk-live-CAFEBABE1234",
		"<html>\n<body>\nAuthorization: Bearer\nsk-live-CAFEBABE1234\n</body>\n</html>",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got := redactUpstreamText(in)
			if strings.Contains(got, "sk-live-CAFEBABE1234") {
				t.Errorf("跨换行的键值对漏了脱敏: %q", got)
			}
			if strings.ContainsAny(got, "\n\r") {
				t.Errorf("输出里不该还有换行: %q", got)
			}
		})
	}
}

// 上游 message 必须与响应体走同一个长度上限。
//
// 没有上限的话，一个话痨（或被打崩）的上游能让这段文本每 5 分钟落一次
// ops 观测的 value_json、进 alerts.detail（无上限的 text 列），最后撑爆
// Telegram sendMessage 的 4096 字符上限——而那条告警正是本片用来替换
// 「作业变红」这个响亮信号的东西。
func TestUpstreamMessageIsLengthCapped(t *testing.T) {
	long := strings.Repeat("A", 200_000)
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"code":30013,"message":%q,"data":null}`, long)
	})

	err := c.do(context.Background(), http.MethodPost, "/v2/cards/apply", []byte(`{}`), nil)
	if err == nil {
		t.Fatal("code != 0 必须报错")
	}
	if n := len(err.Error()); n > 2*bodyPrefixLimit {
		t.Fatalf("对外错误文本 %d 字节，上限应与响应体同档（%d）", n, bodyPrefixLimit)
	}
	// 正向：截断不能把诊断截没了。
	if !strings.Contains(err.Error(), "30013") {
		t.Fatalf("业务码要活下来: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "…(截断)") {
		t.Fatalf("截断要留痕，否则读的人不知道后面还有内容: %q", err.Error())
	}
}

// 上游 message 的长度上限对 switchOp / DeleteCard 那两条路同样成立。
//
// 它们不经过 do() 的信封分支，各自拼自己的 Detail——三个入口只改两个，
// 剩下那个就是下一次泄漏/撑爆的入口。
func TestUpstreamRefusalMessageIsLengthCapped(t *testing.T) {
	long := strings.Repeat("B", 100_000)
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w,
			`{"code":0,"message":"ok","data":{"success":false,"message":%q}}`, long)
	})

	for name, call := range map[string]func() error{
		"freeze": func() error { return c.FreezeCard(context.Background(), "card_1") },
		"delete": func() error { return c.DeleteCard(context.Background(), "card_1") },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("success=false 必须报错")
			}
			if n := len(err.Error()); n > 2*bodyPrefixLimit {
				t.Fatalf("对外错误文本 %d 字节，没有走同一个上限", n)
			}
		})
	}
}

// 对外文本的入口**只有** safeUpstreamText 一个。
//
// 这条读的是源码而不是行为，理由是行为测不出「下一个人会不会绕过去」：
// 长度上限此前只挂在响应体那条路上，另外三个 message 入口各自拼 Detail、
// 各自忘了截断，而每一条都全绿。把「谁能碰未截断的那一份」变成一条
// 物理约束，比再写三条针对具体入口的用例更耐得住新增第四个入口。
func TestOnlySafeUpstreamTextReachesOutwardText(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if name == "redact.go" {
			continue
		}
		if strings.Contains(string(src), "redactUpstreamText(") {
			t.Errorf("%s 直接调了 redactUpstreamText：它不截断，"+
				"对外文本必须走 safeUpstreamText", name)
		}
	}
	// 防恒真：扫到的文件数为 0 时上面的循环什么也没判。
	if scanned < 3 {
		t.Fatalf("只扫到 %d 个非测试源文件，这条门禁没有在判事", scanned)
	}
}
