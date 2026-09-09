package newapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 金额换算：全程字符串 + 整数，换算路径上不出现任何浮点类型（宪法 13 条、规格 §5.9）。
//
// 为什么不用 strconv.ParseFloat 之后乘 100：float64 只有 53 位尾数，
// 12345.67 在二进制里根本没有精确表示，乘 100 之后可能落到
// 1234566.9999999999，截断就少一分钱。对账场景里一分钱的系统性偏差
// 比「少一个字段」难查得多——它不会报错，只会让两边永远对不上。
//
// **为什么不复用 connectors/sub2api 的同名文件**：那份是包内私有的，
// 而且两个上游的金额形状不同——NewAPI 的主口径是整数 quota（要除
// quota_per_unit 才是美元），Sub2API 是十进制美元字符串。共享一份实现会让
// 任意一边为了迁就对方而放宽解析，那正是金额纪律最容易被磨掉的方式。
// 这与 Snapshot 不复用是同一条理由（见 contract.go）。
//
// NewAPI 特有的一条：**quota 是整数**，换算成美元要除 quota_per_unit
// （运行期可变，见 upstream.go 的 fetchQuotaPerUnit）。除法一律
// **先 SUM 再除、只除一次**——每条明细各自除完再相加，误差会按条数累积。

var errAmountFormat = errors.New("amount format")

// maxAmountTextLen 限制金额文本长度：正常金额不会有几十位，
// 超长输入只可能是上游发疯或有人在试探解析器。
const maxAmountTextLen = 40

// maxAmountExponent 限制科学计数法的指数范围。
//
// NewAPI 的 channel.balance 与 quota_per_unit 在上游都是 float64，用 Go 的
// encoding/json 序列化时 500000 会被写成 500000，但 1e-7 这类小数会写成
// 1e-07，所以指数必须支持；而 1e300 这种只能是垃圾数据。
const maxAmountExponent = 30

// rawAmount 接住上游的数值字段：JSON 数字、字符串、null 都收，
// 但**一律按原始文本保存**。
//
// 关键在于绝不在解码这一步落到 float64：json 解码器只要把 12345.67 塞进
// float64，精度就已经丢了，后面再怎么小心也追不回来。NewAPI 的
// channel.balance 与 quota_per_unit 都是 float64 字段，走的正是这条路径。
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
//
// 空值按 0 处理：上游省略字段与上游给 0 在这里等价，
// 「有没有数据」由 Snapshot 表达，不靠金额字段兼职。
func (a rawAmount) minorUnits(scale int) (int64, error) {
	if a.empty() {
		return 0, nil
	}
	return decimalToMinorUnits(string(a), scale)
}

// count 把整数字段（用户数、订单数、quota）换算成 int64，
// scale=0 即可复用同一条路径。
func (a rawAmount) count() (int64, error) { return a.minorUnits(0) }

// decimalToMinorUnits 把十进制金额文本换算成整数最小货币单位。
//
// scale 是该币种的小数位数（USD/CNY 为 2，JPY 为 0）。
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

// quotaToMinorUnits 把 NewAPI 的整数 quota 换算成整数最小货币单位。
//
//	美元 = quota / quota_per_unit          （上游 controller/billing.go 的口径）
//	最小单位 = round(quota * 10^scale / quota_per_unit)
//
// **调用纪律：先把该聚合的 quota 全部 SUM 完，再调一次本函数。**
// 每条明细各自换算再相加，误差会按条数累积——1000 条各差半分就是 5 块钱。
// 这是本文件里最容易被好心改坏的一条：把这个调用挪进循环体，
// 编译得过、测试可能也过，只有对账那天才看得出来。
//
// quotaPerUnit <= 0 时报错而不是当 1 用：上游的 option 写入路径
// （model/option.go 把 ParseFloat 的 error 丢掉了）确实可能把它置成 0，
// 而拿 0 去做除数、或者拿 1 顶上，都会产出一个**看起来正常**的错数字。
func quotaToMinorUnits(quota, quotaPerUnit int64, scale int) (int64, error) {
	if quotaPerUnit <= 0 {
		return 0, fmt.Errorf("quota_per_unit=%d 非正，无法换算: %w", quotaPerUnit, errAmountFormat)
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
		// 放大后会溢出。报错而不是回退到浮点：浮点能算出一个数，
		// 但那个数已经不精确了，而这里报错至少会被看见。
		return 0, fmt.Errorf("quota %d 放大 10^%d 后溢出 int64: %w", quota, scale, errAmountFormat)
	}
	scaled := magnitude * factor

	// +quotaPerUnit/2 就是整数域里的四舍五入；scaled 已经确认不会溢出，
	// 但加上半个除数仍可能顶到上界，所以这一步也要挡一下。
	if scaled > maxInt64-quotaPerUnit/2 {
		return 0, fmt.Errorf("quota %d 换算时溢出 int64: %w", quota, errAmountFormat)
	}
	out := (scaled + quotaPerUnit/2) / quotaPerUnit

	if negative {
		return -out, nil
	}
	return out, nil
}

// ratePPM 把「分子/分母」换算成 ppm（百万分之一）整数。
//
// 比率和金额是同一类东西：一旦落进浮点，0.1% 就不再等于 0.1%，两次采集算出
// 来的同一个比率可能不相等，阈值比较会在边界上抖动（契约 ErrorRatePPM 的注释
// 里写的就是这条）。所以这里也只做整数运算。
//
// 分母为 0 返回 0：没有请求就没有错误率，这不是「未知」而是「没有发生过」。
// 「没测到」是另一回事，由调用方用 Snapshot.IsPartial 表达，不挤进这个返回值。
func ratePPM(numerator, denominator int64) (int64, error) {
	if numerator < 0 || denominator < 0 {
		return 0, fmt.Errorf("比率的分子分母不能为负(%d/%d): %w", numerator, denominator, errAmountFormat)
	}
	if denominator == 0 {
		return 0, nil
	}
	if numerator > denominator {
		// 分子大于分母说明口径搞错了（比如分母漏加了错误条数），
		// 静静地返回一个 >1000000 的 ppm 会让契约套件的定义域断言在别处炸，
		// 那时已经查不回这里了。
		return 0, fmt.Errorf("比率分子 %d 大于分母 %d: %w", numerator, denominator, errAmountFormat)
	}
	const ppmScale = 1_000_000
	if numerator > maxInt64/ppmScale {
		return 0, fmt.Errorf("比率分子 %d 放大 %d 倍后溢出 int64: %w", numerator, ppmScale, errAmountFormat)
	}
	// 四舍五入而不是截断：一个 0.6 ppm 的错误率截断成 0 会让「有错」看起来
	// 像「没错」，而 ppm 的分辨率本来就是为了看清这个量级才选的。
	return (numerator*ppmScale + denominator/2) / denominator, nil
}

func pow10(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}
