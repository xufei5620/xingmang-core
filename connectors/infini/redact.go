package infini

// 上游文本的脱敏（XM-CARD-VISIBILITY）。
//
// 为什么需要它：本片把上游的业务码与 message 带进了对外错误文本、作业错误串
// 与结构化日志（此前它们只在 Unwrap 链里，运维看不到，2026-09-08 的告警风暴
// 因此查了三天）。一旦上游原话能进日志，它就是一个**泄漏面**——而且不止是
// 内部日志：这段文本还会进 ops 观测的 value_json、进 alerts 的 Finding.Detail，
// 最后由 notify 投到 Telegram / 企业微信 / 通用 webhook，也就是**发出平台之外**。
//
// 纪律：**所有**上游文本都必须经 safeUpstreamText 才能进对外可见的地方。
// 全仓此前没有任何一处对「上游回来的文本」做脱敏——只有 secrets.SecretValue
// 与 RevealedCard 对**我们自己的类型**打码，管不到这一侧。

import (
	"regexp"
	"strings"
)

// redactedMarker 与 secrets 包的占位符一致，便于全仓一眼认出。
const redactedMarker = "[REDACTED]"

// sensitiveKeyFragments 是「像凭据的字段名」的**小写子串**清单。
//
// 语义是**子串**不是整词，这一点是这份清单的全部要害。第一版用 `\b` 锚的
// 整词匹配，于是凡是带词字符前缀的复合字段名一律漏过：`access_token`、
// `client_secret`、`refresh_token`、`app_secret`、`x_api_key` 全都原样进了
// 对外文本（`\btoken\b` 在 `access_token` 里因为下划线是词字符而根本匹配不到）。
// 荒唐的是 `x-api-key`（连字符）挡得住而 `x_api_key`（下划线）挡不住——
// 防线成不成立取决于上游用了哪个分隔符。
//
// 前 11 项与 internal/platform/audit 的 RedactDefault 逐项对齐（那边的注释
// 早就写明「子串匹配，覆盖 Access_Token 这类变体」，是这个仓库里已经想清楚
// 了的那一份）。对齐关系由 redact_shared_test.go 遍历 audit.SensitiveKeyFragments()
// 钉住：那边加一项而这边漏了，测试当场红。**不要把这份清单改成整词匹配。**
//
// 刻意不收 card_number / pan / 卡号：卡号走 digitRun 打成 `****`+末 4 位，
// 末 4 位是运维认卡的唯一依据。把它并进这份清单会让整个卡号消失——那是
// 「宁可多脱敏」用错了地方，脱掉的是诊断而不是风险。
var sensitiveKeyFragments = []string{
	// 与 audit.SensitiveKeyFragments() 同源的 11 项
	"password", "secret", "token", "credential", "api_key", "apikey",
	"authorization", "cookie", "dsn", "private_key", "passphrase",
	// 本连接器特有：签名通道与卡面安全码
	"key", "auth", "signature", "digest", "cvv", "cvc",
	"passwd", "pwd", "client_id",
	// 上游文案与本仓库注释按约定用全角中文
	"密钥", "密码", "令牌", "凭据", "签名",
}

// sensitiveKeyExact 是只能**整键**匹配的字段名。
//
// 单列一份的理由：`pan` 当子串会命中 company / expand / panel，
// `kid` 会命中 kidding；按子串收进去只会把诊断吃掉。
var sensitiveKeyExact = []string{"pan", "kid", "otp", "mac"}

// authSchemes 是「后面紧跟着凭据」的认证 scheme。
//
// 只收这三个：Bearer / Basic / Negotiate 的下一个 token 按定义就是凭据本身。
// **不收 Signature 与 Digest**——HTTP Signature 的 scheme 后面跟的是
// `keyId=...,algorithm=...,signature=...` 这样的参数表，把它们一并吃掉会
// 连着盖住 keyId 那个键值对，反而让 keyId 逃出后面那条键值规则。
const authSchemes = `[Bb]earer|BEARER|[Bb]asic|BASIC|[Nn]egotiate|NEGOTIATE`

// keyedKey 匹配「字段名 + 分隔符」；值由 valueSpan 手工向后取。分组 1 = 键。
//
// 与 adjacentKey 同一个理由：正则的匹配是不重叠的。把值一起写进这条正则，
// `upstream said: token = sk-live-x` 会先被 (said, token) 那一对吃掉，
// 真正要判的 (token, sk-live-x) 根本轮不到检查——而 `said:` 这种「散文里
// 恰好有个冒号」在错误文案里遍地都是。
//
// 分隔符类里**必须**含全角冒号与全角引号：本仓库的上游文案与注释按约定用
// 全角标点，而只认 ASCII 冒号的掩码在这个仓库里已经漏过一次（见记忆
// 「绝不查看密钥文件」）。键的字符类同样要放行中文，否则 `"密钥"："x"`
// 这种形状根本取不到键名。
var keyedKey = regexp.MustCompile(
	`([^\s“”"'「」:：=＝,，;；、{}\[\]()（）]{1,64})` +
		`[“”"'」]?[ \t\x{3000}]*[:：=＝][ \t\x{3000}]*`)

// valuePrefix 是值前面可以有的一个转义反斜杠与一个引号。
//
// 放行 `\` 是因为日志里的 JSON 常常被二次转义（`\"ak_live_1\"`）：
// 不放行的话取值会停在反斜杠上、只吃掉一个字符，密钥本体原样留下。
var valuePrefix = regexp.MustCompile(`^\\?[“”"'「]?`)

// schemePrefix 是值开头可能有的认证 scheme（连同它后面的空白）。
var schemePrefix = regexp.MustCompile(`^(?:` + authSchemes + `)[ \t\x{3000}]+`)

// schemeCredential 匹配没有前置键名的裸 scheme（`… using Bearer sk-live-x`）。
var schemeCredential = regexp.MustCompile(
	`((?:` + authSchemes + `)[ \t\x{3000}]+)([^\s“”"'「」,，;；、)）\]}]+)`)

// adjacentKey 匹配「一个词 + 它后面的空白」；值由 nextToken 手工向后取。
//
// 上游的 message 常是自然语言（`for api_key sk-live-…`），没有分隔符，
// 只认 `k=v` 会让最常见的那种漏网。
//
// **为什么不把「词 + 空白 + 下一个词」写成一条正则**：正则的匹配是不重叠的，
// `invalid cvv2 123` 会先被 (invalid, cvv2) 这一对吃掉，于是真正要判的
// (cvv2, 123) 那一对根本轮不到检查。只匹配键、值另取，相邻对才不会漏。
//
// 还有一条：**下一个词不能无条件当成凭据吃掉**。第一版就是这么干的，于是
// `signature mismatch` 变成 `signature [REDACTED]`、`token expired` 变成
// `token [REDACTED]`、`authorization denied for account LINFENG` 变成
// `authorization [REDACTED] for account LINFENG`——恰恰是拒绝类 message 里
// 最高频的那几个词，而它们后面那个词正是答案。这一片存在的全部理由就是
// 让上游原话到人眼前，把答案吃掉等于白改。判据交给 looksLikeSecret。
var adjacentKey = regexp.MustCompile(
	`([A-Za-z0-9_.\-]{2,64}|密钥|密码|令牌|凭据|签名)[ \t\x{3000}]+`)

// digitRun 匹配 12~19 位数字（允许组间有一个空格或连字符）。
//
// 12~19 是主流卡号的长度区间。允许分隔符是因为上游的报错里出现过
// `5332 2812 3456 1234` 这种给人看的排版，只认连续数字会整段漏掉。
var digitRun = regexp.MustCompile(`\d(?:[ -]?\d){11,18}`)

// secretPrefix 是常见的密钥前缀，用于补 looksLikeSecret 的长度判据。
var secretPrefix = regexp.MustCompile(`(?i)^(?:sk|ak|pk|rk|tk|xk|api|tok|key|secret)[-_]`)

// isSensitiveKey 判断一个字段名像不像凭据。小写**子串**匹配，见清单注释。
func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.Trim(key, "\"'“”「」 \t\\"))
	for _, f := range sensitiveKeyFragments {
		if strings.Contains(k, f) {
			return true
		}
	}
	for _, e := range sensitiveKeyExact {
		if k == e {
			return true
		}
	}
	return false
}

// looksLikeSecret 判断一个词像不像密钥本体。
//
// 判据刻意保守（宁可漏判一个短密钥，也不要把 mismatch / expired / denied
// 这类答案吃掉）：真正的凭据几乎总是长、且混着数字与字母，或者带着
// sk-/ak_ 这类前缀；而自然语言里的下一个词是纯字母的短单词。
// 带分隔符的形状（`token=hunter2`）由 keyedValue 无条件兜住，不靠这里。
func looksLikeSecret(v string) bool {
	v = strings.Trim(v, "\"'“”「」\\")
	if len(v) < 8 {
		return false
	}
	var digits, upper, lower int
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'A' && r <= 'Z':
			upper++
		case r >= 'a' && r <= 'z':
			lower++
		case strings.ContainsRune("+/=_-.~:", r):
			// base64 / JWT / URL-safe 里合法的填充字符
		default:
			// 含中文或自然语言标点：不是密钥本体。
			return false
		}
	}
	if secretPrefix.MatchString(v) {
		return true
	}
	if len(v) >= 16 && digits > 0 && upper+lower > 0 {
		return true
	}
	// 纯字母的长 base64 串（大小写混排）也算。
	return len(v) >= 20 && upper > 0 && lower > 0
}

// isCVVKey / looksLikeCVV 兜住安全码这一种「短得不像密钥」的凭据。
//
// `cvv 123` 里的 123 长度只有 3，looksLikeSecret 一定判否；而安全码恰恰
// 是三位数。注意用子串判键：`\bcvv\b` 匹配不到 `cvv2`。
func isCVVKey(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "cvv") || strings.Contains(k, "cvc")
}

func looksLikeCVV(v string) bool {
	v = strings.Trim(v, "\"'“”「」\\")
	if len(v) < 3 || len(v) > 4 {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// safeUpstreamText 是上游文本进入对外可见文本的**唯一**入口：脱敏 + 截断。
//
// 单独立这个函数（而不是让每个调用点自己拼 truncateFlat(redact…)）是因为
// 长度上限此前只挂在响应体那条路上，message 那条路一个字都不截：上游回一个
// 20 万字节的 message，对外错误串就是 20 万字节，而它会每 5 分钟落一次
// ops 观测的 value_json、进 alerts.detail（无上限的 text 列），最后进
// Telegram sendMessage 的 body——而那边单条上限 4096 字符。一个话痨或被打崩的
// 上游既能把库撑大，又能让**这一片新加的那条告警**投不出去，而那条告警正是
// 本片用来替换「作业变红」的东西。
//
// 由 TestOnlySafeUpstreamTextReachesOutwardText 钉住：非测试文件里除了本函数
// 之外不许再出现 redactUpstreamText 的调用点。
func safeUpstreamText(text string) string {
	return truncateFlat(redactUpstreamText(text))
}

// redactUpstreamText 把一段上游原文脱敏（不截断）。
//
// 顺序是有意的，每一步都被一条用例钉着：
//
//  1. **先压平换行**。两条分隔符字符类都不含 `\n`，于是键与值跨行的键值对
//     整个逃过脱敏，随后压平又把换行抹掉，把漏出来的密钥排成一行漂亮的
//     可读文本。美化过的 JSON 与网关错误页正是响应体最常见的形状。
//     压平必须放在**这里**而不是 bodyPrefix 里：三个 message 入口根本
//     不经过 bodyPrefix。
//  2. 键值对 → 裸 scheme → 相邻词，三条规则依次跑。keyedValue 在前，
//     `Authorization: Bearer x` 一步吃干净，schemeCredential 就不会在
//     已经被替换掉的文本上再命中一次。
//  3. **最后**才打码卡号，也最后才截断（截断在 safeUpstreamText 里）。
//     反过来先截断的话，一个正好跨在 300 字节边界上的卡号会被切成两截，
//     两截都短于 12 位于是都逃过 digitRun——那是一条只在长响应体上才发作
//     的泄漏。RE2 是线性时间，对 4MB 上限内的体做全量替换不是负担。
func redactUpstreamText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = replaceKeyedValues(text)
	text = replaceSchemeCredentials(text)
	text = replaceAdjacentSecrets(text)
	return maskDigitRuns(text)
}

// replaceKeyedValues 按键名判定，命中就把值**整个去掉**。
//
// 凭据没有「留末 4 位还能用」这回事，而留一截反而给撞库省了工。
func replaceKeyedValues(text string) string {
	return rewriteMatches(text, keyedKey.FindAllStringSubmatchIndex(text, -1),
		func(m []int) (int, int, bool) {
			if !isSensitiveKey(text[m[2]:m[3]]) {
				return 0, 0, false
			}
			off := m[1]
			start, end := valueSpan(text[off:])
			if end <= start {
				return 0, 0, false
			}
			return off + start, off + end, true
		})
}

// valueSpan 从一个分隔符之后取「值」的区间（相对于 rest 的字节下标）。
//
// 引号前缀不计进被替换的区间：留着它，`"api_key":"[REDACTED]"` 仍是一段
// 合法可读的 JSON 形状，运维一眼看得出这里原本有个值。
//
// scheme 计进区间：`Authorization: Bearer sk-live-…` 只盖住 Bearer 会留下
// 一个把凭据摆在掩码标记旁边的假安全形状，读日志的人会以为处理过了。
func valueSpan(rest string) (int, int) {
	start := len(valuePrefix.FindString(rest))
	end := start + len(schemePrefix.FindString(rest[start:]))
	end += nextTokenLen(rest[end:])
	return start, end
}

func replaceSchemeCredentials(text string) string {
	return rewriteMatches(text, schemeCredential.FindAllStringSubmatchIndex(text, -1),
		func(m []int) (int, int, bool) { return m[4], m[5], true })
}

func replaceAdjacentSecrets(text string) string {
	return rewriteMatches(text, adjacentKey.FindAllStringSubmatchIndex(text, -1),
		func(m []int) (int, int, bool) {
			key := text[m[2]:m[3]]
			valStart := m[1]
			valEnd := valStart + nextTokenLen(text[valStart:])
			if valEnd <= valStart {
				return 0, 0, false
			}
			val := text[valStart:valEnd]
			if isCVVKey(key) && looksLikeCVV(val) {
				return valStart, valEnd, true
			}
			if !isSensitiveKey(key) || !looksLikeSecret(val) {
				return 0, 0, false
			}
			return valStart, valEnd, true
		})
}

// nextTokenLen 返回紧跟其后的那个词的字节长度（遇到空白或标点即止）。
var tokenStart = regexp.MustCompile(`^[^\s“”"'「」,，;；、)）\]}]+`)

func nextTokenLen(rest string) int {
	return len(tokenStart.FindString(rest))
}

// rewriteMatches 把 pick 选中的区间换成占位符。
//
// 走 FindAllStringSubmatchIndex + 手工拼串，而不是 ReplaceAllStringFunc 里
// 再匹配一次：那种写法拿不到「这一条要不要换」的判断结果，而本片三条规则
// 全都是**有条件**替换（键名不敏感、值不像密钥时必须原样留下）。
func rewriteMatches(text string, matches [][]int, pick func([]int) (int, int, bool)) string {
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end, ok := pick(m)
		if !ok || start < last {
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString(redactedMarker)
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

// maskDigitRuns 把卡号打成 `****` + 末 4 位。
//
// 与凭据不同，卡号**留末 4 位**：运维靠它对上是哪一张卡，而末 4 位不足以
// 复用（宪法条款 7 的既有口径，同 cards 领域层的掩码）。
func maskDigitRuns(text string) string {
	return digitRun.ReplaceAllStringFunc(text, func(m string) string {
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
}
