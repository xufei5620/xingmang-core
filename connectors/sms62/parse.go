package sms62

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// codeCandidate 是验证码的形状：4–8 位数字。
var codeCandidate = regexp.MustCompile(`^[0-9]{4,8}$`)

// standaloneDigits 找正文里独立成词的 4–8 位数字。
//
// 要求前后是非数字边界：`\d{4,8}` 会从一串 11 位号码里切出 8 位来当验证码。
var standaloneDigits = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4,8})(?:[^0-9]|$)`)

func isCodeCandidate(s string) bool {
	return codeCandidate.MatchString(strings.TrimSpace(s))
}

// ExtractCode 从一条短信里取验证码。
//
// 两条规则，都是刻意的：
//
//  1. **上游给的结构化字段优先**——它比我们从正文里认要可靠；
//  2. 正文里**只认唯一候选**：出现两个及以上 4–8 位数字时返回空，不猜。
//
// 第二条是这段代码最要紧的部分。一条「您的验证码是 123456，订单号 998877」
// 有两个候选；猜错一个填进付款页，代价是一次失败的注册加一个被烧掉的号。
// 宁可让人自己看正文——而正文不落库，所以这个函数是唯一的出口。
func ExtractCode(m Message) string {
	if c := strings.TrimSpace(m.StructuredCode); isCodeCandidate(c) {
		return c
	}
	matches := standaloneDigits.FindAllStringSubmatch(m.Text, -1)
	seen := map[string]struct{}{}
	var only string
	for _, match := range matches {
		candidate := match[1]
		if _, dup := seen[candidate]; dup {
			// 同一个数字出现两次仍是同一个候选（「验证码 1234，请输入 1234」）。
			continue
		}
		seen[candidate] = struct{}{}
		if only != "" {
			return ""
		}
		only = candidate
	}
	return only
}

// PickLatest 从多条短信里挑最新的一条**且能取出验证码的**。
//
// 按 ReceivedAt 取最大而不是取第一条：上游返回顺序没有文档保证，而拿到一条
// 五分钟前的旧码去填当前这次验证，会失败得毫无头绪。
func PickLatest(messages []Message) (Message, string, bool) {
	var best Message
	var bestCode string
	found := false
	for _, m := range messages {
		code := ExtractCode(m)
		if code == "" {
			continue
		}
		if !found || m.ReceivedAt > best.ReceivedAt {
			best, bestCode, found = m, code, true
		}
	}
	return best, bestCode, found
}

// collectionObjects 从 data 里取出对象数组。
//
// 允许有限的几个容器名（goods/items/list/data）是因为这家上游在不同端点上
// 用的名字不一样。**未知结构判协议失败，不返回空集合**——空集合会让
// 「上游改了形状」看起来像「今天没有数据」。
func collectionObjects(raw json.RawMessage, keys ...string) ([]map[string]any, error) {
	objects, _, err := collectionObjectsWithRoot(raw, keys...)
	return objects, err
}

func collectionObjectsWithRoot(raw json.RawMessage, keys ...string) ([]map[string]any, map[string]any, error) {
	var value any
	if err := decodeOneJSON(raw, &value); err != nil {
		return nil, nil, &ProtocolError{Kind: "集合数据"}
	}

	root := map[string]any{}
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case map[string]any:
		root = typed
		for _, key := range keys {
			if nested, ok := typed[key].([]any); ok {
				values = nested
				break
			}
		}
		if values == nil {
			return nil, nil, &ProtocolError{Kind: "集合容器名未知"}
		}
	default:
		return nil, nil, &ProtocolError{Kind: "集合数据类型"}
	}

	out := make([]map[string]any, 0, len(values))
	for _, v := range values {
		object, ok := v.(map[string]any)
		if !ok {
			return nil, nil, &ProtocolError{Kind: "集合元素类型"}
		}
		out = append(out, object)
	}
	return out, root, nil
}

// scalarString 按给定的键顺序取第一个能当字符串用的标量。
//
// 数字也接受并转成字符串：这家上游的 ID 时而是数字时而是字符串，
// 而我们只把它当标识符用，不做算术。
func scalarString(o map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := o[key]
		if !ok {
			continue
		}
		switch typed := v.(type) {
		case string:
			return strings.TrimSpace(typed)
		case json.Number:
			return typed.String()
		}
	}
	return ""
}

func scalarInt(o map[string]any, keys ...string) int64 {
	for _, key := range keys {
		v, ok := o[key]
		if !ok {
			continue
		}
		switch typed := v.(type) {
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return n
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// providerIDFromJSON 把上游 ID 转成字符串，数字与字符串都接受。
func providerIDFromJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return sanitizeText(asString, 128)
	}
	var asNumber json.Number
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber.String()
	}
	return ""
}
