package finance

// 订阅成本批次、代理资产、摊销损失的领域类型（XM-0037c，设计稿 §2.5/§3.5）。
//
// 这三样与登记簿（§2.1）的关系是「配置 vs 付款」：登记簿说「这条渠道怎么算成本」，
// 这里说「为这条渠道付了多少钱、覆盖哪几天、摊给几个账号」。与利润台账的关系是
// 「输入 vs 事实」：摊销值每天被算一次写进台账，台账过去冻结，这边的登记随时可改
// ——所以改一份代理的挂载状态**不会**追溯改写任何一天的成本（§5.3 免费提供了
// §12 拍板要的「按当日挂载快照」）。

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// buyAddressUserInfoPattern 匹配「URL 里带 user:pass@ 段」，与库层的
// proxy_asset_buy_address_no_userinfo 是同一条规则的两处实现。
var buyAddressUserInfoPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*://[^/]*@`)

const (
	// AccountGrainPrefix 是**账号级聚合行**的 token_id 前缀（§12.2 的「渠道键」）。
	//
	// 台账的主键第三段是 token_id，而两类行没有「那一个令牌」可填：
	//   - 多把令牌供给同一个自营账号时的计量型聚合行（成本 = 各令牌之和）；
	//   - 订阅型渠道的摊销行（成本是账号级的一笔摊销，压根没有令牌维度）。
	//
	// 哨兵形如 `account:<own_account_id>`,而不是空串——空串会让同一个上游账号下
	// **两个各自挂多把令牌的自营账号**撞主键，后写的把前一个整行覆盖掉：
	// 金额少一块，剩下那一行看起来完全正常。库层的
	// profit_daily_account_grain_token_id 与 token_map_upstream_token_id_not_account_grain
	// 两条 CHECK 把哨兵与真令牌的命名空间彻底切开（000010）。
	AccountGrainPrefix = "account:"

	// resourceSubscriptionBatch / resourceProxyAsset 是审计里的资源类型，与库表同名。
	resourceSubscriptionBatch = "finance.subscription_cost_batch"
	resourceProxyAsset        = "finance.proxy_asset"
)

var (
	// ErrNoAmortizableBatch：这一天没有任何批次覆盖该账号 → 成本**未知**，不是 0。
	//
	// 「还没登记批次」与「订阅真的到期了」在库里长得一模一样，平台分不出来。
	// 写 0 会让这条渠道显示一个笃定的「零成本、毛利 = 收入」（§5.1 要挡的那个
	// 错数字）；写 NULL 并把账号名报进日志，运营才知道该去补一笔批次
	// 还是该把账号停用（宪法 12 条）。
	ErrNoAmortizableBatch = errors.New("finance: 当日无覆盖的订阅批次，摊销成本未知")

	// ErrAmortizationMixedCurrency：同一账号当日的批次 / 代理币种不一致。
	//
	// 不同币种的最小单位不能相加，那个和是纯粹的错数（同 metering 的
	// mixed_currency 纪律）。宁可这一天不入账，也不给一个不知道错在哪的数。
	ErrAmortizationMixedCurrency = errors.New("finance: 当日摊销币种不一致，金额不可相加")

	// ErrBatchImmutableField：批次登记后不可改的字段被改了。
	//
	// 金额、期间、账号数登记后冻结（§3.5：续费 = 新批次不覆盖历史）。
	// 允许原地改会让「上个月按 99 摊」在改完之后变成「上个月按 129 摊」，
	// 而历史台账已经按 99 入过账了——两份记录从此对不上且没有报错。
	ErrBatchImmutableField = errors.New("finance: 该字段登记后不可修改，请新登记一个批次")

	// ErrRefundNotDecreasing：退款额只增不减（§3.5：部分退款冲减成本基础）。
	ErrRefundNotDecreasing = errors.New("finance: 累计退款额只增不减")

	// ErrAlreadyTerminated：已经终止过一次。
	//
	// 终止是一次性事件：它结转一笔损失。允许改终止日等于让那笔损失
	// 悄悄变个数，而它已经出现在某一天的报表上了。
	ErrAlreadyTerminated = errors.New("finance: 已终止，终止日不可再改")
)

// AccountGrainTokenID 构造账号级聚合行的哨兵 token_id。
func AccountGrainTokenID(ownAccountID string) string {
	return AccountGrainPrefix + strings.TrimSpace(ownAccountID)
}

// IsAccountGrain 报告一行台账是不是账号级聚合行。
//
// 037d 的看板下钻要用它：聚合行没有令牌可展开，展开会显示一个不存在的令牌。
// 用函数而不是让每处各写一次 strings.HasPrefix——前缀改了之后，
// 漏改的那一处会把聚合行当成一个真令牌显示出来。
func IsAccountGrain(tokenID string) bool {
	return strings.HasPrefix(tokenID, AccountGrainPrefix)
}

// AccountGrainOwner 从哨兵里取回自营账号 id；不是哨兵时返回 false。
func AccountGrainOwner(tokenID string) (string, bool) {
	if !IsAccountGrain(tokenID) {
		return "", false
	}
	return strings.TrimPrefix(tokenID, AccountGrainPrefix), true
}

// SubscriptionBatch 是一笔订阅付款 = 一个成本批次（§2.5）。
//
// **续费是新增一行，不是改这一行**（§3.5/§10.3）。同一个上游账号下可以有多条，
// 当日成本取全部覆盖当日的批次之和。
type SubscriptionBatch struct {
	ID                uuid.UUID
	UpstreamAccountID uuid.UUID

	// 金额一律 scale-6 微单位（§2.4）。PaidMinor / SurchargeMinor 登记后不可改，
	// RefundedMinor 只增不减——理由见 ErrBatchImmutableField / ErrRefundNotDecreasing。
	PaidMinor      int64
	SurchargeMinor int64
	RefundedMinor  int64
	// RefundedOn 是退款生效的业务日；零值 = 无退款。
	RefundedOn time.Time
	Currency   string

	// StartsOn / ExpiresOn 含两端（§12 拍板）；TerminatedOn 零值 = 未终止。
	StartsOn     time.Time
	ExpiresOn    time.Time
	TerminatedOn time.Time

	// AccountCount 是这批订阅覆盖几个账号；本批次只摊到自己这一个账号头上。
	AccountCount int

	// ProxyAssetID 是关联的代理资产；uuid.Nil = 无代理，代理成本为 0（§2.5）。
	//
	// 库里的列名是 `proxy_batch_id`（沿用设计稿 §2.5 的命名），Go 侧叫
	// ProxyAssetID——它引用的是 proxy_asset，代理没有「批次」这个概念。
	ProxyAssetID uuid.UUID

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProxyAsset 是一份代理资产（§2.5/§10.3）。
type ProxyAsset struct {
	ID uuid.UUID

	PaidMinor      int64
	SurchargeMinor int64
	RefundedMinor  int64
	RefundedOn     time.Time
	Currency       string

	OpenedOn     time.Time
	ExpiresOn    time.Time
	TerminatedOn time.Time

	// SharedAccountCount 是分摊账号数（§10.3）。
	SharedAccountCount int

	BuyPlatform string
	BuyAddress  string
	// CredentialRef 只存引用（ADR-014）。代理的账号密码一步都不进库。
	CredentialRef string

	// Mounted=false → 每日代理成本**明确为 0**（§10.3/§6.4）。
	// 注意是「已知的 0」而不是「未知」：没挂上的代理确实没在为这个账号服务。
	Mounted bool

	Environment string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AmortizationLoss 是一笔提前失效结转的损失（§12 拍板第 5 项）。
//
// **单列科目，不进渠道当日成本**：计进 cost_minor 会让失效当天的渠道毛利
// 凭空塌一个月的量，看板上像是渠道出了事，而真相是我们退订了一个没用完的订阅。
type AmortizationLoss struct {
	ID uuid.UUID
	// BatchID 与 ProxyAssetID 恰好有一个非零（库层 CHECK 保证）。
	BatchID      uuid.UUID
	ProxyAssetID uuid.UUID

	// LossMinor 有符号：负值 = 终止后又收到大额退款产生的贷记，见
	// AmortizationTerm.UnamortizedMinor。
	LossMinor int64
	Currency  string
	BookedOn  time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Term 把批次映射成摊销段。
func (b SubscriptionBatch) Term() AmortizationTerm {
	return AmortizationTerm{
		PaidMinor:      b.PaidMinor,
		SurchargeMinor: b.SurchargeMinor,
		RefundedMinor:  b.RefundedMinor,
		RefundedOn:     b.RefundedOn,
		StartsOn:       b.StartsOn,
		ExpiresOn:      b.ExpiresOn,
		TerminatedOn:   b.TerminatedOn,
		ShareCount:     b.AccountCount,
	}
}

// Term 把代理资产映射成摊销段。
//
// 注意它**不含 Mounted**：挂载与否不是摊销区间的一部分，而是「这一天算不算钱」
// 的开关。混进 Term 会让一份未挂载的代理表现成「不在期内」，
// 于是「没挂上」与「还没开通」在计数上分不开（见 DailyMinor）。
func (p ProxyAsset) Term() AmortizationTerm {
	return AmortizationTerm{
		PaidMinor:      p.PaidMinor,
		SurchargeMinor: p.SurchargeMinor,
		RefundedMinor:  p.RefundedMinor,
		RefundedOn:     p.RefundedOn,
		StartsOn:       p.OpenedOn,
		ExpiresOn:      p.ExpiresOn,
		TerminatedOn:   p.TerminatedOn,
		ShareCount:     p.SharedAccountCount,
	}
}

// DailyMinor 返回代理在某业务日应摊的金额，以及这一天是否在期内。
//
// **未挂载时返回 (0, true)**：那是一个**已知的 0**（§10.3 逐字：「代理未挂载
// → 每日代理成本 = 0」），不是「不在期内」。两者必须分开——前者说明这份代理
// 今天确实没在为这个账号服务，后者说明它今天根本不该被算进来。
func (p ProxyAsset) DailyMinor(day time.Time) (int64, bool, error) {
	term := p.Term()
	if err := term.Validate(); err != nil {
		return 0, false, err
	}
	if !term.Covers(day) {
		return 0, false, nil
	}
	if !p.Mounted {
		return 0, true, nil
	}
	return term.DailyMinor(day)
}

// Validate 校验批次的领域不变量（与 000010 的 CHECK 同一套规则）。
func (b SubscriptionBatch) Validate() error {
	if b.UpstreamAccountID == uuid.Nil {
		return fmt.Errorf("upstream_account_id: %w", ErrMissingField)
	}
	if err := validateAmountCurrency(b.Currency); err != nil {
		return err
	}
	return b.Term().Validate()
}

// Validate 校验代理资产的领域不变量。
func (p ProxyAsset) Validate() error {
	if strings.TrimSpace(p.Environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	if err := validateAmountCurrency(p.Currency); err != nil {
		return err
	}
	if p.CredentialRef != "" {
		// 错误里不回显被拒的值：这条校验拦下的恰恰是「有人把代理密码
		// 粘进了这个字段」，回显等于把那次手滑变成一次真正的泄漏（宪法 7 条）。
		if err := checkCredentialRef(p.CredentialRef); err != nil {
			return err
		}
	}
	if err := validateBuyAddress(p.BuyAddress); err != nil {
		return err
	}
	return p.Term().Validate()
}

// validateAmountCurrency 校验币种形态且必须是**已登记**的。
//
// 猜错最小单位小数位的那 100 倍不会有任何症状，只会让所有金额静静地错着
// （同 UpstreamAccount.Validate 的同一条）。
func validateAmountCurrency(code string) error {
	if !currencyPattern.MatchString(code) {
		return fmt.Errorf("currency %q 须为三位大写 ISO 4217 码: %w", code, ErrInvalidFormat)
	}
	if _, err := money.CurrencyScale(code); err != nil {
		return fmt.Errorf("currency %q 未登记最小单位小数位（%v）: %w", code, err, ErrInvalidFormat)
	}
	return nil
}

// validateBuyAddress 拒绝 URL 形态里的 user:pass@ 段。
//
// 与 base_url 同一条判据、同一个理由：那是明文凭据最常见的藏身处。
// 纯文字地址不受限——代理未必从一个网站买来。
// **错误里不回显输入**（宪法 7 条）。
func validateBuyAddress(raw string) error {
	if raw == "" {
		return nil
	}
	if buyAddressUserInfoPattern.MatchString(raw) {
		return fmt.Errorf("buy_address 不得在 URL 里带 user:pass@ 段（凭据走 credential_ref）: %w",
			ErrInvalidFormat)
	}
	return nil
}

// AmortizableBatch 是一笔覆盖某业务日的批次，连同它引用的代理资产。
//
// 代理随批次一起取回（一次 LEFT JOIN），而不是逐批次再查一次：
// 一个账号当日通常只有一两条批次，N+1 换不来任何东西，
// 却会让「代理读失败」变成一种独立的、要单独处理的失败形态。
type AmortizableBatch struct {
	Batch SubscriptionBatch
	// Proxy 为 nil 表示这批订阅不走代理（proxy_batch_id 为 NULL）。
	Proxy *ProxyAsset
}

// DailySupplyCost 是一个订阅型账号在某业务日的供给成本（§3.5）。
type DailySupplyCost struct {
	// CostMinor 是当日全部覆盖批次的订阅摊销 + 代理摊销之和。
	CostMinor int64
	Currency  string

	// BatchCount / ProxyCount 是实际计入的批次数与代理数。
	//
	// 与金额一起给才让金额可解释（宪法 12 条）：一个 $3.20 的日成本，
	// 来自一条批次还是三条批次续费叠加，运营要能一眼看出来。
	BatchCount int
	ProxyCount int

	// DedupedProxies 是被去重掉的重复代理引用数。
	//
	// 同一个账号的两条批次引用同一份代理时（续费期重叠是常态），那份代理
	// 当日只算**一次**——它就一份，不会因为被两条批次指着就贵一倍。
	// 计数留出来是为了让这件事在日志里看得见，而不是悄悄发生。
	DedupedProxies int
}

// AmortizeDay 算一个订阅型账号在某业务日的供给成本（§3.5 的四行公式）。
//
// 没有任何批次覆盖当日时返回 ErrNoAmortizableBatch——**成本未知，不是 0**，
// 理由见那个错误的注释。币种不一致时返回 ErrAmortizationMixedCurrency。
func AmortizeDay(batches []AmortizableBatch, day time.Time) (DailySupplyCost, error) {
	var out DailySupplyCost
	seenProxy := make(map[uuid.UUID]struct{}, len(batches))

	for _, item := range batches {
		amount, covered, err := item.Batch.Term().DailyMinor(day)
		if err != nil {
			return DailySupplyCost{}, fmt.Errorf("批次 %s: %w", item.Batch.ID, err)
		}
		if !covered {
			continue
		}
		if err := out.add(amount, item.Batch.Currency); err != nil {
			return DailySupplyCost{}, err
		}
		out.BatchCount++

		if item.Proxy == nil {
			continue
		}
		if _, duplicate := seenProxy[item.Proxy.ID]; duplicate {
			out.DedupedProxies++
			continue
		}
		seenProxy[item.Proxy.ID] = struct{}{}

		proxyAmount, proxyCovered, err := item.Proxy.DailyMinor(day)
		if err != nil {
			return DailySupplyCost{}, fmt.Errorf("代理 %s: %w", item.Proxy.ID, err)
		}
		if !proxyCovered {
			// 代理的有效期与订阅的不必一致：代理先到期是常见形态。
			// 那几天的代理成本确实是 0，但**不计入 ProxyCount**——
			// 「有一份代理但今天没在期内」与「今天摊了一份代理」是两回事。
			continue
		}
		if err := out.add(proxyAmount, item.Proxy.Currency); err != nil {
			return DailySupplyCost{}, err
		}
		out.ProxyCount++
	}

	if out.BatchCount == 0 {
		return DailySupplyCost{}, ErrNoAmortizableBatch
	}
	return out, nil
}

// add 累加一笔当日摊销并守住币种一致与 int64 边界。
func (c *DailySupplyCost) add(amount int64, currency string) error {
	switch {
	case c.Currency == "":
		c.Currency = currency
	case c.Currency != currency:
		return fmt.Errorf("%s 与 %s: %w", c.Currency, currency, ErrAmortizationMixedCurrency)
	}
	if (amount > 0 && c.CostMinor > maxInt64Amount-amount) ||
		(amount < 0 && c.CostMinor < -maxInt64Amount-1-amount) {
		return fmt.Errorf("当日摊销合计超出 int64: %w", ErrInvalidFormat)
	}
	c.CostMinor += amount
	return nil
}
