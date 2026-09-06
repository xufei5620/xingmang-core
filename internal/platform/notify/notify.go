// Package notify 定义发往企业微信群机器人的**消息信封**（XM-NOTIFY-ENVELOPE，
// 设计稿 docs/superpowers/specs/2026-09-06-xm-notify-envelope-design.md）。
//
// 为什么需要它：平台有三条互相独立的企微通道（告警 / 卡片事件 / 接码验证码），
// 各有各的凭据引用、各写各的格式。运营把同一个 Webhook 地址填进了三个凭据——
// 这是合理用法——于是三种消息落在同一个群里，而它们谁也没说清自己是什么：
// 没有域、没有环境、没有严重度、没有稳定编号、没有"该去哪处理"。
//
// 本包**不合并那三条通道**（它们的领域字段完全不同，合并会让任一方加字段都要
// 改另一方，这条取舍在 cards/notify.go 与 sms/notify.go 的注释里各写过一次），
// 只统一"头部与尾部"：域徽标、严重度、环境、编号、处理入口。正文仍由各域自己写。
package notify

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Domain 是消息所属的业务域。闭集——它决定徽标与编号前缀，是我方文案，
// 因此可以安全地进 markdown 语法（上游文本一律不行，见 RenderWeComMarkdown）。
type Domain string

const (
	DomainAlert   Domain = "alert"
	DomainCard    Domain = "card"
	DomainSMS     Domain = "sms"
	DomainInvoice Domain = "invoice"
)

// Severity 与 alerts.Severity 的三个取值逐字一致。
//
// 刻意**不 import alerts**：本包被 alerts 自己 import，反向依赖会成环；而这三个
// 字符串是规格 §9.3 的稳定取值，不是 alerts 的内部实现细节。两边的一致性由
// alerts 侧的转换点（一次 Severity(string) 转换）与本包的 Validate 共同守住。
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

const (
	// weComMaxContentBytes 是企微 markdown 消息 content 字段的官方上限。
	// 单位是**字节**：UTF-8 下一个汉字占 3 字节，按 rune 数截断仍会越界，
	// 上游会直接拒收整条消息。
	weComMaxContentBytes = 4096
	// truncationMarker 明说被截断了。一条悄悄被截短的消息比一条明说截断的
	// 危险得多——读的人会以为自己看到了全部。
	truncationMarker = "…（正文已截断）"
)

var domainLabels = map[Domain]string{
	DomainAlert:   "告警",
	DomainCard:    "卡片",
	DomainSMS:     "接码",
	DomainInvoice: "开票",
}

var severityLabels = map[Severity]string{
	SeverityInfo:     "通知",
	SeverityWarning:  "警告",
	SeverityCritical: "严重",
}

// severityColors 是企微 markdown 认识的三种颜色。
//
// 企微没有独立的红与橙：critical 与 warning 只能同色。所以**颜色不是唯一的
// 区分手段**——中文词（严重 / 警告 / 通知）才是，配色只是辅助。
var severityColors = map[Severity]string{
	SeverityInfo:     "comment",
	SeverityWarning:  "warning",
	SeverityCritical: "warning",
}

// Line 是正文里的一行"标签：值"。
//
// Value 可能来自上游（告警标题、商户名、指标键、失败原因），因此渲染时一律
// 进引用块且不带任何 markdown 标记。Label 是我方文案。
type Line struct {
	Label string
	Value string
}

// Envelope 是一条待发消息。
type Envelope struct {
	Domain Domain
	// Kind 是域内的稳定事件键，进编号。告警用 rule_key 原样，
	// 卡片用 challenge/transaction/status_change，接码用 code。
	Kind        string
	Severity    Severity
	Environment string
	Title       string
	Lines       []Line
	// Action 是"该去哪处理"，我方闭集文案（例："管理后台 → 告警与故障"）。
	// 空表示这条消息不需要处理（纯周知）。
	Action string
}

// Code 是类型编号：XM-<域大写>-<Kind>。
//
// 它标识**一类**事件而不是一条实例：同类事件共用一个编号，群里搜它能捞出
// 同类的全部历史。实例的唯一标识（告警的 dedup_key、卡片的账号与掩码）
// 已经在正文里，不重复进编号。
func (e Envelope) Code() string {
	kind := strings.TrimSpace(e.Kind)
	if kind == "" {
		kind = "unknown"
	}
	return "XM-" + strings.ToUpper(string(e.Domain)) + "-" + kind
}

// RenderWeComMarkdown 渲染成企微 markdown 消息的 content。
//
// 排版纪律（三条，都不是新发明，是把两处现有实现里已经写死的理由集中到一处）：
//
//  1. 只有闭集用 markdown 语法。域徽标、严重度、环境、编号、处理入口来自封闭
//     枚举与我方文案，给它们上色是安全的；标题与每一行的值由上游决定，可能含
//     `*` `#` 反引号等特殊字符，一律进 `> ` 引用块且不带标记——带标记就要转义，
//     漏转义的后果是排版错乱甚至整条发不出去。`> ` 本身不会被内容提前闭合。
//  2. 按**字节**截断到 4096。截断只发生在正文行：头部与编号/处理两行永远保留，
//     一条被截到看不出是什么的消息等于没发。
//  3. 空值行直接省略，不渲染成"详情："这种半截。
func RenderWeComMarkdown(e Envelope) string {
	head := fmt.Sprintf("【星芒·%s】<font color=\"%s\">%s</font>",
		domainLabel(e.Domain), severityColor(e.Severity), severityLabel(e.Severity))
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

func domainLabel(domain Domain) string {
	if label, ok := domainLabels[domain]; ok {
		return label
	}
	// 未知域仍要发得出去：宁可显示一个陌生的域名，也不要丢一条通知。
	if trimmed := oneLine(string(domain)); trimmed != "" {
		return trimmed
	}
	return "通知"
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

// oneLine 把值压成单行并去掉首尾空白。
//
// 换行会把一行"标签：值"劈成两行，第二行不带 `> ` 前缀就跳出引用块，上游的
// 一段多行错误描述能把整条消息的排版拆散。
func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// truncateBytes 按字节截断，且不切碎一个 UTF-8 字符。
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

// SortedLines 是给调用方的小工具：把一张 map 变成稳定顺序的 Line 列表。
//
// 存在的理由是**确定性**：map 迭代顺序随机，同一条事件两次渲染出的消息不一样，
// 会让"这条我是不是收到过"变得无法判断，也让测试变成碰运气。
func SortedLines(values map[string]string) []Line {
	labels := make([]string, 0, len(values))
	for label := range values {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	lines := make([]Line, 0, len(labels))
	for _, label := range labels {
		lines = append(lines, Line{Label: label, Value: values[label]})
	}
	return lines
}

// --- 模板（XM-NOTIFY-INTEGRATION）-----------------------------------------
//
// 产品负责人 2026-09-06：「规范的基础上还需要对不同需要进行通知的功能制定好
// 通知的模板和格式。这样才能清楚每条通知代表什么。」
//
// **模板写成数据而不是散文**，因为散文会和实现漂开：每种消息该带哪几行，
// 在这里声明一次，由测试对着真实渲染结果核对（notify 的模板测试与各域自己的
// 测试）。漏了一行、改了标签，测试就红——而不是等到运营在群里看见一条缺了
// 关键字段的消息。
//
// 只声明**必须有**的行。可选行（比如告警的「指标」只在有来源指标时才有）不在
// 这里，它们由各域自己判断——把可选行也钉死会逼出一堆空值占位，而空值行本来
// 就该被省略。
var templates = map[string][]string{
	// 告警：读的人要能回答"什么规则、现在什么状态、响了多久"。
	"XM-ALERT": {"规则", "状态", "首次发现", "最近发现"},
	// 卡片交易：商户与金额是这条消息的全部价值；状态决定要不要动手。
	"XM-CARD-transaction": {"商户", "金额", "状态"},
	// 卡片状态变更：只有一件事要说。
	"XM-CARD-status_change": {"当前状态"},
	// 接码：号码定位、码是内容、供应商用来查上游、时间用来判断还有没有效。
	"XM-SMS-code": {"号码", "验证码", "供应商", "时间"},
}

// TemplateFor 返回这条消息**必须**包含的行标签。
//
// 查找按「先精确匹配 XM-域-Kind，再回退到 XM-域」：告警的 Kind 是规则键，
// 每加一条规则都登记一遍既没必要也会漏，所以整个告警域共用一套必备行。
func TemplateFor(e Envelope) []string {
	if labels, ok := templates[e.Code()]; ok {
		return labels
	}
	if labels, ok := templates["XM-"+strings.ToUpper(string(e.Domain))]; ok {
		return labels
	}
	return nil
}

// MissingTemplateLines 返回模板要求、但这条信封没给（或给了空值）的行。
//
// 给测试用，不在渲染路径上：**一条缺字段的消息也要发得出去**——半条消息
// 胜过没有消息，而缺字段该在测试里被拦下，不该在运行时把通知丢掉。
func (e Envelope) MissingTemplateLines() []string {
	present := make(map[string]bool, len(e.Lines))
	for _, line := range e.Lines {
		if oneLine(line.Label) != "" && oneLine(line.Value) != "" {
			present[oneLine(line.Label)] = true
		}
	}
	var missing []string
	for _, label := range TemplateFor(e) {
		if !present[label] {
			missing = append(missing, label)
		}
	}
	return missing
}
