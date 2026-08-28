// Package finance 是成本核算的登记簿与写入口（XM-0037a，设计稿 §2.1/§8.3）。
//
// 范围严格限于**登记簿**：每个上游账号一行配置（接入方式、凭据引用、充值倍率、
// 业务日时区）加上「上游令牌 ↔ 自营账号」的映射。利润台账（§2.2）、订阅成本
// 批次与代理资产（§2.5）、看板供数（§8.5）分别属于 XM-0037b / c / d。
//
// 三条贯穿本包的纪律：
//
//   - **凭据只经 CredentialRef**（ADR-014、宪法 7 条）。本包从不解析、
//     从不持有、从不返回明文；查询接口回显的永远只是 `secret://<scope>/<name>`。
//   - **倍率是除数，且是规范存储量**（§3.4）。「充值成本率」= 1/倍率 只是展示
//     投影，不入库；成本计算恒用整数除法（money.Divide），绝不先取倒数再乘。
//   - **金额不在本包出现**。登记簿里一个金额列都没有，成本金额是 037b 的事。
package finance

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// SystemType 是上游系统类型（UI 交接 §13 的 systemType）。
type SystemType string

const (
	// SystemSub2API 是 sub2api 系上游中转（成本走每令牌 /v1/usage，§3.1）。
	SystemSub2API SystemType = "sub2api"
	// SystemNewAPI 是 new-api 系上游中转（成本走 /api/log/self/stat，§3.1）。
	SystemNewAPI SystemType = "newapi"
	// SystemOfficial 是原厂官方 API 直连（成本口径 v1 待定，§2.0/§12）。
	SystemOfficial SystemType = "official"
)

// AccessMethod 是渠道接入方式——**三套成本口径的分叉点**（设计稿 §2.0）。
//
// 它不是一个分类标签：三种取值对应三条完全不同的成本算法，
//
//	upstream_key         实扣 ÷ recharge_ratio（§3.1）★与 SoloAI 逐笔对齐
//	official_api         原厂账单（v1 占位后置，§12 拍板）
//	subscription_account 固定付款按天/按账号摊销（§3.5，XM-0037c）
//
// 选错的后果不是报错，是**一个看起来完全正常的错数字**。
type AccessMethod string

const (
	// AccessUpstreamKey 是计量型上游中转 Key——本次与 SoloAI 影子对比的唯一范围（§9）。
	AccessUpstreamKey AccessMethod = "upstream_key"
	// AccessOfficialAPI 是官方 API 直连（原厂计费）。
	AccessOfficialAPI AccessMethod = "official_api"
	// AccessSubscriptionAccount 是订阅账号（固定月费 + 代理）。
	AccessSubscriptionAccount AccessMethod = "subscription_account"
)

// Status 是登记簿条目的启停状态。
type Status string

const (
	// StatusActive 表示采集任务应当继续读这个账号。
	StatusActive Status = "active"
	// StatusDisabled 是采集侧的 Kill Switch（宪法 26 条）：停用后不再打上游，
	// 但历史台账保持不动。
	StatusDisabled Status = "disabled"
)

// DefaultCurrency 是登记簿的默认计价币种。
//
// 计量型台账全程 USD——sub2api 与 newapi 的成本口径本就是美元
// （SoloAI relaymon/newapi.go 的 NewAPIQuotaPerUSD、sub2api 的 actual_cost 皆然）。
// 显式常量而不是靠库的 DEFAULT：领域层要能独立回答「不填是什么」。
const DefaultCurrency = "USD"

// DefaultBusinessDayTZ 是默认业务日切日时区：CST 固定 +08:00，无夏令时。
//
// ★口径常量（设计稿 §4）：与 SoloAI 的 cstNow（relay_profit.go:60）一致。
// 收入与成本必须共用同一个时间权威——各切各的会让「收入记 D、成本记 D+1」
// 且**不报错**，那种偏差在影子对比里会表现成一整天的错位。
const DefaultBusinessDayTZ = "+08:00"

// DefaultBusinessDayLocation 返回 DefaultBusinessDayTZ 对应的固定偏移时区。
//
// 用 FixedZone 而不是 time.LoadLocation("Asia/Shanghai")：后者依赖容器里有
// tzdata（scratch 镜像里会失败），而且是一个**会随 tzdata 更新而变**的定义。
// 核算口径里的 CST 必须是固定偏移（§4 标 ★）。
//
// 调用方是那些**手里没有具体账号**的地方（如 Query 端点算默认日期窗口）；
// 有账号时一律用 UpstreamAccount.BusinessDayLocation，有台账行时一律用
// ProfitRow.BusinessDayLocation——那两个拿的是各自冻结的偏移，
// 而这个只是平台默认值。
func DefaultBusinessDayLocation() *time.Location {
	loc, err := fixedZone(DefaultBusinessDayTZ)
	if err != nil {
		// 常量本身不合法只可能是有人改坏了它，那属于编译期就该发现的错误。
		panic(err)
	}
	return loc
}

var (
	// ErrNotFound：登记簿里没有这条记录。
	ErrNotFound = errors.New("finance: not found")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("finance: missing required field")
	// ErrInvalidFormat：字段格式非法。
	ErrInvalidFormat = errors.New("finance: invalid format")
	// ErrInconsistent：字段之间自相矛盾（如计量型缺倍率）。
	ErrInconsistent = errors.New("finance: inconsistent fields")
)

// businessDayTZPattern 只接受固定偏移（±HH:MM），不接受 IANA 时区名。
//
// 写 "Asia/Shanghai" 看起来更规范，但那是一个**会随 tzdata 更新而变**的定义；
// 核算口径里的 CST 必须是固定 +08:00 无夏令时（§4 标 ★）。让领域层与库层
// 都只认偏移量，口径就不会随某次基础镜像升级悄悄漂走。
var businessDayTZPattern = regexp.MustCompile(`^[+-][0-9]{2}:[0-9]{2}$`)

// currencyPattern 是 ISO 4217 三位大写字母码。
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// platformIDPattern 是自营平台标识的形态，与 core.service.instance_id 同一套
// （XM-0037b 的台账用它做平台归属分桶，§5.2）。
//
// 两边必须同形，否则一个形态合法但对不上的 platform_id 会永久停在
// 「指向已移除平台」那一桶里，而那一桶本该表示「平台真的下线了」。
var platformIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// UpstreamAccount 是登记簿的一行：一个上游账号 / 渠道的成本配置（§2.1）。
type UpstreamAccount struct {
	ID           uuid.UUID
	SystemType   SystemType
	AccessMethod AccessMethod

	// UpstreamName / Contact / Group 是上游管理页手工维护的当前元数据。
	// 空串 = 未登记，落库为 NULL；它们不参与供应商归并或任何金额计算。
	UpstreamName    string
	UpstreamContact string
	UpstreamGroup   string

	// BaseURL 是上游站网址（https，**不含任何凭证**）。订阅型可能没有可读端点，
	// 故用空串表示「没有」。
	BaseURL string

	// CredentialRef 是账号级凭据引用（ADR-014）：
	//   sub2api → 收入侧 admin key（§3.2）
	//   newapi  → 成本侧会话凭据（§3.1 的 New-Api-User + Cookie）
	// 本字段永远只是引用，明文只在 SecretProvider 内部与拼 HTTP 头那一瞬存在。
	CredentialRef string

	// RechargeRatio 是充值倍率（**除数**，§3.4）。零值表示未配置——
	// 计量型必须有，订阅型必须没有，见 Validate。
	RechargeRatio money.Ratio

	// GroupRate 是自营侧的**分组倍率**（§10.2 + §13 的 groupRate，XM-0049）。
	//
	// ⚠️ **它一次都不参与成本或收入计算。** §10.2 的原话是「分组倍率独立存储 /
	// 展示，不并入 recharge_ratio，前端不重复乘算」。它与 RechargeRatio 是两个
	// 完全不同的量：后者是成本折算的除数（逐行冻结进 ratio_snapshot），
	// 前者只是定价分组的展示标注。把它乘进成本会让每条渠道按各自的分组倍率
	// 错一遍，而那种错在报表上完全看不出来。
	//
	// 零值 = 未配置，且这是绝大多数渠道的正常状态——没有分组倍率就是没有，
	// 不是 1（§13 的 groupRate 本就是可选字段）。
	GroupRate money.Ratio

	Currency string
	// BusinessDayTZ 是业务日切日时区的固定偏移，如 "+08:00"（宪法 14 条）。
	BusinessDayTZ string

	// PlatformID 是「哪个自营平台在用这个上游账号」的归属标注（§5.2，XM-0037c）。
	//
	// 空串 = 未配对，落库为 NULL。它是采集器 PlatformResolver 的取值处——
	// 037b 留下那个钩子时登记簿里还没有任何一列能回答这个问题，于是台账的
	// platform_id 恒为 NULL、四桶恒只剩「未归属」一桶。
	//
	// **登记后可改**，与 AccessMethod（登记后不可改）刻意相反：它只是一条标注，
	// 改它不改变任何一个金额怎么算；而台账侧 platform_id 的 COALESCE 方向是
	// 「空缺可补、已有不动」（§5.3），历史行的归属不会被追溯改写。
	PlatformID string

	Status      Status
	Environment string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TokenMapping 是「上游令牌 ↔ 自营账号」的一条映射（§2.1）。
type TokenMapping struct {
	UpstreamAccountID uuid.UUID

	// UpstreamTokenID 是**成本侧键**：sub2api 为令牌 id，newapi 为 token_name。
	UpstreamTokenID string
	// OwnAccountID 是**收入侧键**：sub2api 为自营账号 id，newapi 为 channel_id。
	OwnAccountID string

	// CredentialRef 是**每令牌**凭据引用，sub2api 成本侧用它去解析明文 sk-
	// （§2.1 正文 + §12 拍板「sk- 走 SecretProvider」）。newapi 留空。
	CredentialRef string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ParseSystemType 解析上游系统类型；只接受精确小写值。
func ParseSystemType(s string) (SystemType, error) {
	switch SystemType(s) {
	case SystemSub2API, SystemNewAPI, SystemOfficial:
		return SystemType(s), nil
	default:
		return "", fmt.Errorf("system_type %q 须为 sub2api / newapi / official: %w",
			s, ErrInvalidFormat)
	}
}

// ParseAccessMethod 解析接入方式；只接受精确小写值。
func ParseAccessMethod(s string) (AccessMethod, error) {
	switch AccessMethod(s) {
	case AccessUpstreamKey, AccessOfficialAPI, AccessSubscriptionAccount:
		return AccessMethod(s), nil
	default:
		return "", fmt.Errorf(
			"access_method %q 须为 upstream_key / official_api / subscription_account: %w",
			s, ErrInvalidFormat)
	}
}

// ParseStatus 解析启停状态。
func ParseStatus(s string) (Status, error) {
	switch Status(s) {
	case StatusActive, StatusDisabled:
		return Status(s), nil
	default:
		return "", fmt.Errorf("status %q 须为 active / disabled: %w", s, ErrInvalidFormat)
	}
}

// IsMetered 报告该接入方式是否走「实扣 ÷ 倍率」的计量口径（§2.0）。
//
// 只有它为真的渠道才进 SoloAI 影子对比（§9），也只有它需要 recharge_ratio。
func (a AccessMethod) IsMetered() bool { return a == AccessUpstreamKey }

// Validate 校验登记簿条目的领域不变量。
//
// 与库层 CHECK 是**同一套规则的两处实现**，这是有意的冗余：领域层能给出
// 说人话的错误（「计量型渠道必须配 recharge_ratio」），库层保证任何写入路径
// ——包括将来某个绕过本包的迁移脚本——都绕不过去。
func (a UpstreamAccount) Validate() error {
	if _, err := ParseSystemType(string(a.SystemType)); err != nil {
		return err
	}
	if _, err := ParseAccessMethod(string(a.AccessMethod)); err != nil {
		return err
	}
	if _, err := ParseStatus(string(a.Status)); err != nil {
		return err
	}
	if strings.TrimSpace(a.Environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}

	// 凭据只能是 CredentialRef。用 secrets.ParseCredentialRef 而不是自己写正则：
	// 两处正则迟早会漂，而这条是安全边界（宪法 7 条）。
	if strings.TrimSpace(a.CredentialRef) == "" {
		return fmt.Errorf("credential_ref: %w", ErrMissingField)
	}
	if err := checkCredentialRef(a.CredentialRef); err != nil {
		return err
	}

	if err := validateBaseURL(a.BaseURL); err != nil {
		return err
	}

	if !currencyPattern.MatchString(a.Currency) {
		return fmt.Errorf("currency %q 须为三位大写 ISO 4217 码: %w", a.Currency, ErrInvalidFormat)
	}
	// 币种必须是**已登记**的：最小单位小数位猜错的那 100 倍不会有任何症状。
	if _, err := money.CurrencyScale(a.Currency); err != nil {
		return fmt.Errorf("currency %q 未登记最小单位小数位（%v）: %w",
			a.Currency, err, ErrInvalidFormat)
	}

	if !businessDayTZPattern.MatchString(a.BusinessDayTZ) {
		return fmt.Errorf("business_day_tz %q 须为固定偏移如 +08:00（不接受 IANA 时区名）: %w",
			a.BusinessDayTZ, ErrInvalidFormat)
	}

	if !a.GroupRate.IsZero() && !a.GroupRate.IsPositive() {
		// 没有算术层的兜底可依赖——group_rate 不参与任何计算，
		// 所以领域层与库层的两道 CHECK 是它仅有的护栏。
		return fmt.Errorf("group_rate=%s 必须为正: %w", a.GroupRate, ErrInvalidFormat)
	}

	if a.PlatformID != "" && !platformIDPattern.MatchString(a.PlatformID) {
		// 与 ProfitRow.Validate 同一条：形态错的 platform_id 永远匹配不上任何
		// 平台，会永久停在四桶的「指向已移除平台」那一桶里，
		// 而那一桶本该表示「平台真的下线了」。
		return fmt.Errorf("platform_id %q 须匹配 %s: %w",
			a.PlatformID, platformIDPattern.String(), ErrInvalidFormat)
	}

	return a.validateRatio()
}

// validateRatio 落实 §2.0 的三套口径对倍率的不同要求。
func (a UpstreamAccount) validateRatio() error {
	hasRatio := !a.RechargeRatio.IsZero()
	if hasRatio && !a.RechargeRatio.IsPositive() {
		// 算术层对 ratio ≤ 0 有「按 1 处理」的兜底（与 SoloAI relay_profit.go:97
		// 一致，§3.1 要求），但**登记簿不生产这种行**：被静默当成 1 的 0 倍率，
		// 会让「上游涨价」与「有人把倍率填成 0」在台账上长得一模一样。
		return fmt.Errorf("recharge_ratio=%s 必须为正: %w", a.RechargeRatio, ErrInvalidFormat)
	}

	switch a.AccessMethod {
	case AccessUpstreamKey:
		if !hasRatio {
			// 缺倍率要在登记这一刻炸，不是等到夜里跑批时台账里出现一列 NULL 成本
			return fmt.Errorf("计量型渠道（upstream_key）必须配 recharge_ratio: %w", ErrInconsistent)
		}
	case AccessSubscriptionAccount:
		if hasRatio {
			// 留一个用不上的倍率在行里，早晚有人拿它去乘一遍
			return fmt.Errorf(
				"订阅型渠道（subscription_account）不得配 recharge_ratio，成本走 §3.5 摊销: %w",
				ErrInconsistent)
		}
	case AccessOfficialAPI:
		// v1 成本口径待定（§2.0/§12 拍板：占位后置），故不约束倍率的有无。
	}
	return nil
}

// baseURLPattern 与库层 CHECK 同一条规则：https 且不含用户信息段。
var baseURLPattern = regexp.MustCompile(`^https://[^@\s]+$`)

func validateBaseURL(raw string) error {
	if raw == "" {
		// 订阅型账号可能没有可读端点；采集侧会在需要 base_url 时报缺配置，
		// 而不是在这里替它决定
		return nil
	}
	if !baseURLPattern.MatchString(raw) {
		// 明文凭证藏在 URL 的 user:pass@ 段里是只读通道最常见的泄漏形态，
		// 所以拒绝 @ 而不只是要求 https（ADR-018 闸 1 的同款判据）。
		//
		// **错误里不回显 raw**：被拒的正是「可能带着 user:pass@ 的那个串」，
		// 把它拼进错误等于顺手把密码写进日志与 HTTP 响应（宪法 7 条）。
		// 一句说清形状要求的话已经足够让人改对，回显反而制造了泄漏面。
		return fmt.Errorf("base_url 必须是 https 且不含 user:pass@ 段（凭据走 credential_ref）: %w",
			ErrInvalidFormat)
	}
	return nil
}

// checkCredentialRef 校验凭据引用的形态。
//
// **错误里不回显被拒的值**，也不包 ParseCredentialRef 的原文——那个错误
// 带着 %q 的输入。这条校验拦下的恰恰是「有人把明文粘进了这个字段」，
// 而领域错误会一路走到 Action 的 Message、进 HTTP 响应、进日志
// （见 httpapi.safeMessage：action.Error 的 Message 是原样回给调用方的）。
// 回显等于把那次手滑变成一次真正的泄漏（宪法 7 条）。
func checkCredentialRef(ref string) error {
	if _, err := secrets.ParseCredentialRef(ref); err != nil {
		return fmt.Errorf("credential_ref 必须是 secret://<scope>/<name> 形态: %w", ErrInvalidFormat)
	}
	return nil
}

// Validate 校验令牌映射的领域不变量。
func (m TokenMapping) Validate() error {
	if m.UpstreamAccountID == uuid.Nil {
		return fmt.Errorf("upstream_account_id: %w", ErrMissingField)
	}
	if strings.TrimSpace(m.UpstreamTokenID) == "" {
		return fmt.Errorf("upstream_token_id: %w", ErrMissingField)
	}
	if strings.TrimSpace(m.OwnAccountID) == "" {
		return fmt.Errorf("own_account_id: %w", ErrMissingField)
	}
	if m.CredentialRef != "" {
		if err := checkCredentialRef(m.CredentialRef); err != nil {
			return err
		}
	}
	return nil
}

// RechargeCostRate 给出 UI 交接 §13 的 `rechargeCostRate`（充值成本率）：
// 1 / recharge_ratio，保留 money.MicroScale 位小数。
//
// **展示投影，不是存储量**（§3.4）：它每次按当前倍率现算，所以两个值不可能
// 漂移；成本计算恒用 money.Divide 的整数除法，绝不走这条乘法路径。
// 未配倍率时返回空串——不编一个 "1.000000" 冒充「没打折」。
func (a UpstreamAccount) RechargeCostRate() string {
	if a.RechargeRatio.IsZero() || !a.RechargeRatio.IsPositive() {
		return ""
	}
	rate, err := money.ReciprocalString(a.RechargeRatio, money.MicroScale)
	if err != nil {
		return ""
	}
	return rate
}

// BusinessDayLocation 把 business_day_tz 解析成 time.Location。
//
// 用固定偏移构造而不是 time.LoadLocation：偏移量没有夏令时规则，
// 也不依赖容器里有没有 tzdata——一个 scratch 镜像里 LoadLocation 会失败，
// 而业务日切日不该因为基础镜像瘦身就换个口径（宪法 14 条）。
func (a UpstreamAccount) BusinessDayLocation() (*time.Location, error) {
	return fixedZone(a.BusinessDayTZ)
}

// fixedZone 把 ±HH:MM 解析成固定偏移时区。
//
// 登记簿（当前配置）与利润台账（逐行冻结的历史值，XM-0037b）共用它：
// 两处各写一遍解析，迟早有一处把 "-05:30" 的符号搞反，而那种错不会报错，
// 只会让某一批业务日整体错开一天。
func fixedZone(offset string) (*time.Location, error) {
	if !businessDayTZPattern.MatchString(offset) {
		return nil, fmt.Errorf("business_day_tz %q 非法: %w", offset, ErrInvalidFormat)
	}
	sign := 1
	if offset[0] == '-' {
		sign = -1
	}
	hours := int(offset[1]-'0')*10 + int(offset[2]-'0')
	minutes := int(offset[4]-'0')*10 + int(offset[5]-'0')
	if hours > 14 || minutes > 59 {
		return nil, fmt.Errorf("business_day_tz %q 偏移超出范围: %w", offset, ErrInvalidFormat)
	}
	return time.FixedZone(offset, sign*(hours*3600+minutes*60)), nil
}
