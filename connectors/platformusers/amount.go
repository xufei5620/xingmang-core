package platformusers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 金额换算:全程字符串/整数 + 定点运算,换算路径上不出现任何浮点类型
// (宪法 13 条、规格 §5.9)。
//
// **为什么不复用 connectors/sub2api 或 connectors/newapi 的同名文件**:那两份
// 都是包内私有的,而且两个上游的金额形状本就不同——Sub2API 是十进制美元
// 字符串/浮点字面量,NewAPI 是整数 quota(要除 quota_per_unit 才是美元)。
// 本包同时要接两边,与其挑一边复用、另一边另起炉灶,不如照着已验证过的两份
// 实现各抄一遍需要的部分,风格与 connectors/newapi/amount.go 顶部的说明一致。
//
// 两条换算路径:
//   - decimalToMinorUnits:Sub2API 的十进制字符串/浮点字面量 → 分(scale 位小数)。
//   - quotaToMinorUnits:NewAPI 的整数 quota → 分,换算基数是运行期可变的
//     quota_per_unit(见 upstream.go 的 newapiQuotaPerUnit)。

var errAmountFormat = errors.New("amount format")

// maxAmountTextLen 限制金额文本长度:正常金额不会有几十位,
// 超长输入只可能是上游发疯或有人在试探解析器。
const maxAmountTextLen = 40

// maxAmountExponent 限制科学计数法的指数范围。
// 上游若用 Go 的 encoding/json 序列化 float64,1000000 会被写成 1e+06,
// 所以指数必须支持;但 1e300 这种只能是垃圾数据。
const maxAmountExponent = 30

// rawAmount 接住上游的数值字段:JSON 数字、字符串、null 都收,
// 但**一律按原始文本保存**。
//
// 关键在于绝不在解码这一步落到 float64:json 解码器只要把 12345.67 塞进
// float64,精度就已经丢了,后面再怎么小心也追不回来。
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
		// 数字字面量原样留着,不经 float64
		*a = rawAmount(text)
		return nil
	}
}

func (a rawAmount) empty() bool { return strings.TrimSpace(string(a)) == "" }

// minorUnits 把金额文本按 scale 位小数换算成整数最小货币单位。
// 空值按 0 处理:上游省略字段与上游给 0 在这里等价,
// "有没有数据"由 Snapshot/Amount.Known 表达,不靠金额字段兼职。
func (a rawAmount) minorUnits(scale int) (int64, error) {
	if a.empty() {
		return 0, nil
	}
	return decimalToMinorUnits(string(a), scale)
}

// count 把整数字段(用户 id、状态码、时间戳)换算成 int64,scale=0 复用同一条路径。
func (a rawAmount) count() (int64, error) { return a.minorUnits(0) }

// decimalToMinorUnits 把十进制金额文本换算成整数最小货币单位。
//
// scale 是该币种的小数位数。超出 scale 的多余精度按**四舍五入**处理
// (half-up,away from zero),而不是直接截断——见 connectors/sub2api/amount.go
// 同名函数的详细论证,这里的算法逐字照抄那份已核对过的实现。
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

	// 科学计数法:指数只是小数点的位移量,用整数处理即可
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
	// point 是小数点在 digits 中的位置;乘 10^scale 等价于把它右移 scale 位
	point := len(intPart) + exponent + scale

	var value int64
	switch {
	case point <= 0:
		// 整个数值都在最小单位以下,只可能舍入出 0 或 1
		if point == 0 && len(digits) > 0 && digits[0] >= '5' {
			value = 1
		}
	default:
		if point > len(digits) {
			// 右侧补零:位数会不会溢出交给 parseDigits 判断
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

// parseDigits 解析纯数字串;前导零与空串都当 0。
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
// 用途是"先按高精度累加,最后一次性折成分":Sub2API 的用户余额是
// decimal(20,8),几千个人各自先四舍五入到分再相加,误差会累积到几十分钱;
// 按 8 位小数累加、只在最后舍一次,误差最多半分。同样只做整数运算。
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

// quotaToMinorUnits 把 NewAPI 的整数 quota 换算成整数最小货币单位。
//
//	美元 = quota / quota_per_unit          (上游 controller/billing.go 的口径)
//	最小单位 = round(quota * 10^scale / quota_per_unit)
//
// **调用纪律:先把该聚合的 quota 全部 SUM 完,再调一次本函数。**
// 每条明细各自换算再相加,误差会按条数累积——见 connectors/newapi/amount.go
// 同名函数的详细论证,算法逐字照抄那份已核对过的实现。
//
// quotaPerUnit <= 0 时报错而不是当 1 用:上游的 option 写入路径可能把它置成
// 0,拿 0 做除数、或拿 1 顶上,都会产出一个**看起来正常**的错数字。
func quotaToMinorUnits(quota, quotaPerUnit int64, scale int) (int64, error) {
	if quotaPerUnit <= 0 {
		return 0, fmt.Errorf("quota_per_unit=%d 非正,无法换算: %w", quotaPerUnit, errAmountFormat)
	}
	if scale < 0 || scale > 9 {
		return 0, fmt.Errorf("scale %d 超出范围: %w", scale, errAmountFormat)
	}

	negative := quota < 0
	magnitude := quota
	if negative {
		if quota == -maxInt64-1 {
			return 0, fmt.Errorf("quota 溢出 int64: %w", errAmountFormat)
		}
		magnitude = -quota
	}

	factor := pow10(scale)
	if magnitude > maxInt64/factor {
		return 0, fmt.Errorf("quota %d 放大 10^%d 后溢出 int64: %w", quota, scale, errAmountFormat)
	}
	scaled := magnitude * factor

	if scaled > maxInt64-quotaPerUnit/2 {
		return 0, fmt.Errorf("quota %d 换算时溢出 int64: %w", quota, errAmountFormat)
	}
	out := (scaled + quotaPerUnit/2) / quotaPerUnit

	if negative {
		return -out, nil
	}
	return out, nil
}

func pow10(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}
