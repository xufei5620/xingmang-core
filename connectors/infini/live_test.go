package infini_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 真实端点验证（XM-CARD0 契约「真实实例验证清单」）。
//
// **默认跳过**，只有显式设置 XM_INFINI_LIVE_CHECK=1 才跑。这是一次真实的
// 上游调用，所以不能挂在常规门禁上——门禁必须能在没有凭据、没有网络的
// 机器上跑绿。
//
// 本测试**只调只读接口**（GET /v2/cards/list），零花费。开卡那类要花钱的
// 验证不在这里，必须由人在明确知情的情况下单独做。
//
// 跑法：
//
//	XM_INFINI_LIVE_CHECK=1 \
//	XM_SECRET_ROOT=/run/xm/secrets \
//	XM_CARDS_BASE_URL=https://openapi.infini.money \
//	XM_CARDS_KEY_ID_REF=secret://infini-prod/api-key-id \
//	XM_CARDS_SECRET_REF=secret://infini-prod/api-secret \
//	go test ./connectors/infini/ -run TestLive -v
//
// 注意本机代理会让 Go 的 http.Client 把请求发给代理，跑之前把
// HTTP_PROXY/HTTPS_PROXY/ALL_PROXY/NO_PROXY 一并 unset。
func TestLiveReadOnlyProbe(t *testing.T) {
	if os.Getenv("XM_INFINI_LIVE_CHECK") != "1" {
		t.Skip("未设置 XM_INFINI_LIVE_CHECK=1，跳过真实端点验证（这是一次真实上游调用）")
	}

	baseURL := strings.TrimSpace(os.Getenv("XM_CARDS_BASE_URL"))
	keyIDRaw := strings.TrimSpace(os.Getenv("XM_CARDS_KEY_ID_REF"))
	secretRaw := strings.TrimSpace(os.Getenv("XM_CARDS_SECRET_REF"))
	secretRoot := strings.TrimSpace(os.Getenv("XM_SECRET_ROOT"))
	if baseURL == "" || keyIDRaw == "" || secretRaw == "" || secretRoot == "" {
		t.Fatal("需要 XM_CARDS_BASE_URL / XM_CARDS_KEY_ID_REF / XM_CARDS_SECRET_REF / XM_SECRET_ROOT")
	}

	keyIDRef, err := secrets.ParseCredentialRef(keyIDRaw)
	if err != nil {
		t.Fatalf("XM_CARDS_KEY_ID_REF 格式错: %v", err)
	}
	secretRef, err := secrets.ParseCredentialRef(secretRaw)
	if err != nil {
		t.Fatalf("XM_CARDS_SECRET_REF 格式错: %v", err)
	}

	// 走与生产同一条凭据链：文件 SecretProvider + 审计。
	// 明文只在客户端内部使用，本测试不打印它。
	provider := secrets.NewFileProvider(secretRoot)

	host := hostOf(t, baseURL)
	client := infini.NewClient(baseURL, provider, keyIDRef, secretRef, []string{host})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	page, err := client.ListCards(ctx, infini.ListCardsQuery{Page: 1, PageSize: 20})
	if err != nil {
		switch connector.KindOf(err) {
		case connector.KindAuth:
			t.Fatalf(`认证被拒（401/403）。按这个顺序查，不要先怀疑网络：
  1. 待签名串是否以换行结尾（{keyId}\n{METHOD} {path}\ndate: {GMT}\n）
  2. Authorization 的 headers 值是否是 "@request-target date"
  3. Date 是否 RFC1123 GMT
  4. 本机时钟与服务端偏差是否在 ±300 秒内
  5. 出口 IP 是否真的在白名单里（且出口是否固定）
原始错误: %v`, err)
		case connector.KindForbiddenTarget:
			t.Fatalf("目标被写通道拒绝：base URL 的主机名与 allowlist 不一致，或发生了重定向。%v", err)
		default:
			// 把整条 Unwrap 链打出来：对外错误文本按 ADR-004 只有分类与操作名，
			// 状态码与上游响应体开头都在链里。没有它，一个 bad_response
			// 完全无从下手。
			t.Fatalf(`调用失败（分类 %s）。完整错误链：
  %s

bad_response 的常见成因，按可能性排：
  1. 路径不对——base URL 是否该带前缀（如 /api），或端点其实不是 /v2/cards/list
  2. 上游返回了非 JSON（网关错误页、验证码页、WAF 拦截页）
  3. data 的字段形状与契约不符（契约漂移）
链里的 http 状态码与响应体开头会直接指出是哪一种。`,
				connector.KindOf(err), errorChain(err))
		}
	}

	// —— 到这里签名、Date 格式、IP 白名单三项已经验证通过 ——
	t.Logf("✓ 验证项 1（签名口径 / Date 格式 / IP 白名单）：通过，返回 %d 张卡（共 %d）",
		len(page.Cards), page.Total)

	if len(page.Cards) == 0 {
		t.Log("账号下暂无卡片，验证项 3/4/5 无法从列表核对——开出第一张卡后重跑本测试")
		return
	}

	c := page.Cards[0]

	// 验证项 3：金额单位。打印出来由人对着上游后台核一眼。
	t.Logf("验证项 3（金额单位）：卡 %s 余额 = %d（最小单位）币种 = %q。"+
		"请对照上游后台确认这张卡的真实余额——若后台显示 12.34 而这里是 1234，则口径正确；"+
		"若后台显示 1234，说明上游给的已经是最小单位，card.go 的换算要去掉",
		c.ID, c.BalanceMinor, c.Currency)

	// 验证项 4：时间格式。能解析出来就说明 timestampLayouts 覆盖到了。
	if c.CreatedAt.IsZero() {
		t.Error("验证项 4（时间格式）：created_at 解析成了零值——不该发生，解析失败会报错")
	} else {
		t.Logf("✓ 验证项 4（时间格式）：created_at 解析为 %s", c.CreatedAt.Format(time.RFC3339))
	}

	// 验证项 2 的一半：alias 字段是否被上游回显。
	// 另一半（开卡时能否**设置** alias）要等第一次真实开卡。
	withAlias := 0
	for _, card := range page.Cards {
		if card.Alias != "" {
			withAlias++
		}
	}
	t.Logf("验证项 2（card_alias 回显）：%d/%d 张卡带 alias。"+
		"若平台开出的卡在这里能看到 xm- 开头的 alias，幂等方案成立；"+
		"若开出来的卡 alias 为空，说明上游忽略了申请时的该字段，"+
		"幂等要退回「持卡人 + 时间窗 + 金额」的模糊匹配",
		withAlias, len(page.Cards))

	// 验证项 5：卡状态取值。冻结后的取值文档没写，这里把实际见到的列出来。
	seen := map[string]int{}
	for _, card := range page.Cards {
		seen[card.Status]++
	}
	t.Logf("验证项 5（卡状态取值）：本次见到 %v。契约里记的是 "+
		"init/pending/active/pending_delete/deleted，冻结后的取值待补", seen)

	// 掩码卡号必须是掩码。这一条是安全底线：如果上游在列表里直接给了完整
	// 卡号，那投影表的设计前提就不成立，必须立刻停下重新设计。
	if len(c.Mask) >= 16 && !strings.Contains(c.Mask, "*") {
		t.Errorf("上游在列表接口返回的 mask 看起来不是掩码（长度 %d 且不含 *）——"+
			"投影表存的就是这个字段，若它是完整卡号，落库即违反宪法条款 7，"+
			"必须立刻停止上线并重新设计", len(c.Mask))
	} else {
		t.Logf("✓ mask 字段确认为掩码形态")
	}
}

// errorChain 把整条 Unwrap 链拼出来，每层一行。
func errorChain(err error) string {
	var lines []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		lines = append(lines, e.Error())
	}
	return strings.Join(lines, " -> ")
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if i := strings.IndexAny(trimmed, "/:"); i >= 0 {
		trimmed = trimmed[:i]
	}
	if trimmed == "" {
		t.Fatalf("无法从 %q 解析主机名", rawURL)
	}
	return trimmed
}
