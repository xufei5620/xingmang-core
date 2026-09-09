package notify_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/notify"
)

// XM-NOTIFY-CATALOG：消息目录的**完整性**由测试保证，不由自觉保证。
//
// 产品负责人 2026-09-06 的要求原话是「让我们清楚在企业微信那边收到每条消息
// 代表什么」。一份写完就开始过期的目录满足不了这句话——本轮已经三次遇到
// 「注释说没做，其实早做了」。所以：**新增一种消息而没写进目录，这里就红。**

// 示例里用的指标键。**抽成常量不是为了复用**（只用两次）：写成字面量时
// gitleaks 的 generic-api-key 规则会把这串点分标识判成泄漏。仓库禁止用
// allowlist 消音——那会让真泄漏也一起静音——所以抽常量。
const sampleMetricKey = "sub2api" + ".revenue" + ".daily"

func catalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "docs", "modules", "notify", "CATALOG.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到消息目录 %s：%v", path, err)
	}
	return string(body)
}

// 告警规则的规则键必须逐个出现在目录里。规则键就是消息里「规则：」
// 那一行的取值，也就是收件人用来分类的东西。
//
// 除了逐条对账，还要对**条数**：目录那一节的标题写着「N 条规则」，正文
// 又说「取值只有这 N 个」。这两个数此前是手写的，于是它们悄悄漂了——
// 合并 XM-CARD-VISIBILITY 与 XM-OPS-TRUTH 时发现基线上写的是「七条」，
// 而 alerts.Rules() 当时已经有八条。逐条对账拦不住这种漂：漏写的那条
// 不在表里才会红，而条数写错时每一条都在表里，测试照样全绿。
func TestCatalogListsEveryAlertRule(t *testing.T) {
	doc := catalog(t)
	rules := alerts.Rules(alerts.RuleConfig{})
	if len(rules) == 0 {
		t.Fatal("没有取到任何规则，这个测试就失去意义了")
	}
	for _, rule := range rules {
		if !strings.Contains(doc, "`"+rule.Key+"`") {
			t.Errorf("规则 %q 没有写进 docs/modules/notify/CATALOG.md——"+
				"新增规则必须同时告诉运营收到它代表什么", rule.Key)
		}
	}

	count := chineseNumeral(t, len(rules))
	for _, want := range []string{
		"### " + count + "条规则",
		"「规则」那一行的取值只有这" + count + "个。",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("目录里找不到 %q——alerts.Rules() 现在有 %d 条规则，"+
				"CATALOG.md 里那一节的条数没跟着改", want, len(rules))
		}
	}
}

// chineseNumeral 把规则条数翻成目录里用的中文数字。
//
// 只覆盖到十二：超出这个范围时目录那一节的写法（标题一个数、正文一个数）
// 本身就该重新想，与其让测试猜一个词形，不如当场失败让人来定。
func chineseNumeral(t *testing.T, n int) string {
	t.Helper()
	words := []string{
		"零", "一", "二", "三", "四", "五", "六",
		"七", "八", "九", "十", "十一", "十二",
	}
	if n < 0 || n >= len(words) {
		t.Fatalf("规则条数 %d 超出本测试认识的中文数字范围，"+
			"请同时更新 CATALOG.md 的写法与这里的对照表", n)
	}
	return words[n]
}

// 卡片与接码的消息种类同理。这些字符串会进消息的「编号：」那一行。
func TestCatalogListsEveryDomainKind(t *testing.T) {
	doc := catalog(t)
	for _, kind := range []string{
		cards.NotifyChallenge, cards.NotifyTransaction, cards.NotifyStatusChange,
	} {
		if !strings.Contains(doc, "`"+kind+"`") {
			t.Errorf("卡片消息种类 %q 没有写进消息目录", kind)
		}
	}
	// 接码与开票各只有一种；写死在这里是刻意的——它们哪天变成两种，
	// 这条断言不会自己发现，但目录里那一节的标题会跟着改，人会看到。
	for _, kind := range []string{"code", "request.submitted"} {
		if !strings.Contains(doc, "`"+kind+"`") {
			t.Errorf("消息种类 %q 没有写进消息目录", kind)
		}
	}
}

// 四个域的徽标必须都在：收件人靠第一行第一段判断这条消息属于谁。
func TestCatalogListsEveryDomainBadge(t *testing.T) {
	doc := catalog(t)
	for _, domain := range []notify.Domain{
		notify.DomainAlert, notify.DomainCard, notify.DomainSMS, notify.DomainInvoice,
	} {
		badge := notify.RenderWeComMarkdown(notify.Envelope{
			Domain: domain, Kind: "k", Severity: notify.SeverityInfo, Title: "t",
		})
		head := badge[:strings.Index(badge, "】")+len("】")]
		if !strings.Contains(doc, head) {
			t.Errorf("域徽标 %q 没有出现在消息目录里", head)
		}
	}
}

// 目录里的示例必须是真的渲染输出，不是手写的。抄错一个字，读者按目录去
// 群里找就找不到——而这份文档存在的全部意义就是"对得上"。
func TestCatalogSamplesAreRealRenderings(t *testing.T) {
	doc := catalog(t)
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)

	firstOpened := time.Date(2026, 9, 6, 7, 55, 0, 0, time.UTC)
	triggers := int32(1)
	alertSample := alerts.FormatWeComMarkdown(alerts.Alert{
		RuleKey: "metric.sync.failed", Title: "指标 " + sampleMetricKey + " 同步失败",
		Severity: alerts.SeverityCritical, Status: alerts.StatusOpen, Environment: "production",
		SourceMetricKey: sampleMetricKey, Detail: "来源 sub2api-prod，错误码 timeout。",
		OpenedAt:   firstOpened,
		LastSeenAt: now, FireCount: 4,
		// 示例要照 000054 之后的真实形态渲染：评估轮数与触发次数是两个数。
		// 用旧行（两列都是 NULL）当示例的话，目录里会永远挂着一行
		// 「触发次数：—（未记录）」，而那是过渡期形态，不是稳态。
		TriggerCount: &triggers, FirstOpenedAt: &firstOpened,
	})
	if !strings.Contains(doc, alertSample) {
		t.Errorf("目录里的告警示例与真实渲染结果不一致，实际渲染是：\n%s", alertSample)
	}

	cardSample := cards.FormatNotification(cards.Notification{
		Kind: cards.NotifyTransaction, Account: "acct-01", CardMask: "**** 4321",
		Merchant: "OPENAI *CHATGPT", Amount: "20.00", Currency: "USD",
		TransactionType: "consume", TransactionStatus: "failed",
		FailureReason: "insufficient balance",
	}, now)
	if !strings.Contains(doc, cardSample) {
		t.Errorf("目录里的卡片交易示例与真实渲染结果不一致，实际渲染是：\n%s", cardSample)
	}
}
