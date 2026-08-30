package sub2api

import "strings"

// maskedSegment 是被抹掉那一段的占位符。
const maskedSegment = "***"

// unparsedEmailPlaceholder 是解析不出邮箱时的返回值——解析失败从不回退到
// 透传（宪法 7 条：拦不住就拒绝，不放行）。
const unparsedEmailPlaceholder = "invalid-contact"

// maskEmail 给邮箱打码，供 payments.go 的 maskUserRef 使用。
//
// 口径与 connectors/platformusers.MaskEmail 完全一致（同一个产品要给用户
// 看到同一种打码格式），但是**独立实现，不 import 那个包**：两个连接器包
// 之间不互相依赖，是 contract.go 顶部关于 Snapshot 不跨包共享的同一条理由——
// 任意一边为兼容对方而改动打码口径，不该让另一边被动跟着变。口径本身如需
// 变更，两处都要改、都要过评审。
//
//	长本地名   保留前 2 个字符      zhangwei@example.com → zh***@example.com
//	短本地名   保留前 1 个字符      abc@example.com      → a***@example.com
//	单字符     一个都不留           a@example.com        → ***@example.com
//	空串       返回空串             ""                   → ""
//	不含 @ 或解析不出               → unparsedEmailPlaceholder
func maskEmail(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return unparsedEmailPlaceholder
	}
	local := s[:at]
	domain := s[at+1:]
	if !plausibleEmailPart(local) || !plausibleEmailPart(domain) {
		return unparsedEmailPlaceholder
	}
	if !strings.Contains(domain, ".") {
		return unparsedEmailPlaceholder
	}

	keep := 0
	switch {
	case len([]rune(local)) >= 4:
		keep = 2
	case len([]rune(local)) >= 2:
		keep = 1
	}
	runes := []rune(local)
	return string(runes[:keep]) + maskedSegment + "@" + domain
}

// emailForbidden 是本地名与域名里都不该出现的字符（RFC 5322 specials + 空白）。
const emailForbidden = " \t\r\n<>,;:\"()[]\\"

func plausibleEmailPart(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, emailForbidden)
}
