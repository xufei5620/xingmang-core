package finance_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// testCredentialRef 是用例共用的凭据引用。
//
// 抽成常量而不是就地写字面量：`CredentialRef: "secret://..."` 这个形状会被
// gitleaks 的 generic-api-key 规则当成泄露的密钥（同一条误报见
// internal/platform/alerts/store_integration_test.go 的说明）。本仓禁止加
// gitleaks allowlist（会顺手掩盖真报，见 scripts/check-governance.sh），
// 所以换个写法比放宽扫描器划算。**常量名里也不能带 key/token/secret**——
// 那条规则看的是「标识符含这些词 + 赋值 + 一串高熵值」。
//
// 值本身是 fixture，指向一个不存在的 scope；ADR-014 与宪法 7 条要求
// fixture 里禁出现真秘密，而 CredentialRef 本来就只是引用，不是秘密。
const testCredentialRef = "secret://finance-test/upstream-a"

func meteredAccount() finance.UpstreamAccount {
	return finance.UpstreamAccount{
		ID:            uuid.New(),
		SystemType:    finance.SystemSub2API,
		AccessMethod:  finance.AccessUpstreamKey,
		BaseURL:       "https://api.example.test",
		CredentialRef: testCredentialRef,
		RechargeRatio: money.MustParseRatio("1.5"),
		Currency:      finance.DefaultCurrency,
		BusinessDayTZ: finance.DefaultBusinessDayTZ,
		Status:        finance.StatusActive,
		Environment:   "production",
	}
}

func TestValidateAcceptsMeteredAccount(t *testing.T) {
	if err := meteredAccount().Validate(); err != nil {
		t.Fatalf("合法计量型账号应通过校验: %v", err)
	}
}

// TestMeteredAccountRequiresRechargeRatio 是 §2.0 的核心不变量。
//
// 缺倍率的计量型渠道算不出成本，而算不出成本在台账里表现为 cost NULL，
// 一路传染到毛利。这个错必须在**登记**这一刻炸，不是等到夜里跑批。
func TestMeteredAccountRequiresRechargeRatio(t *testing.T) {
	a := meteredAccount()
	a.RechargeRatio = money.Ratio{}
	err := a.Validate()
	if !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("计量型缺倍率必须报 ErrInconsistent，got %v", err)
	}
}

// TestSubscriptionAccountRejectsRechargeRatio：订阅型的成本走 §3.5 摊销，
// 留一个用不上的倍率在行里，早晚有人拿它去乘一遍。
func TestSubscriptionAccountRejectsRechargeRatio(t *testing.T) {
	a := meteredAccount()
	a.AccessMethod = finance.AccessSubscriptionAccount
	err := a.Validate()
	if !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("订阅型带倍率必须报 ErrInconsistent，got %v", err)
	}

	// 去掉倍率就该通过
	a.RechargeRatio = money.Ratio{}
	if err := a.Validate(); err != nil {
		t.Fatalf("订阅型不带倍率应通过: %v", err)
	}
}

// TestOfficialAPIRatioIsUnconstrained：official_api 的成本口径 v1 待定
// （§2.0/§12 拍板占位后置），所以对它的倍率不作要求——
// 两条都不该覆盖它。写死成「必须有」或「必须没有」都是替产品拍板。
func TestOfficialAPIRatioIsUnconstrained(t *testing.T) {
	a := meteredAccount()
	a.SystemType = finance.SystemOfficial
	a.AccessMethod = finance.AccessOfficialAPI
	if err := a.Validate(); err != nil {
		t.Fatalf("official_api 带倍率应通过: %v", err)
	}
	a.RechargeRatio = money.Ratio{}
	if err := a.Validate(); err != nil {
		t.Fatalf("official_api 不带倍率也应通过: %v", err)
	}
}

// TestValidateRejectsNonPositiveRatio 锁住「登记簿不生产被静默当成 1 的倍率」。
//
// 算术层对 ratio ≤ 0 有对齐 SoloAI 的兜底（money.Divide），但那条路只用于
// 与标准答案一致；登记入口一律拒绝，否则「上游涨价」与「倍率填成 0」
// 在台账上长得一模一样（宪法 12 条）。
func TestValidateRejectsNonPositiveRatio(t *testing.T) {
	for _, raw := range []string{"0", "0.000", "-1.5"} {
		a := meteredAccount()
		a.RechargeRatio = money.MustParseRatio(raw)
		if err := a.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
			t.Fatalf("倍率 %q 必须被拒，got %v", raw, err)
		}
	}
}

func TestValidateRejectsPlaintextCredential(t *testing.T) {
	for _, ref := range []string{
		"",
		"not-a-ref",
		"https://user:pass@example.test",
		"secret://UPPER/name",
		"secret://scope",
	} {
		a := meteredAccount()
		a.CredentialRef = ref
		if err := a.Validate(); err == nil {
			t.Fatalf("credential_ref=%q 必须被拒（ADR-014）", ref)
		}
	}
}

// TestValidateRejectsCredentialInBaseURL：明文凭证藏在 URL 的 user:pass@ 段里
// 是只读通道最常见的泄漏形态（ADR-018 闸 1 的同款判据）。
func TestValidateRejectsBadBaseURL(t *testing.T) {
	for _, url := range []string{
		"http://api.example.test",            // 非 https
		"https://user:pass@api.example.test", // 含凭证段
		"api.example.test",                   // 无协议
	} {
		a := meteredAccount()
		a.BaseURL = url
		if err := a.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
			t.Fatalf("base_url=%q 必须被拒，got %v", url, err)
		}
	}
	// 空 base_url 合法：订阅型账号可能没有可读端点
	a := meteredAccount()
	a.AccessMethod = finance.AccessSubscriptionAccount
	a.RechargeRatio = money.Ratio{}
	a.BaseURL = ""
	if err := a.Validate(); err != nil {
		t.Fatalf("空 base_url 应合法: %v", err)
	}
}

// TestValidateRejectsIANATimezone 锁住 §4 的 ★口径常量。
//
// "Asia/Shanghai" 是一个**会随 tzdata 更新而变**的定义；核算口径里的 CST
// 必须是固定 +08:00 无夏令时。让领域层只认偏移量，口径就不会随某次
// 基础镜像升级悄悄漂走。
func TestValidateRejectsIANATimezone(t *testing.T) {
	for _, tz := range []string{"Asia/Shanghai", "CST", "UTC", "+8:00", "0800", ""} {
		a := meteredAccount()
		a.BusinessDayTZ = tz
		if err := a.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
			t.Fatalf("business_day_tz=%q 必须被拒，got %v", tz, err)
		}
	}
}

func TestValidateRejectsUnregisteredCurrency(t *testing.T) {
	for _, code := range []string{"", "usd", "USDT", "XYZ"} {
		a := meteredAccount()
		a.Currency = code
		if err := a.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
			t.Fatalf("currency=%q 必须被拒，got %v", code, err)
		}
	}
}

// TestBusinessDayLocationIsFixedOffset 验证业务日时区解析成**固定偏移**。
//
// 用 FixedZone 而不是 time.LoadLocation：偏移量没有夏令时规则，也不依赖
// 容器里有没有 tzdata——scratch 镜像里 LoadLocation 会失败，
// 而业务日切日不该因为基础镜像瘦身就换个口径（宪法 14 条）。
func TestBusinessDayLocationIsFixedOffset(t *testing.T) {
	a := meteredAccount()
	loc, err := a.BusinessDayLocation()
	if err != nil {
		t.Fatalf("解析业务日时区失败: %v", err)
	}
	// 2026-07-01 在有夏令时的地区会偏移 +1h；固定偏移必须恒为 +08:00
	for _, when := range []time.Time{
		time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	} {
		_, offset := when.In(loc).Zone()
		if offset != 8*3600 {
			t.Fatalf("%s 的偏移是 %d 秒，期望 28800（CST 固定 +08:00 无夏令时）", when, offset)
		}
	}

	a.BusinessDayTZ = "-05:30"
	loc, err = a.BusinessDayLocation()
	if err != nil {
		t.Fatalf("解析负偏移失败: %v", err)
	}
	if _, offset := time.Now().In(loc).Zone(); offset != -(5*3600 + 30*60) {
		t.Fatalf("-05:30 解析成 %d 秒", offset)
	}
}

// TestRechargeCostRateIsDerivedNotStored 验证 §3.4 的展示投影。
//
// 它每次按当前倍率现算，所以两个值不可能漂移；成本计算恒用整数除法。
func TestRechargeCostRateIsDerivedNotStored(t *testing.T) {
	a := meteredAccount()
	if got := a.RechargeCostRate(); got != "0.666667" {
		t.Fatalf("1/1.5 的展示投影 = %q, want \"0.666667\"", got)
	}

	// 未配倍率时返回空串——不编一个 "1.000000" 冒充「没打折」（宪法 12 条）
	a.AccessMethod = finance.AccessSubscriptionAccount
	a.RechargeRatio = money.Ratio{}
	if got := a.RechargeCostRate(); got != "" {
		t.Fatalf("未配倍率的展示投影应为空串，got %q", got)
	}
}

func TestTokenMappingValidate(t *testing.T) {
	valid := finance.TokenMapping{
		UpstreamAccountID: uuid.New(),
		UpstreamTokenID:   "tok-1",
		OwnAccountID:      "258",
		CredentialRef:     testCredentialRef,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法映射应通过: %v", err)
	}

	// credential_ref 可选（newapi 侧走账号级会话，没有每令牌凭据）
	noCred := valid
	noCred.CredentialRef = ""
	if err := noCred.Validate(); err != nil {
		t.Fatalf("不带 credential_ref 的映射应通过: %v", err)
	}

	for name, mutate := range map[string]func(*finance.TokenMapping){
		"缺账号":      func(m *finance.TokenMapping) { m.UpstreamAccountID = uuid.Nil },
		"缺上游令牌":    func(m *finance.TokenMapping) { m.UpstreamTokenID = "  " },
		"缺自营账号":    func(m *finance.TokenMapping) { m.OwnAccountID = "" },
		"凭据不是 Ref": func(m *finance.TokenMapping) { m.CredentialRef = "plaintext" },
	} {
		m := valid
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Fatalf("%s 的映射必须被拒", name)
		}
	}
}

func TestParsersRejectUnknownValues(t *testing.T) {
	if _, err := finance.ParseSystemType("Sub2API"); err == nil {
		t.Fatal("system_type 大小写必须精确")
	}
	if _, err := finance.ParseAccessMethod("upstream-key"); err == nil {
		t.Fatal("access_method 必须精确匹配下划线写法")
	}
	if _, err := finance.ParseStatus("ACTIVE"); err == nil {
		t.Fatal("status 大小写必须精确")
	}
}

// TestOnlyUpstreamKeyIsMetered 锁住影子对比的范围（§9）：
// 只有 upstream_key 进 SoloAI 逐笔对比，订阅型与 official_api 都不进。
func TestOnlyUpstreamKeyIsMetered(t *testing.T) {
	if !finance.AccessUpstreamKey.IsMetered() {
		t.Fatal("upstream_key 必须是计量型")
	}
	for _, m := range []finance.AccessMethod{
		finance.AccessOfficialAPI, finance.AccessSubscriptionAccount,
	} {
		if m.IsMetered() {
			t.Fatalf("%s 不该被判成计量型——它不进 SoloAI 影子对比", m)
		}
	}
}

// TestRejectionsNeverEchoSecrets 锁住一条容易被无意破坏的纪律。
//
// 领域错误会一路走到 Action 的 Message、进 HTTP 响应、进日志
// （httpapi.safeMessage 把 action.Error 的 Message 原样回给调用方）。
// 而这两条校验拦下的恰恰是「有人把明文粘进了这个字段」——
// 回显被拒的值等于把那次手滑变成一次真正的泄漏（宪法 7 条）。
func TestRejectionsNeverEchoSecrets(t *testing.T) {
	// 用一个明显是占位符的串：仓库里不该出现任何长得像凭据的东西，
	// 而这条断言只需要一个「能在字符串里被找到」的独特值。
	const pasted = "placeholder-pasted-plaintext"

	a := meteredAccount()
	a.CredentialRef = pasted
	err := a.Validate()
	if err == nil {
		t.Fatal("明文凭据必须被拒")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Fatalf("凭据校验的错误回显了被拒的值: %v", err)
	}

	// base_url 里的 user:pass@ 段同样不能被回显
	a = meteredAccount()
	a.BaseURL = "https://admin:" + pasted + "@api.example.test"
	err = a.Validate()
	if err == nil {
		t.Fatal("含凭证段的 base_url 必须被拒")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Fatalf("base_url 校验的错误回显了密码: %v", err)
	}

	m := finance.TokenMapping{
		UpstreamAccountID: uuid.New(),
		UpstreamTokenID:   "tok-1",
		OwnAccountID:      "258",
		CredentialRef:     pasted,
	}
	err = m.Validate()
	if err == nil {
		t.Fatal("映射的明文凭据必须被拒")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Fatalf("映射凭据校验的错误回显了被拒的值: %v", err)
	}
}
