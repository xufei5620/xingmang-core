// Package notify 渲染发往企业微信群机器人的消息（XM-INV-SUBMIT-NOTICE）。
//
// **它是平台线 internal/platform/notify 的同构实现，刻意重复。** 两个系统隔着
// ADR-018 的通道隔离边界：开票系统不 import 平台代码，平台也不读开票的库。
// 为了共用这 80 行而在两者之间拉一条依赖，代价远大于重复本身——那会让开票
// 系统的发布跟着平台的版本走。共用的是**格式约定**（见平台侧
// docs/modules/notify/README.md），由两边各自的测试各自钉住。
//
// 格式约定：每条消息自带域徽标、严重度、环境、类型编号与处理入口，因为运营
// 把同一个 Webhook 地址填给了告警、卡片、接码、开票四个来源，它们落在同一个
// 群里，谁也不说清自己是什么。
package notify

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Severity 按"要不要立刻动手"分，不按"这件事重不重要"。
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

const (
	// weComMaxContentBytes 是企微 markdown 消息 content 的官方上限。单位是
	// **字节**：UTF-8 下一个汉字 3 字节，按 rune 截断仍会越界，上游直接拒收
	// 整条消息。
	weComMaxContentBytes = 4096
	truncationMarker     = "…（正文已截断）"
	// domainLabel 是本系统在群里的徽标。开票系统只发这一个域。
	domainLabel = "开票"
	// domainCode 进类型编号，与平台侧 notify.DomainInvoice 逐字一致。
	domainCode = "INVOICE"
)

var severityLabels = map[Severity]string{
	SeverityInfo:     "通知",
	SeverityWarning:  "警告",
	SeverityCritical: "严重",
}

// severityColors：企微只认三种颜色，没有独立的红与橙，critical 与 warning
// 同色。**颜色不是唯一的区分手段**，中文词才是。
var severityColors = map[Severity]string{
	SeverityInfo:     "comment",
	SeverityWarning:  "warning",
	SeverityCritical: "warning",
}

// Line 是正文里的一行"标签：值"。Value 可能含上游或用户决定的文本，渲染时
// 一律进引用块且不带任何 markdown 标记。
type Line struct {
	Label string
	Value string
}

// Envelope 是一条待发消息。
type Envelope struct {
	// Kind 是稳定事件键，进编号，只增不改。
	Kind        string
	Severity    Severity
	Environment string
	Title       string
	Lines       []Line
	// Action 是"该去哪处理"，我方闭集文案。
	Action string
}

// Code 是类型编号 XM-INVOICE-<kind>。它标识**一类**事件而不是一条实例：
// 同类事件共用一个编号，群里搜它能捞出同类的全部历史；实例的唯一标识
// （申请单号）在正文里。
func (e Envelope) Code() string {
	kind := strings.TrimSpace(e.Kind)
	if kind == "" {
		kind = "unknown"
	}
	return "XM-" + domainCode + "-" + kind
}

// RenderWeComMarkdown 渲染成企微 markdown 消息的 content。
//
// 三条排版纪律：
//
//  1. 只有闭集用 markdown 语法。徽标、严重度、环境、编号、处理入口是我方
//     封闭文案，上色安全；标题与每一行的值可能含用户填写的内容（申请单号
//     由系统生成，但将来加字段时不一定），一律进 `> ` 引用块且不带标记——
//     带标记就得转义，漏转义的后果是排版错乱甚至整条发不出去。
//  2. 按**字节**截断。截断只发生在正文：头部与编号/处理两行永远保留，一条
//     被截到看不出是什么的消息等于没发。
//  3. 空值行整行省略，不渲染成"金额："这种半截。
func RenderWeComMarkdown(e Envelope) string {
	head := fmt.Sprintf("【星芒·%s】<font color=\"%s\">%s</font>",
		domainLabel, severityColor(e.Severity), severityLabel(e.Severity))
	if environment := strings.TrimSpace(e.Environment); environment != "" {
		head += " · " + environment
	}

	tail := "> 编号：" + e.Code()
	if action := strings.TrimSpace(e.Action); action != "" {
		tail += "\n> 处理：" + action
	}

	var body strings.Builder
	if title := oneLine(e.Title); title != "" {
		fmt.Fprintf(&body, "> 标题：%s\n", title)
	}
	for _, line := range e.Lines {
		label, value := oneLine(line.Label), oneLine(line.Value)
		if label == "" || value == "" {
			continue
		}
		fmt.Fprintf(&body, "> %s：%s\n", label, value)
	}

	budget := weComMaxContentBytes - len(head) - len("\n") - len(tail) - len("\n")
	return head + "\n" + truncateBytes(body.String(), budget) + tail
}

func severityLabel(severity Severity) string {
	if label, ok := severityLabels[severity]; ok {
		return label
	}
	return severityLabels[SeverityInfo]
}

func severityColor(severity Severity) string {
	if color, ok := severityColors[severity]; ok {
		return color
	}
	return severityColors[SeverityInfo]
}

// oneLine 把值压成单行。换行会把一行"标签：值"劈成两行，第二行不带 `> `
// 前缀就跳出了引用块。
func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateBytes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	budget := max - len(truncationMarker) - 1
	if budget < 0 {
		return ""
	}
	cut := value[:budget]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + truncationMarker + "\n"
}
