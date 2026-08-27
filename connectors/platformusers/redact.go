package platformusers

import "strings"

// MaskedSegment 是被抹掉那一段的占位符。
const MaskedSegment = "***"

// UnparsedEmailPlaceholder 是解析不出邮箱时的返回值。
//
// 为什么不返回空串(那样界面显示「—」,最省事):空串的意思是「上游没记邮箱」,
// 而解析失败的意思是「上游记了,但形状和我们以为的不一样」。后者在本契约还是
// DRAFT 的阶段是一个**要有人看一眼**的信号——真实响应若把联系方式记成手机号、
// 或者记成 `姓名 <a@b.com>` 这种带显示名的形态,整列会齐刷刷变成这个值,
// 一眼就能发现。
//
// 为什么不返回原值:那就等于没有脱敏。解析失败**从不**回退到透传
// (宪法 7 条的同一条思路:拦不住就拒绝,不放行;MaskIP 也是这么做的)。
const UnparsedEmailPlaceholder = "invalid-contact"

// MaskEmail 按契约口径给邮箱打码。
//
// 口径(本函数是唯一实现,契约文档抄的是这里):
//
//	长本地名   保留前 2 个字符      zhangwei@example.com → zh***@example.com
//	短本地名   保留前 1 个字符      abc@example.com      → a***@example.com
//	单字符     一个都不留           a@example.com        → ***@example.com
//	空串       返回空串             ""                   → ""
//	不含 @     返回占位符           13800001111          → invalid-contact
//
// **域名整体保留**,理由与 MaskIP 保留 /24 相同:运营要回答的问题里有
// 「是不是同一家公司在批量注册」,域名刚好够回答它;抹掉之后这个问题就只能
// 靠登上游控制台解决,而那条路绕开了平台的权限与审计——把人赶过去是最坏的结果。
//
// 为什么保留的是**前缀**而不是后缀:人对着工单里的「zhangwei@」能认出是谁,
// 对着「***wei@」认不出。打码的目的是让**不该看到全量通讯录的人**看不到,
// 不是让处理工单的人也认不出自己刚聊过的那个客户。
//
// 为什么不做成可配置:一个「脱敏强度」开关的默认值迟早会被人为了排查方便调到
// 最松,然后留在那里。口径要变就改这里、改契约文档、过一次评审。
//
// 为什么打码发生在**连接器**而不是 HTTP 层:那样明文邮箱会先进平台进程的内存、
// 进日志缓冲、进错误包装,再在最后一步被抹掉。在这里抹,明文的生命周期就只有
// 解析上游响应的那几行——平台的其余部分从来没有机会持有它。
func MaskEmail(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	at := strings.LastIndex(s, "@")
	// 没有 @、@ 在开头、@ 在结尾,三种都不是我们认得的邮箱
	if at <= 0 || at == len(s)-1 {
		return UnparsedEmailPlaceholder
	}
	local := s[:at]
	domain := s[at+1:]
	// **本地名也要校验**，不只是域名。少了这一步，`姓名 <a@b.com>` 这种带显示名的
	// 形态会被当成本地名叫「姓名 <a」的邮箱，打码成 `姓名***@b.com>`——
	// 真实姓名原封不动地留在了输出里。redact_test.go 里有这一条。
	if !plausibleEmailPart(local) || !plausibleEmailPart(domain) {
		return UnparsedEmailPlaceholder
	}
	// 域名至少要有一个点。没有点的「域名」多半是我们切错了位置
	if !strings.Contains(domain, ".") {
		return UnparsedEmailPlaceholder
	}

	keep := 0
	switch {
	case len([]rune(local)) >= 4:
		keep = 2
	case len([]rune(local)) >= 2:
		keep = 1
	}
	runes := []rune(local)
	return string(runes[:keep]) + MaskedSegment + "@" + domain
}

// emailForbidden 是本地名与域名里都不该出现的字符。
//
// 覆盖 RFC 5322 的 specials 与空白：带显示名的 `姓名 <a@b.com>`、逗号分隔的
// 多地址、引号包起来的本地名，都会命中这里。这些形态里有些本身是合法的邮箱
// 写法，但**本契约不接受它们**——一个我们没见过的形状，宁可整条判为无法解析
// （于是显示成占位符、被人发现），也不要打一半码就放出去。
const emailForbidden = " \t\r\n<>,;:\"()[]\\"

func plausibleEmailPart(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, emailForbidden)
}

// MaskTokenPrefix 收紧令牌前缀的长度。
//
// 前缀存在的意义是「把一条请求对上一个用户」,8 个字符足够;再长就开始接近
// 一个可用的密钥片段了。上游给多长我们都只取这么多——不信任上游自己有分寸,
// 是这条链路上唯一站得住的假设。
const maxTokenPrefix = 8

// MaskTokenPrefix 截断令牌前缀。空串原样返回。
func MaskTokenPrefix(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxTokenPrefix {
		return s
	}
	return string(runes[:maxTokenPrefix])
}
