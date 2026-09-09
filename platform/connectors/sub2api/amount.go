package sub2api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 金额换算：全程字符串 + 整数，换算路径上不出现任何浮点类型（宪法 13 条、规格 §5.9）。
//
// 为什么不能用 strconv.ParseFloat 之后乘 100：float64 只有 53 位尾数，
// 12345.67 在二进制里根本没有精确表示，乘 100 之后可能落到
// 1234566.9999999999，截断就少一分钱。对账场景里一分钱的系统性偏差
// 比"少一个字段"难查得多——它不会报错，只会让两边永远对不上。
//
// 所以上游给的金额从 JSON 字面量开始就以**文本**形态传递（见 rawAmount），
// 直到这里被解析成整数最小货币单位为止，中间没有任何一步经过 float。

var errAmountFormat = errors.New("amount format")

// maxAmountTextLen 限制金额文本长度：正常金额不会有几十位，
// 超长输入只可能是上游发疯或有人在试探解析器。
const maxAmountTextLen = 40

// maxAmountExponent 限制科学计数法的指数范围。
// 上游若用 Go 的 encoding/json 序列化 float64，1000000 会被写成 1e+06，
// 所以指数必须支持；但 1e300 这种只能是垃圾数据。
const maxAmountExponent = 30

// rawAmount 接住上游的数值字段：JSON 数字、字符串、null 都收，
// 但**一律按原始文本保存**。
//
// 关键在于绝不在解码这一步落到 float64：json 解码器只要把 12345.67 塞进
// float64，精度就已经丢了，后面再怎么小心也追不回来。
type rawAmount string

func (a *rawAmount) UnmarshalJSON(b []byte) error {
	text := strings.TrimSpace(string(b))
	switch {
	case text == "" || text == "null":
		*a = ""
		return nil
	case strings.HasPrefix(text, `"`):
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = rawAmount(strings.TrimSpace(s))
		return nil
	default:
		// 数字字面量原样留着，不经 float64
		*a = rawAmount(text)
		return nil
	}
}

func (a rawAmount) empty() bool { return strings.TrimSpace(string(a)) == "" }

// minorUnits 把金额文本按 scale 位小数换算成整数最小货币单位。
// 空值按 0 处理：上游省略字段与上游给 0 在这里等价，
// "有没有数据"由 Snapshot 表达，不靠金额字段兼职。
func (a rawAmount) minorUnits(scale int) (int64, error) {
	if a.empty() {
		return 0, nil
	}
	return decimalToMinorUnits(string(a), scale)
}

// count 把整数字段（用户数、订单数）换算成 int64，scale=0 即可复用同一条路径。
func (a rawAmount) count() (int64, error) { return a.minorUnits(0) }

// decimalToMinorUnits 把十进制金额文本换算成整数最小货币单位。
//
// scale 是该币种的小数位数（CNY/USD 为 2，JPY 为 0）。
// 超出 scale 的多余精度按**四舍五入**处理（half-up，away from zero），
// 而不是直接截断：上游若用 float64 序列化，0.1+0.2 会写成
// 0.30000000000000004，截断把它变成 0.30 恰好正确，但 12.345 截断成
// 12.34 会系统性地少算——四舍五入的误差不带方向，截断的误差带。
// 舍入本身也只做整数比较，不经 float。
func decimalToMinorUnits(raw string, scale int) (int64, error) {
	if scale < 0 || scale > 9 {
		return 0, fmt.Errorf("scale %d 超出范围: %w", scale, errAmountFormat)
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("空金额: %w", errAmountFormat)
	}
	if len(s) > maxAmountTextLen {
		return 0, fmt.Errorf("金额文本过长(%d): %w", len(s), errAmountFormat)
	}

	negative := false
	switch s[0] {
	case '-':
		negative = true
		s = s[1:]
	case '+':
		s = s[1:]
	}

	// 科学计数法：指数只是小数点的位移量，用整数处理即可
	exponent := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return 0, fmt.Errorf("指数非法: %w", errAmountFormat)
		}
		if exp > maxAmountExponent || exp < -maxAmountExponent {
			return 0, fmt.Errorf("指数 %d 超出范围: %w", exp, errAmountFormat)
		}
		exponent = exp
		s = s[:i]
	}

	intPart, fracPart, hasPoint := strings.Cut(s, ".")
	if hasPoint && strings.Contains(fracPart, ".") {
		return 0, fmt.Errorf("多个小数点: %w", errAmountFormat)
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return 0, fmt.Errorf("含非数字字符: %w", errAmountFormat)
	}
	if intPart == "" && fracPart == "" {
		return 0, fmt.Errorf("没有数字: %w", errAmountFormat)
	}

	digits := intPart + fracPart
	// point 是小数点在 digits 中的位置；乘 10^scale 等价于把它右移 scale 位
	point := len(intPart) + exponent + scale

	var value int64
	switch {
	case point <= 0:
		// 整个数值都在最小单位以下，只可能舍入出 0 或 1
		if point == 0 && len(digits) > 0 && digits[0] >= '5' {
			value = 1
		}
	default:
		if point > len(digits) {
			// 右侧补零：位数会不会溢出交给 parseDigits 判断
			digits += strings.Repeat("0", point-len(digits))
		}
		kept := digits[:point]
		v, err := parseDigits(kept)
		if err != nil {
			return 0, err
		}
		if point < len(digits) && digits[point] >= '5' {
			if v == maxInt64 {
				return 0, fmt.Errorf("金额溢出 int64: %w", errAmountFormat)
			}
			v++
		}
		value = v
	}

	if negative {
		return -value, nil
	}
	return value, nil
}

const maxInt64 = int64(^uint64(0) >> 1)

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseDigits 解析纯数字串；前导零与空串都当 0。
func parseDigits(s string) (int64, error) {
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("金额溢出 int64: %w", errAmountFormat)
	}
	return v, nil
}

// rescaleMinorUnits 把一个已经是整数的定点数从 from 位小数换算到 to 位小数。
//
// 用途是「先按高精度累加，最后一次性折成分」：上游的用户余额是
// decimal(20,8)，几千个人各自先四舍五入到分再相加，误差会累积到几十分钱；
// 按 8 位小数累加、只在最后舍一次，误差最多半分。
//
// 同样只做整数运算。
func rescaleMinorUnits(v int64, from, to int) (int64, error) {
	if from < 0 || to < 0 || from > 18 || to > 18 {
		return 0, fmt.Errorf("scale 超出范围(from=%d, to=%d): %w", from, to, errAmountFormat)
	}
	switch {
	case from == to:
		return v, nil
	case from < to:
		factor := pow10(to - from)
		if v > maxInt64/factor || v < -(maxInt64/factor) {
			return 0, fmt.Errorf("换算溢出 int64: %w", errAmountFormat)
		}
		return v * factor, nil
	default:
		factor := pow10(from - to)
		negative := v < 0
		magnitude := v
		if negative {
			if v == -maxInt64-1 {
				return 0, fmt.Errorf("换算溢出 int64: %w", errAmountFormat)
			}
			magnitude = -v
		}
		// +factor/2 就是整数域里的四舍五入
		out := (magnitude + factor/2) / factor
		if negative {
			return -out, nil
		}
		return out, nil
	}
}

func pow10(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}
