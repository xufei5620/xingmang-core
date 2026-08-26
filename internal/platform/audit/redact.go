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
		if nested, ok := v.(map[string]any); ok {
			out[k] = redactWith(nested, match)
			continue
		}
		out[k] = v
	}
	return out
}
