// Package money 是平台的整数定点金额与倍率算术（宪法 13 条：金额禁止 Float，
// 货币金额使用整数最小单位，比例使用 Decimal）。
//
// 为什么单独成包而不是像 connectors/sub2api/amount.go 那样每处抄一份：
// 契约结构体之间不共享是有道理的（上游形状各自演进，耦合应为零，见
// connectors/newapi/contract.go 里 Snapshot 的注释），但**算术不是契约**。
// XM-0037 的成本折算要在四个地方给出逐位相同的结果——取数连接器折算、
// 登记簿存倍率、利润台账入账（037b）、订阅摊销（037c）——只要有两处各自
// 实现一遍「半进」，影子对比（§9 要求分粒度 0 差异）就会在某一天差一分钱，
// 而那种差异既不报错也查不出来。
//
// 本包只做整数与 math/big 运算，**任何路径上都不出现 float32/float64**。
// math/big.Int 是精确整数，不是浮点：用它只是为了让中间乘积不溢出，
// 不引入任何舍入。
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MicroScale 是平台成本核算的存储标度（微单位，即 10^-6）。
//
// 取 6 位而不是 2 位（分）或 8 位（对齐 sub2api 用户余额 decimal(20,8)），
// 依据 XM-0037 设计稿 §2.4 与 §12 拍板：
//
//   - NewAPI 的 `quota / quota_per_unit`（默认 500000）在 scale-6 下**精确无舍入**：
//     micro = quota × 10^6 / 500000 = quota × 2。scale-2 做不到，会在每一条
//     令牌成本上留一个舍入误差，逐日累加后就再也对不回去了。
//   - 比展示所需的「分」低 4 位，分级展示零舍入。
//   - int64 @ scale-6 的上限是 ±9.2×10^12 单位货币，成本台账不可能触顶。
const MicroScale = 6

// maxScale 是本包接受的最大标度。
//
// 18 是 int64 十进制位数的上限量级，再高的标度在 int64 上没有意义；
// 更重要的是它给 10^n 的幂运算一个静态边界，让 pow10 不必处理溢出。
const maxScale = 18

// maxRatioScale 是倍率允许的小数位数。
//
// 倍率是运营手工登记的充值折扣（1.5、0.85 这一类），9 位小数远超实际需要；
// 上界的作用是让 ParseRatio 的溢出判定有个明确的边界，而不是让一个
// 40 位的输入在 int64 上悄悄回绕。
const maxRatioScale = 9

// maxAmountTextLen 限制金额文本长度：正常金额不会有几十位，
// 超长输入只可能是上游发疯或有人在试探解析器。
const maxAmountTextLen = 40

// maxAmountExponent 限制科学计数法的指数范围。
// 上游若用 Go 的 encoding/json 序列化 float64，1000000 会被写成 1e+06，
// 所以指数必须支持；但 1e300 这种只能是垃圾数据。
const maxAmountExponent = 30

// ErrFormat：金额或倍率的文本形态非法。
var ErrFormat = errors.New("money: invalid format")

// ErrOverflow：换算结果超出 int64。
var ErrOverflow = errors.New("money: int64 overflow")

// ErrUnknownCurrency：币种未登记，最小单位小数位无从判断。
var ErrUnknownCurrency = errors.New("money: unregistered currency")

const maxInt64 = int64(^uint64(0) >> 1)

// CurrencyScale 返回币种的最小单位小数位。
//
// 不认识的币种**报错而不是猜 2 位**：猜错的那 100 倍不会有任何症状，
// 只会让所有金额静静地错着（宪法 13 条）。与
// connectors/sub2api/upstream.go 的 currencyScale 同一条纪律、同一张表。
func CurrencyScale(code string) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "USD", "CNY", "EUR", "GBP", "HKD", "AUD", "CAD", "SGD":
		return 2, nil
	case "JPY", "KRW", "VND":
		return 0, nil
	default:
		return 0, fmt.Errorf("%q: %w", code, ErrUnknownCurrency)
	}
}

// RawAmount 接住上游的数值字段：JSON 数字、字符串、null 都收，
// 但**一律按原始文本保存**。
//
// 关键在于绝不在解码这一步落到 float64：json 解码器只要把 12345.67 塞进
// float64，精度就已经丢了，后面再怎么小心也追不回来。
type RawAmount string

// UnmarshalJSON 保留字面量原文，不经 float64。
func (a *RawAmount) UnmarshalJSON(b []byte) error {
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
		*a = RawAmount(strings.TrimSpace(s))
		return nil
	default:
		// 数字字面量原样留着，不经 float64
		*a = RawAmount(text)
		return nil
	}
}

// Empty 报告字段是否缺失（上游给了 null 或省略）。
//
// 调用方必须用它区分「上游说 0」与「上游什么都没说」：前者是已知 0，
// 后者是未知，设计稿 §5.1 要求两者在台账里落成不同的东西（0 与 NULL）。
func (a RawAmount) Empty() bool { return strings.TrimSpace(string(a)) == "" }

// MinorUnits 把金额文本按 scale 位小数换算成整数最小单位。
//
// 空值报错而不是当 0：本包的调用方是成本核算，「上游没给这个字段」与
// 「上游给了 0」在这里必须由调用方显式决定怎么处理（先 Empty 再解析），
// 不能由解析函数替它选一个（宪法 12 条）。
func (a RawAmount) MinorUnits(scale int) (int64, error) {
	return ParseMinorUnits(string(a), scale)
}

// Int64 把整数字段（quota、条数）换算成 int64，scale=0 即可复用同一条路径。
func (a RawAmount) Int64() (int64, error) { return ParseMinorUnits(string(a), 0) }

// ParseMinorUnits 把十进制金额文本换算成整数最小货币单位。
//
// scale 是目标标度（本包的成本路径恒为 MicroScale）。超出 scale 的多余精度按
// **四舍五入**处理（half-up，away from zero），而不是直接截断：上游若用 float64
// 序列化，0.1+0.2 会写成 0.30000000000000004，截断把它变成 0.30 恰好正确，但
// 12.345 截断成 12.34 会**系统性地**少算——四舍五入的误差不带方向，截断的带。
// 舍入本身也只做整数比较，不经 float。
//
// 算法与 connectors/sub2api/amount.go 的 decimalToMinorUnits 逐字等价
// （设计稿 §2.4 点名复用），差别只有 scale 上界放宽到 maxScale：
// 那边只服务币种最小单位（≤2 位），这里要服务 scale-6 的微单位。
func ParseMinorUnits(raw string, scale int) (int64, error) {
	if scale < 0 || scale > maxScale {
		return 0, fmt.Errorf("scale %d 超出 0~%d: %w", scale, maxScale, ErrFormat)
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("空金额: %w", ErrFormat)
	}
	if len(s) > maxAmountTextLen {
		return 0, fmt.Errorf("金额文本过长(%d): %w", len(s), ErrFormat)
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
			return 0, fmt.Errorf("指数非法: %w", ErrFormat)
		}
		if exp > maxAmountExponent || exp < -maxAmountExponent {
			return 0, fmt.Errorf("指数 %d 超出范围: %w", exp, ErrFormat)
		}
		exponent = exp
		s = s[:i]
	}

	intPart, fracPart, hasPoint := strings.Cut(s, ".")
	if hasPoint && strings.Contains(fracPart, ".") {
		return 0, fmt.Errorf("多个小数点: %w", ErrFormat)
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return 0, fmt.Errorf("含非数字字符: %w", ErrFormat)
	}
	if intPart == "" && fracPart == "" {
		return 0, fmt.Errorf("没有数字: %w", ErrFormat)
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
		v, err := parseDigits(digits[:point])
		if err != nil {
			return 0, err
		}
		if point < len(digits) && digits[point] >= '5' {
			if v == maxInt64 {
				return 0, fmt.Errorf("进位溢出: %w", ErrOverflow)
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

// Rescale 把一个已经是整数的定点数从 from 位小数换算到 to 位小数。
//
// 用途是「先按高精度累加，最后一次性折标度」：几千个数各自先四舍五入到分
// 再相加，误差会累积到几十分钱；按高标度累加、只在最后舍一次，误差最多半个
// 最小单位。降标度用半进（away from zero），与 ParseMinorUnits 同一条舍入规则。
func Rescale(v int64, from, to int) (int64, error) {
	if from < 0 || to < 0 || from > maxScale || to > maxScale {
		return 0, fmt.Errorf("scale 超出范围(from=%d, to=%d): %w", from, to, ErrFormat)
	}
	switch {
	case from == to:
		return v, nil
	case from < to:
		factor := pow10(to - from)
		if v > maxInt64/factor || v < -(maxInt64/factor) {
			return 0, fmt.Errorf("升标度溢出: %w", ErrOverflow)
		}
		return v * factor, nil
	default:
		factor := pow10(from - to)
		negative := v < 0
		magnitude := v
		if negative {
			if v == -maxInt64-1 {
				return 0, fmt.Errorf("取绝对值溢出: %w", ErrOverflow)
			}
			magnitude = -v
		}
		// +factor/2 就是整数域里的半进
		out := (magnitude + factor/2) / factor
		if negative {
			return -out, nil
		}
		return out, nil
	}
}

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
		return 0, fmt.Errorf("超出 int64: %w", ErrOverflow)
	}
	return v, nil
}

func pow10(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}
