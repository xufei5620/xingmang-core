package notify

import (
	"strings"
	"testing"
)

func TestRenderCarriesDomainSeverityEnvironmentCodeAndAction(t *testing.T) {
	got := RenderWeComMarkdown(Envelope{
		Domain: DomainAlert, Kind: "channel.balance.low", Severity: SeverityWarning,
		Environment: "production", Title: "Sub2API 渠道余额不足",
		Lines:  []Line{{Label: "规则", Value: "channel.balance.low"}, {Label: "详情", Value: "余额 312.00 低于阈值 5000.00"}},
		Action: "管理后台 → 告警与故障",
	})
	for _, want := range []string{
		"【星芒·告警】", "<font color=\"warning\">警告</font>", " · production",
		"> 标题：Sub2API 渠道余额不足",
		"> 规则：channel.balance.low",
		"> 详情：余额 312.00 低于阈值 5000.00",
		"> 编号：XM-ALERT-channel.balance.low",
		"> 处理：管理后台 → 告警与故障",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered message is missing %q:\n%s", want, got)
		}
	}
	if lines := strings.Split(got, "\n"); !strings.HasPrefix(lines[0], "【星芒·告警】") {
		t.Fatalf("the badge must be the first thing a reader sees, got %q", lines[0])
	}
}

func TestCodeIsATypeCodeNotAnInstanceCode(t *testing.T) {
	cases := []struct {
		envelope Envelope
		want     string
	}{
		{Envelope{Domain: DomainAlert, Kind: "metric.sync.failed"}, "XM-ALERT-metric.sync.failed"},
		{Envelope{Domain: DomainCard, Kind: "challenge"}, "XM-CARD-challenge"},
		{Envelope{Domain: DomainSMS, Kind: "code"}, "XM-SMS-code"},
		{Envelope{Domain: DomainInvoice, Kind: "request.submitted"}, "XM-INVOICE-request.submitted"},
		{Envelope{Domain: DomainCard}, "XM-CARD-unknown"},
	}
	for _, tc := range cases {
		if got := tc.envelope.Code(); got != tc.want {
			t.Fatalf("Code()=%q want %q", got, tc.want)
		}
	}
}

// 上游文本是本包最主要的风险来源：标题、商户名、失败原因都由上游决定。
// 它们必须原样出现在引用块里，且不得引入任何会被企微解释的标记。
func TestUpstreamTextIsQuotedVerbatimWithoutMarkup(t *testing.T) {
	hostile := "**加粗** `代码` # 标题 <font color=\"warning\">注入</font>"
	got := RenderWeComMarkdown(Envelope{
		Domain: DomainCard, Kind: "transaction", Severity: SeverityInfo,
		Title: hostile, Lines: []Line{{Label: "商户", Value: hostile}},
	})
	if !strings.Contains(got, "> 标题："+hostile) || !strings.Contains(got, "> 商户："+hostile) {
		t.Fatalf("upstream text must appear verbatim inside the quote block:\n%s", got)
	}
	// 我方唯一使用的 font 标签只出现一次（头部的严重度），上游那段里的
	// font 字样是**内容**，不构成第二个我方标签。
	if strings.Count(got, "<font color=\"comment\">") != 1 {
		t.Fatalf("exactly one severity tag is ours:\n%s", got)
	}
}

func TestMultiLineUpstreamValueIsFlattenedIntoOneQuotedLine(t *testing.T) {
	got := RenderWeComMarkdown(Envelope{
		Domain: DomainAlert, Kind: "metric.sync.failed", Severity: SeverityCritical,
		Lines: []Line{{Label: "详情", Value: "第一行\n第二行\r\n  第三行  "}},
	})
	if !strings.Contains(got, "> 详情：第一行 第二行 第三行") {
		t.Fatalf("a multi-line upstream value must not escape the quote block:\n%s", got)
	}
}

func TestEmptyValuesAreOmittedRatherThanRenderedHalfway(t *testing.T) {
	got := RenderWeComMarkdown(Envelope{
		Domain: DomainSMS, Kind: "code", Severity: SeverityWarning,
		Lines: []Line{{Label: "验证码", Value: ""}, {Label: "", Value: "孤值"}, {Label: "号码", Value: "138****0000"}},
	})
	if strings.Contains(got, "验证码") || strings.Contains(got, "孤值") {
		t.Fatalf("empty label/value pairs must be dropped entirely:\n%s", got)
	}
	if !strings.Contains(got, "> 号码：138****0000") {
		t.Fatalf("complete lines must survive:\n%s", got)
	}
}

// 截断按字节，且头部与编号行必须活下来——一条被截到看不出是什么的消息
// 等于没发。
func TestTruncationIsByBytesAndKeepsTheHeaderAndCode(t *testing.T) {
	got := RenderWeComMarkdown(Envelope{
		Domain: DomainAlert, Kind: "metric.data.stale", Severity: SeverityCritical,
		Environment: "production", Title: strings.Repeat("很长的标题", 2000),
		Lines:  []Line{{Label: "详情", Value: strings.Repeat("很长的详情", 2000)}},
		Action: "管理后台 → 告警与故障",
	})
	if len(got) > weComMaxContentBytes {
		t.Fatalf("content is %d bytes, over the %d-byte WeCom limit", len(got), weComMaxContentBytes)
	}
	for _, want := range []string{"【星芒·告警】", "> 编号：XM-ALERT-metric.data.stale", "> 处理：管理后台 → 告警与故障", truncationMarker} {
		if !strings.Contains(got, want) {
			t.Fatalf("truncated message lost %q:\n%s", want, got[:200])
		}
	}
	// 截断点没有切碎汉字。
	if !isValidUTF8(got) {
		t.Fatal("truncation split a multi-byte character")
	}
}

func TestUnknownDomainAndSeverityStillSend(t *testing.T) {
	got := RenderWeComMarkdown(Envelope{Domain: Domain("newthing"), Kind: "k", Severity: Severity("bogus"), Title: "t"})
	if !strings.Contains(got, "【星芒·newthing】") || !strings.Contains(got, "通知</font>") {
		t.Fatalf("an unknown domain/severity must degrade, never drop the message:\n%s", got)
	}
	if !strings.Contains(got, "XM-NEWTHING-k") {
		t.Fatalf("code must still be derivable:\n%s", got)
	}
}

func TestSortedLinesIsDeterministic(t *testing.T) {
	values := map[string]string{"乙": "2", "甲": "1", "丙": "3"}
	first := SortedLines(values)
	for range 20 {
		again := SortedLines(values)
		for i := range first {
			if first[i] != again[i] {
				t.Fatalf("SortedLines is not deterministic: %+v vs %+v", first, again)
			}
		}
	}
}

func isValidUTF8(value string) bool {
	for _, r := range value {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
