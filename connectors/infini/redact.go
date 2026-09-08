package infini

// 上游文本的脱敏（XM-CARD-VISIBILITY）。
//
// 为什么需要它：本片把上游的业务码与 message 带进了对外错误文本、作业错误串
// 与结构化日志（此前它们只在 Unwrap 链里，运维看不到，2026-09-08 的告警风暴
// 因此查了三天）。一旦上游原话能进日志，它就是一个**泄漏面**——卡端点的
// message 里出现卡号、CVV 或密钥片段完全是可能的，而日志会被归档、导出、搜索
// （宪法条款 7）。
//
// 纪律：**所有**上游文本都必须先过这里，再进任何对外可见的地方。
// 全仓此前没有任何一处对「上游回来的文本」做脱敏——只有 secrets.SecretValue
// 与 RevealedCard 对**我们自己的类型**打码，管不到这一侧。

import "regexp"

// redactedMarker 与 secrets 包的占位符一致，便于全仓一眼认出。
const redactedMarker = "[REDACTED]"

// credentialKeyValue 匹配「像凭据的字段名 + 它的值」。
//
// 三点刻意为之：
//
//  1. 分隔符类里**必须**含全角冒号与全角引号。本仓库的上游文案与注释按约定
//     用全角标点，而只认 ASCII 冒号的掩码在这个仓库里已经漏过一次
//     （见记忆「绝不查看密钥文件」）。
//  2. 分隔符可以是空白：上游的 message 常是自然语言（`for api_key sk-live-…`），
//     不是 JSON。只认 `":"` 会让最常见的那种漏网。
//  3. 长的备选放前面：Go 的 regexp 对 alternation 是 leftmost-first，
//     `api_key` 必须先于裸 `key` 命中，否则 `\bkey\b` 因为下划线是词字符
//     而根本匹配不到。
//
// 命中的值**整个去掉**而不是打码：凭据没有「留末 4 位还能用」这回事，
// 而留一截反而给撞库省了工。卡号是另一条规则（留末 4 位供人对卡）。
var credentialKeyValue = regexp.MustCompile(
	`(?i)([“”"'「」]?\b(?:api[_-]?key|apikey|access[_-]?key|secret[_-]?key|private[_-]?key|` +
		`token|secret|password|passwd|pwd|authorization|credential|signature|digest|cvv|cvc|` +
		`key|auth|sign)\b[“”"'「」]?[\x{3000} \t]*[:：=＝][\x{3000} \t]*|` +
		`\b(?:api[_-]?key|apikey|access[_-]?key|secret[_-]?key|private[_-]?key|` +
		`token|secret|password|passwd|pwd|authorization|credential|signature|digest|cvv|cvc)\b[ \t]+)` +
		`[“”"'「]?[^\s“”"'「」,，;；、)）\]}]+`)

// digitRun 匹配 12~19 位数字（允许组间有一个空格或连字符）。
//
// 12~19 是主流卡号的长度区间。允许分隔符是因为上游的报错里出现过
// `5332 2812 3456 1234` 这种给人看的排版，只认连续数字会整段漏掉。
var digitRun = regexp.MustCompile(`\d(?:[ -]?\d){11,18}`)

// redactUpstreamText 把一段上游原文脱敏。
//
// 顺序是有意的：先去凭据字段，再打码卡号，**最后**才截断。
// 反过来先截断的话，一个正好跨在 300 字节边界上的卡号会被切成两截，
// 两截都短于 12 位，于是两截都逃过 digitRun——那是一条只在长响应体上
// 才发作的泄漏，最难被测试碰到。
func redactUpstreamText(text string) string {
	text = credentialKeyValue.ReplaceAllStringFunc(text, func(m string) string {
		// 只保留「字段名 + 分隔符」那一段，值整个换成占位符。
		idx := credentialKeyValue.FindStringSubmatchIndex(m)
		if len(idx) < 4 || idx[2] < 0 {
			return redactedMarker
		}
		return m[idx[2]:idx[3]] + redactedMarker
	})

	text = digitRun.ReplaceAllStringFunc(text, func(m string) string {
		digits := make([]rune, 0, len(m))
		for _, r := range m {
			if r >= '0' && r <= '9' {
				digits = append(digits, r)
			}
		}
		if len(digits) < 4 {
			return "****"
		}
		return "****" + string(digits[len(digits)-4:])
	})

	return text
}
