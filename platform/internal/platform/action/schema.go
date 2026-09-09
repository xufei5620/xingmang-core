package action

import (
	"errors"
	"fmt"
	"slices"
)

// FieldType 是参数字段类型（Foundation-A 只需这四类）。
type FieldType string

const (
	FieldString      FieldType = "string"
	FieldInt         FieldType = "int"
	FieldBool        FieldType = "bool"
	FieldStringSlice FieldType = "string_slice"
)

var (
	// ErrUnknownField：出现未在 Schema 中声明的字段（白名单语义）。
	ErrUnknownField = errors.New("unknown field")
	// ErrRequiredField：必填字段缺失。
	ErrRequiredField = errors.New("required field missing")
	// ErrFieldType：字段类型不符。
	ErrFieldType = errors.New("field type mismatch")
	// ErrEnumViolation：值不在枚举内。
	ErrEnumViolation = errors.New("value not in enum")
)

// Field 是一个参数字段声明。
type Field struct {
	Name     string
	Type     FieldType
	Required bool
	Enum     []string // 仅对 FieldString 生效；空表示不限
}

// Schema 是 Action 的参数契约。
//
// 采用白名单语义：**未声明的字段一律拒绝**——参数偷渡是权限绕过的常见入口，
// 宁可让调用方显式扩展 Schema，也不放行未知字段。
type Schema struct {
	Fields []Field
}

// Validate 校验参数。
func (s Schema) Validate(params map[string]any) error {
	declared := make(map[string]Field, len(s.Fields))
	for _, f := range s.Fields {
		declared[f.Name] = f
	}
	for name := range params {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("字段 %q 未在 Schema 中声明: %w", name, ErrUnknownField)
		}
	}
	for _, f := range s.Fields {
		v, present := params[f.Name]
		if !present || v == nil {
			if f.Required {
				return fmt.Errorf("字段 %q: %w", f.Name, ErrRequiredField)
			}
			continue
		}
		if err := checkField(f, v); err != nil {
			return err
		}
	}
	return nil
}

func checkField(f Field, v any) error {
	switch f.Type {
	case FieldString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("字段 %q 期望 string: %w", f.Name, ErrFieldType)
		}
		if len(f.Enum) > 0 && !slices.Contains(f.Enum, s) {
			return fmt.Errorf("字段 %q=%q 不在枚举 %v 内: %w", f.Name, s, f.Enum, ErrEnumViolation)
		}
	case FieldInt:
		switch n := v.(type) {
		case int, int64:
		case float64:
			// JSON 解码后整数是 float64；只接受无小数部分的值。
			if n != float64(int64(n)) {
				return fmt.Errorf("字段 %q 期望整数: %w", f.Name, ErrFieldType)
			}
		default:
			return fmt.Errorf("字段 %q 期望 int: %w", f.Name, ErrFieldType)
		}
	case FieldBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("字段 %q 期望 bool: %w", f.Name, ErrFieldType)
		}
	case FieldStringSlice:
		switch xs := v.(type) {
		case []string:
		case []any:
			for i, e := range xs {
				if _, ok := e.(string); !ok {
					return fmt.Errorf("字段 %q[%d] 期望 string: %w", f.Name, i, ErrFieldType)
				}
			}
		default:
			return fmt.Errorf("字段 %q 期望字符串数组: %w", f.Name, ErrFieldType)
		}
	default:
		return fmt.Errorf("字段 %q 声明了未知类型 %q: %w", f.Name, f.Type, ErrFieldType)
	}
	return nil
}

// StringSliceParam 从已校验的参数中取出字符串数组（同时接受 []string 与 []any）。
func StringSliceParam(params map[string]any, name string) []string {
	switch xs := params[name].(type) {
	case []string:
		return xs
	case []any:
		out := make([]string, 0, len(xs))
		for _, e := range xs {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// StringParam 从已校验的参数中取出字符串（缺失返回空串）。
func StringParam(params map[string]any, name string) string {
	s, _ := params[name].(string)
	return s
}

// IntParam 从已校验的参数中取出整数（缺失返回 0）。
//
// 三种来源都收：Go 直接构造的 int / int64，以及 JSON 解码后的 float64。
// 最后一种是 HTTP 路径的常态——encoding/json 把所有数字解成 float64。
// checkField 已经拒绝过带小数部分的值，所以这里的转换不会丢东西。
//
// **不用它取金额**：float64 在 scale-6 下超过 $9,007,199 就开始丢精度，
// 而丢掉的那一位不会报错（宪法 13 条）。金额一律走字符串参数
// （见 finance.requiredMinorParam）。
func IntParam(params map[string]any, name string) int {
	switch n := params[name].(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// BoolParam 从已校验的参数中取出布尔值（缺失返回 false）。
func BoolParam(params map[string]any, name string) bool {
	b, _ := params[name].(bool)
	return b
}
