package audit

import "strings"

// redactedPlaceholder 是脱敏后的占位值。
const redactedPlaceholder = "[REDACTED]"

// defaultSensitiveKeys 是常见的敏感字段名片段（小写子串匹配）。
// 宁可多脱敏，也不要让凭据进审计（宪法 7 条）。
var defaultSensitiveKeys = []string{
	"password", "secret", "token", "credential", "api_key", "apikey",
	"authorization", "cookie", "dsn", "private_key", "passphrase",
}

// Redact 深拷贝并把命中键名的值替换为占位符；不修改入参。
//
// 责任边界：本函数只按键名匹配，**不判断值本身是否敏感**。调用方仍需
// 自行确认 summary 里没有以其他名字混进来的凭据或用户隐私。
func Redact(m map[string]any, keys ...string) map[string]any {
	if m == nil {
		return nil
	}
	lower := make([]string, len(keys))
	for i, k := range keys {
		lower[i] = strings.ToLower(k)
	}
	return redactWith(m, func(key string) bool {
		lk := strings.ToLower(key)
		for _, k := range lower {
			if lk == k {
				return true
			}
		}
		return false
	})
}

// RedactDefault 用内置敏感键名清单脱敏（子串匹配，覆盖 Access_Token 这类变体）。
func RedactDefault(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return redactWith(m, func(key string) bool {
		lk := strings.ToLower(key)
		for _, s := range defaultSensitiveKeys {
			if strings.Contains(lk, s) {
				return true
			}
		}
		return false
	})
}

func redactWith(m map[string]any, match func(string) bool) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if match(k) {
			out[k] = redactedPlaceholder
			continue
		}
		out[k] = redactValue(v, match)
	}
	return out
}

// redactValue 递归处理一个值：map 逐键判定，切片逐元素下钻，其余原样返回。
//
// 切片必须下钻（XM-0031）：审计摘要里已经有 target_allowlist、
// granted_capabilities 这类数组，而 jsonb 允许对象数组。只递归 map 时，
// `{"connections":[{"token":"..."}]}` 会整个躲过脱敏——一个按键名脱敏的
// 函数漏掉一整种容器，等于给凭据留了条法定通道。
//
// 元素为 map 时递归返回**新的** map，所以切片也要新建而不是复用入参底层
// 数组：redactWith 承诺不修改入参，就地改写会让调用方手里的原始摘要被悄悄
// 改掉（而它可能还要用于别的用途，比如返回给 Action 调用方）。
func redactValue(v any, match func(string) bool) any {
	switch t := v.(type) {
	case map[string]any:
		return redactWith(t, match)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactValue(item, match)
		}
		return out
	default:
		// 具体类型的切片（[]string 等）不含嵌套 map，无需下钻。
		return v
	}
}
