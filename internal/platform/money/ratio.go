package money

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Ratio 是定点十进制倍率：值 = num / 10^scale（宪法 13 条：比例使用 Decimal）。
//
// 它承载 XM-0037 的 `recharge_ratio`（充值倍率，**除数**，对齐 SoloAI 迁移 0084）。
// 设计稿 §3.4 明确了规范表示的选择理由：影子对比要与 SoloAI 逐笔 0 差异，
// 就必须用**同一个数**、做**同一种运算（÷）**。UI 交接 §10.2 的
// 「充值成本率」= 1 / recharge_ratio 只是展示投影（见 ReciprocalString），
// **不作为规范存储量**，成本计算也绝不先把它舍入成有限小数再乘——
// 那会二次舍入，破坏影子对齐。
//
// 零值表示**未配置**，与「配置成 0」是两件事——后者非法但可表达，
// 前者是订阅型渠道的正常状态（§2.0）。两者靠 set 区分：没有它的话
// ParseRatio("0") 与从未赋值会给出同一个结构体，于是「订阅型不该有倍率」
// 与「有人把倍率填成 0」在校验里长得一模一样，只有一条能报出来。
// 只能经 ParseRatio / NewRatio 构造。
type Ratio struct {
	num   int64
	scale int32
	set   bool
}

// NewRatio 用「分子 + 标度」直接构造倍率（值 = num / 10^scale）。
//
// 主要给 pgtype.Numeric 的转换与测试用：从库里读回的 NUMERIC 天然就是
// 这个形状（Int + Exp），不必再经一次文本往返。
func NewRatio(num int64, scale int32) (Ratio, error) {
	if scale < 0 || scale > maxRatioScale {
		return Ratio{}, fmt.Errorf("倍率标度 %d 超出 0~%d: %w", scale, maxRatioScale, ErrFormat)
	}
	return Ratio{num: num, scale: scale, set: true}, nil
}

// ParseRatio 解析定点倍率文本（如 "1.5"、"0.85"、"2"）。
//
// 不接受科学计数法：倍率是人手工登记的运营参数，"1.5e0" 这种写法只可能是
// 粘错了地方。金额字段要收科学计数法是因为上游可能用 float64 序列化，
// 倍率没有那条来路。
func ParseRatio(raw string) (Ratio, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ratio{}, fmt.Errorf("空倍率: %w", ErrFormat)
	}
	if len(s) > maxAmountTextLen {
		return Ratio{}, fmt.Errorf("倍率文本过长(%d): %w", len(s), ErrFormat)
	}

	negative := false
	switch s[0] {
	case '-':
		negative = true
		s = s[1:]
	case '+':
		s = s[1:]
	}

	intPart, fracPart, hasPoint := strings.Cut(s, ".")
	if hasPoint && strings.Contains(fracPart, ".") {
		return Ratio{}, fmt.Errorf("多个小数点: %w", ErrFormat)
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return Ratio{}, fmt.Errorf("含非数字字符: %w", ErrFormat)
	}
	if intPart == "" && fracPart == "" {
		return Ratio{}, fmt.Errorf("没有数字: %w", ErrFormat)
	}
	if len(fracPart) > maxRatioScale {
		return Ratio{}, fmt.Errorf("倍率小数位 %d 超出上限 %d: %w",
			len(fracPart), maxRatioScale, ErrFormat)
	}

	num, err := parseDigits(intPart + fracPart)
	if err != nil {
		return Ratio{}, err
	}
	if negative {
		num = -num
	}
	return Ratio{num: num, scale: int32(len(fracPart)), set: true}, nil
}

// MustParseRatio 供测试与静态装配使用；解析失败即 panic。
func MustParseRatio(s string) Ratio {
	r, err := ParseRatio(s)
	if err != nil {
		panic(err)
	}
	return r
}

// Num 返回分子（值 = Num / 10^Scale）。
func (r Ratio) Num() int64 { return r.num }

// Scale 返回小数位数。
func (r Ratio) Scale() int32 { return r.scale }

// IsZero 报告倍率**未配置**（零值，从未经 ParseRatio / NewRatio 构造）。
//
// 它与「倍率是 0」不同：后者 IsZero() 为 false、IsPositive() 也为 false，
// 会被登记入口当作非法值拒绝。这个区分是必要的——订阅型渠道**本就该**
// 没有倍率（§2.0），而 0 倍率是有人填错了，两者要报出不同的错。
func (r Ratio) IsZero() bool { return !r.set }

// IsPositive 报告倍率是否为正。
//
// 非正倍率在折算时按 1 处理（见 Divide 的注释），但登记簿在库层拒绝它——
// 两条纪律的分工见那里。
func (r Ratio) IsPositive() bool { return r.num > 0 }

// String 还原定点十进制文本。往返稳定：ParseRatio(r.String()) == r。
//
// 未配置的倍率返回空串而不是 "0"：把「没有倍率」渲染成一个具体的数，
// 正是宪法 12 条禁止的那种沉默。
func (r Ratio) String() string {
	if !r.set {
		return ""
	}
	return fixedDecimalString(r.num, int(r.scale))
}

// fixedDecimalString 把「分子 + 标度」渲染成定点十进制文本。
//
// 纯字符串拼接，不经 strconv.FormatFloat：把 num/10^scale 交给浮点格式化
// 正是本包存在的理由要挡的那一步。
func fixedDecimalString(num int64, scale int) string {
	negative := num < 0
	magnitude := uint64(num)
	if negative {
		// 取负用 uint64 承接，避免 math.MinInt64 取反溢出
		magnitude = -uint64(num)
	}
	digits := strconv.FormatUint(magnitude, 10)
	var out string
	switch {
	case scale <= 0:
		out = digits
	case len(digits) <= scale:
		out = "0." + strings.Repeat("0", scale-len(digits)) + digits
	default:
		out = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if negative {
		return "-" + out
	}
	return out
}

// divRoundHalfUp 计算 num/den 并按半进（away from zero）舍入。
//
// den 必须为正——调用方在进来之前已经判过，这里不再兜一次：
// 一个静默返回 0 的除零分支比 panic 难查得多。
//
// 半进的判据是 |余数| × 2 ≥ 除数，用乘 2 而不是把除数除 2：
// 除数为奇数时 den/2 会把边界判偏半个单位，正好落在
// 「真值恰在半分边界」那一类个例上（设计稿 §9 的容差旋钮就是为它留的）。
func divRoundHalfUp(num, den *big.Int) *big.Int {
	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	doubled := new(big.Int).Abs(remainder)
	doubled.Lsh(doubled, 1)
	if doubled.Cmp(den) >= 0 {
		if num.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	return quotient
}

// Divide 把上游实扣折算成平台成本：cost = usage ÷ ratio（设计稿 §2.4/§3.1）。
//
// 展开成整数运算就是 `round_halfup(usage × 10^ratioScale / ratio_num)`：
// 全程整数，唯一的舍入发生在最后一步，误差 ≤ 0.5 个最小单位。设计稿 §2.4
// 证明了这条整数路与 SoloAI 的 float64 除法在「分」粒度必然落到同一个数
// （worked example：actual_cost="5.813729"、ratio=1.5 两边都得 $3.88），
// 而在分以下**平台这条路更准**——SoloAI 那边的 float64 早就有 ~1e-16 的尾数噪声。
//
// 中间乘积走 math/big：usage 是 scale-6 的 int64，再乘 10^ratioScale（最多 10^9）
// 会在 usage 超过约 9.2×10^9 微单位（约 $9200）时溢出 int64——那是一个**每天
// 都会踩到**的量级，不是理论边界。big.Int 是精确整数，不引入任何舍入。
//
// **ratio ≤ 0 按 1 处理**，与 SoloAI `relay_profit.go:97` 一致（设计稿 §3.1
// 逐字要求）。这条兜底在平台上**不可达**：登记簿的库层 CHECK 拒绝非正倍率
// （见 000008 迁移的 upstream_account_recharge_ratio_positive），所以本分支
// 只是让算术层与标准答案逐条对齐，而不是给平台留一条「悄悄把垃圾当 1」的路
// ——那正是宪法 12 条禁止的沉默。
func Divide(usageMinorUnits int64, ratio Ratio) (int64, error) {
	if !ratio.IsPositive() {
		return usageMinorUnits, nil
	}
	numerator := new(big.Int).Mul(
		big.NewInt(usageMinorUnits),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(ratio.scale)), nil),
	)
	quotient := divRoundHalfUp(numerator, big.NewInt(ratio.num))
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("折算结果 %s 超出 int64: %w", quotient, ErrOverflow)
	}
	return quotient.Int64(), nil
}

// DivideByUnits 把整数计数按「每单位货币多少计数」折算成 scale 标度的金额。
//
// NewAPI 的成本就是这个形状（设计稿 §3.1）：上游给的是整数 credits（quota），
// 每 `unitsPerWhole` 个 credits 折 1 单位货币（口径常量 500000，§4 标 ★），于是
//
//	minor = round_halfup(units × 10^scale / unitsPerWhole)
//
// unitsPerWhole=500000、scale=6 时结果恒为 `units × 2`——**精确无舍入**，
// 这正是 §2.4 选 scale-6 的第一条理由。
//
// 调用方必须**先把要合计的 quota 整数相加、再调一次本函数**（「先 SUM 再除」）：
// 逐条折算后相加会让每条各带半个微单位的舍入，几十条累加就够在分粒度上抖一次。
func DivideByUnits(units int64, unitsPerWhole int64, scale int) (int64, error) {
	if unitsPerWhole <= 0 {
		return 0, fmt.Errorf("每单位计数 %d 必须为正: %w", unitsPerWhole, ErrFormat)
	}
	if scale < 0 || scale > maxScale {
		return 0, fmt.Errorf("scale %d 超出 0~%d: %w", scale, maxScale, ErrFormat)
	}
	numerator := new(big.Int).Mul(
		big.NewInt(units),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil),
	)
	quotient := divRoundHalfUp(numerator, big.NewInt(unitsPerWhole))
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("折算结果 %s 超出 int64: %w", quotient, ErrOverflow)
	}
	return quotient.Int64(), nil
}

// ReciprocalString 给出「充值成本率」的展示投影：1 / recharge_ratio，
// 保留 scale 位小数（半进）。
//
// 它**只用于展示**（UI 交接 §13 的 `UpstreamSummary.rechargeCostRate`），
// 设计稿 §3.4 明确要求不另存这个值，以免两个数漂移；成本计算恒用 Divide。
// 返回定点字符串而不是 Ratio：调用方拿到它只该往 JSON 里放，
// 类型上就不给「再拿去算一次成本」的机会。
func ReciprocalString(r Ratio, scale int) (string, error) {
	if !r.IsPositive() {
		return "", fmt.Errorf("非正倍率没有可展示的倒数: %w", ErrFormat)
	}
	if scale < 0 || scale > maxScale {
		return "", fmt.Errorf("scale %d 超出 0~%d: %w", scale, maxScale, ErrFormat)
	}
	// 1/r = 10^ratioScale / num，放大到 scale 位小数：
	//   value = round_halfup(10^(scale+ratioScale) / num)
	numerator := new(big.Int).Exp(
		big.NewInt(10), big.NewInt(int64(scale)+int64(r.scale)), nil)
	quotient := divRoundHalfUp(numerator, big.NewInt(r.num))
	if !quotient.IsInt64() {
		return "", fmt.Errorf("倒数 %s 超出 int64: %w", quotient, ErrOverflow)
	}
	return fixedDecimalString(quotient.Int64(), scale), nil
}

// RatioString 把两个整数最小单位的量相除，渲染成定点十进制**字符串**。
//
// 用途是毛利率这类展示比率（§3.3：`margin = grossProfit / usageRevenue`）。
// 三条纪律：
//
//   - **全程整数**：分子先放大 10^scale 再整除并半进，一次舍入，零 float
//     （宪法 13 条）。中间乘积走 math/big——毛利可以是几百万微单位，
//     再乘 10^6 就出 int64 了。
//   - **返回字符串而不是数值**：比率一旦以 JSON 数字出去，前端会用 double
//     接住它，0.15 变成 0.15000000000000002。与 ReciprocalString 同一条。
//   - **分母 ≤ 0 时报错，不返回 0**：`revenue ≤ 0` 时毛利率没有意义
//     （对齐 SoloAI relay_profit.go:331 的 nil），调用方据此显示「—」。
//     返回 0 会让「没有收入」看起来像「毛利率是零」。
func RatioString(numerator, denominator int64, scale int) (string, error) {
	if denominator <= 0 {
		return "", fmt.Errorf("分母 %d 必须为正（收入 ≤ 0 时毛利率无意义）: %w",
			denominator, ErrFormat)
	}
	if scale < 0 || scale > maxScale {
		return "", fmt.Errorf("scale %d 超出 0~%d: %w", scale, maxScale, ErrFormat)
	}
	num := new(big.Int).Mul(
		big.NewInt(numerator),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil),
	)
	quotient := divRoundHalfUp(num, big.NewInt(denominator))
	if !quotient.IsInt64() {
		return "", fmt.Errorf("比率 %s 超出 int64: %w", quotient, ErrOverflow)
	}
	return fixedDecimalString(quotient.Int64(), scale), nil
}
