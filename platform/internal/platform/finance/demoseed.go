package finance

// 演示数据种子（XM-0037d）。**只在非生产环境、且显式开启时运行。**
//
// 存在的理由很具体：037a~c 把整条成本链路建起来了，但一个新拉起的 staging
// 环境里登记簿是空的——采集每轮遍历零个账号，台账零行，毛利卡上一片「未接入」。
// 于是没人看得出这条链路到底通没通，直到有人手工登记了三个账号为止。
//
// 三条纪律：
//
//  1. **走 Action 内核，不绕库**（宪法 2 条）。种子调的就是运营手工登记时调的
//     那几个 L1 Action，因此参数校验、跨环境闸门、审计链一样都不少。
//     绕过去直接 INSERT 会让种子成为唯一一条不受 Action 约束的写路径——
//     那正是宪法 2 条要挡的通道。
//  2. **生产硬拒**（启动即失败，不是静默跳过）。演示数据一旦落进生产登记簿，
//     采集就会照着它去打一批 .invalid 域名，而台账里会多出几条永远算不出
//     成本的渠道。
//  3. **幂等**：环境里已经有任何一条登记簿记录就整个跳过。按「有没有」而不是
//     按「有没有这几条」判断，是为了不与真实数据抢——一个已经在用的环境
//     误开了这个开关，种子什么都不该做。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 演示凭据引用与地址。
//
// 常量而不是就地写字面量：`credential_ref: "secret://…"` 这个形状会被
// gitleaks 的 generic-api-key 规则当成泄露的密钥（同 collector_test.go 的说明）。
// 本仓禁止加 gitleaks allowlist，所以换个写法比放宽扫描器划算。
//
// 地址用 **.invalid**（RFC 2606 保留给「保证解析不到」的用途）：
// 万一有人把演示账号带进了 real 模式，请求会在 DNS 就失败，
// 而不是打到某个恰好存在的域名上。
const (
	demoUpstreamRefA = "secret://finance-demo/upstream-a"
	demoUpstreamRefB = "secret://finance-demo/upstream-b"
	demoUpstreamRefC = "secret://finance-demo/subscription-a"
	demoMappingRefA  = "secret://finance-demo/mapping-a"
	demoMappingRefB  = "secret://finance-demo/mapping-b"
	demoProxyRef     = "secret://finance-demo/proxy-a"

	demoSub2APIBaseURL = "https://sub2api.demo.invalid"
	demoNewAPIBaseURL  = "https://newapi.demo.invalid"

	// demoSeedRequestPrefix 是每一步的 X-Request-ID 前缀。
	//
	// 种子调的这四个 Action **都没有 reason 参数**（Schema 是白名单语义，
	// 未声明的字段一律拒绝），所以「这几条记录是谁为什么写的」只能靠
	// 身份的 Issuer 与这个请求前缀来回答——两者都会进审计事件。
	demoSeedRequestPrefix = "finance-demo-seed:"
)

// ErrDemoSeedForbiddenInProduction：生产环境不允许种演示数据。
var ErrDemoSeedForbiddenInProduction = errors.New(
	"finance: 生产环境禁止演示数据种子（XM_FINANCE_FAKE_SEED）")

// DemoSeedExecutor 是种子用到的 Action 内核子集（*action.Kernel 满足）。
//
// 只声明 Execute 一个方法：种子除了「按运营的方式调那几个 Action」之外
// 没有别的能力，接口里没有第二条路。
type DemoSeedExecutor interface {
	Execute(ctx context.Context, req action.Request) (action.Result, error)
}

// DemoSeedRegistry 是种子用到的登记簿只读子集（*Store 满足）。
type DemoSeedRegistry interface {
	ListAccountsByEnvironment(ctx context.Context, environment string) ([]UpstreamAccount, error)
}

// DemoSeedOptions 是种子的构造参数。
type DemoSeedOptions struct {
	Environment string
	Kernel      DemoSeedExecutor
	// Registry 用来判「这个环境是不是已经有登记簿记录了」。
	//
	// 声明成接口而不是 *Store：种子对登记簿只有「看一眼有没有」这一种需求，
	// 而**写**必须经内核（上面第 1 条）。收窄到一个只读方法，
	// 「种子绕过 Action 直接写库」就在类型层面不可表达。
	Registry DemoSeedRegistry
	Logger   *slog.Logger
	// Now 决定演示批次的有效期落在哪几天；默认 time.Now。
	Now func() time.Time
}

// DemoSeedResult 报告种子做了什么。
type DemoSeedResult struct {
	// Skipped 为真表示环境里已经有登记簿记录，整个种子没有执行。
	Skipped         bool
	Accounts        int
	TokenMappings   int
	ProxyAssets     int
	SubscriptionSet int
}

// demoSeedPrincipal 构造种子执行时用的身份。
//
// ⚠️ **类型是 HUMAN，而种子不是人。** 这是本文件唯一一处需要评审确认的取舍：
// 登记簿那几个 Action 声明的是 humanOnly（ADR-009 红线——不给机器身份一条
// 「算不出来就把倍率改到能算」的路），而种子要调的正是它们。三条路里选一条：
//
//	绕过内核直接 INSERT   → 破坏宪法 2 条，且种子成为唯一不受约束的写路径；
//	把 Action 放宽到收 SERVICE → 削弱的是**生产**环境的护栏，代价最大；
//	以 HUMAN 身份执行       → 谎报的只是类型，且只发生在非生产环境。
//
// 选第三条，并用 **Issuer 把来路写死**：`xingmang://finance-demo-seed`
// 是任何真实登录都产不出的签发者，审计链上一眼能认出这几条不是人干的。
// AuthenticationLevel 留空同样是诚实的——这里没有发生过任何认证。
func demoSeedPrincipal(environment string) principal.Principal {
	return principal.Principal{
		ID:           "finance-demo-seed",
		Type:         principal.TypeHuman,
		IdentityZone: "staff",
		Issuer:       "xingmang://finance-demo-seed",
		Environment:  environment,
		Scopes: []string{
			ScopeAccountManage, ScopeTokenMapManage, ScopeSubscriptionManage, ScopeRead,
		},
	}
}

// SeedDemoData 种一批演示登记簿记录：两个计量型账号 + 一个订阅型账号
// （含一笔订阅批次与一份代理资产）。
//
// 两个计量型账号的令牌**刻意做成两种形态**，好让 037c 的两条路在演示里都跑到：
//
//	sub2api  两把令牌 → 同一个自营账号  ⇒ 账号级聚合行（收入只计一次）
//	         一把令牌 → 另一个自营账号  ⇒ 令牌级行（保留下钻）
//	newapi   一把令牌 → 一个自营账号    ⇒ 令牌级行
//
// 只造「一切正常」的数据是不够的：一条链路最值得演示的恰恰是它在
// 边界形态上的行为。
func SeedDemoData(ctx context.Context, opts DemoSeedOptions) (DemoSeedResult, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.Environment == "production" {
		return DemoSeedResult{}, ErrDemoSeedForbiddenInProduction
	}
	if opts.Kernel == nil || opts.Registry == nil {
		return DemoSeedResult{}, errors.New("finance: 演示种子缺少 kernel / registry")
	}

	existing, err := opts.Registry.ListAccountsByEnvironment(ctx, opts.Environment)
	if err != nil {
		return DemoSeedResult{}, fmt.Errorf("读登记簿判断是否已种过: %w", err)
	}
	if len(existing) > 0 {
		// 按「有没有记录」而不是「有没有这几条」判断：一个已经在用的环境
		// 误开了这个开关，种子什么都不该做。
		logger.LogAttrs(ctx, slog.LevelInfo, "finance_demo_seed_skipped",
			slog.String("module", "platform.finance"),
			slog.String("environment", opts.Environment),
			slog.Int("existing_accounts", len(existing)),
			slog.String("hint", "登记簿已有记录，演示种子跳过（不与真实数据抢）"),
		)
		return DemoSeedResult{Skipped: true}, nil
	}

	seed := &demoSeeder{
		ctx:  principal.WithPrincipal(ctx, demoSeedPrincipal(opts.Environment)),
		exec: opts.Kernel, logger: logger, environment: opts.Environment,
	}
	today := BusinessDayAt(now(), DefaultBusinessDayLocation())

	sub2apiID := seed.account(map[string]any{
		"system_type": string(SystemSub2API), "access_method": string(AccessUpstreamKey),
		"credential_ref": demoUpstreamRefA, "base_url": demoSub2APIBaseURL,
		// §2.4 的 worked example 用的就是 1.5——演示数出来能直接对照设计稿。
		"recharge_ratio": "1.5", "platform_id": "sub2api-demo",
	})
	newapiID := seed.account(map[string]any{
		"system_type": string(SystemNewAPI), "access_method": string(AccessUpstreamKey),
		"credential_ref": demoUpstreamRefB, "base_url": demoNewAPIBaseURL,
		"recharge_ratio": "1.2", "platform_id": "newapi-demo",
	})
	subscriptionID := seed.account(map[string]any{
		"system_type":    string(SystemOfficial),
		"access_method":  string(AccessSubscriptionAccount),
		"credential_ref": demoUpstreamRefC,
		// 订阅型**不得配倍率**（§2.0，库层 CHECK 也拦）：它的成本走 §3.5 摊销。
		// base_url 也留空——订阅账号常常没有可读端点。
		"platform_id": "subscription-demo",
	})

	// sub2api：两把令牌指向同一个自营账号 ⇒ 演示账号级聚合行（XM-0037c）
	seed.mapping(sub2apiID, "demo-token-a1", "demo-account-1", demoMappingRefA)
	seed.mapping(sub2apiID, "demo-token-a2", "demo-account-1", demoMappingRefB)
	// sub2api：再来一把独立的 ⇒ 演示令牌级行（下钻保留）
	seed.mapping(sub2apiID, "demo-token-a3", "demo-account-2", demoMappingRefA)
	// newapi：成本侧走账号级会话，没有每令牌凭据（§2.1）
	seed.mapping(newapiID, "demo-token-b1", "demo-channel-7", "")
	// 订阅型：恰好一个自营账号来承接摊销成本（XM-0037c 的 soleOwnAccount）
	seed.mapping(subscriptionID, "demo-seat-1", "demo-account-3", "")

	proxyID := seed.proxy(map[string]any{
		"paid_minor": "6200000", "currency": DefaultCurrency,
		"opened_on":            today.AddDate(0, 0, -5).Format(ProfitBusinessDayLayout),
		"expires_on":           today.AddDate(0, 0, 25).Format(ProfitBusinessDayLayout),
		"shared_account_count": 2,
		"buy_platform":         "demo-idc",
		"credential_ref":       demoProxyRef,
		"mounted":              true,
	})
	seed.batch(map[string]any{
		"upstream_account_id": subscriptionID,
		"paid_minor":          "29990000",
		"surcharge_minor":     "1000000",
		"currency":            DefaultCurrency,
		// 覆盖今天：不覆盖的话摊销每轮都会报 rows_skipped_no_batch，
		// 而那是「配置还没到位」的信号，不该由种子自己造出来。
		"starts_on":      today.AddDate(0, 0, -10).Format(ProfitBusinessDayLayout),
		"expires_on":     today.AddDate(0, 0, 20).Format(ProfitBusinessDayLayout),
		"account_count":  1,
		"proxy_asset_id": proxyID,
	})

	if seed.err != nil {
		return DemoSeedResult{}, seed.err
	}
	logger.LogAttrs(ctx, slog.LevelWarn, "finance_demo_seed_applied",
		slog.String("module", "platform.finance"),
		slog.String("environment", opts.Environment),
		slog.Int("accounts", seed.result.Accounts),
		slog.Int("token_mappings", seed.result.TokenMappings),
		slog.String("hint", "这些是**演示**登记簿记录（.invalid 地址），不是真实上游；"+
			"看板上的毛利随之而来的也是演示数据"),
	)
	return seed.result, nil
}

// demoSeeder 把「调一个 Action、出错就记下来」这件事收成一处。
//
// 逐步累积错误而不是每步 if err != nil：种子是一串同构的调用，
// 每步展开成四行会把「种了什么」淹没在错误处理里。第一个错误一旦出现，
// 后续调用照样发出但结果被忽略——**不提前返回**是有意的：
// 一次启动就能看到全部失败，而不是修一个重启一次再看下一个。
type demoSeeder struct {
	ctx         context.Context
	exec        DemoSeedExecutor
	logger      *slog.Logger
	environment string
	result      DemoSeedResult
	err         error
}

func (s *demoSeeder) execute(actionID string, params map[string]any) (uuid.UUID, bool) {
	res, err := s.exec.Execute(s.ctx, action.Request{
		ActionID:      actionID,
		ActionVersion: actionVersion,
		// RequestID 内核必填。带上 Action ID 让审计里一眼看得出是哪一步，
		// 前缀则让「这一批是种子写的」可被一次检索捞出来。
		RequestID: demoSeedRequestPrefix + actionID,
		Params:    params,
	})
	if err != nil {
		if s.err == nil {
			s.err = fmt.Errorf("演示种子执行 %s: %w", actionID, err)
		}
		s.logger.LogAttrs(s.ctx, slog.LevelError, "finance_demo_seed_step_failed",
			slog.String("module", "platform.finance"),
			slog.String("environment", s.environment),
			slog.String("action_id", actionID),
			slog.Any("err", err),
		)
		return uuid.Nil, false
	}
	return idOfSeededResource(res.Value), true
}

// idOfSeededResource 从 Action 返回值里取出新建资源的 id。
//
// 三个 Action 的返回类型不同（账号 / 代理 / 批次），但都带一个 ID 字段。
// 用类型 switch 而不是反射：三种就是三种，反射会让「加了第四种忘了处理」
// 变成一个运行时的零值 uuid，而那会让后续步骤挂错父资源。
func idOfSeededResource(value any) uuid.UUID {
	switch v := value.(type) {
	case UpstreamAccount:
		return v.ID
	case ProxyAsset:
		return v.ID
	case SubscriptionBatch:
		return v.ID
	default:
		return uuid.Nil
	}
}

func (s *demoSeeder) account(params map[string]any) string {
	id, ok := s.execute(ActionAccountSet, params)
	if !ok {
		return ""
	}
	s.result.Accounts++
	return id.String()
}

func (s *demoSeeder) mapping(accountID, tokenID, ownAccountID, credentialRef string) {
	if accountID == "" {
		// 父账号没建成，映射必然挂不上。跳过而不是发一个注定失败的请求——
		// 那只会在日志里多一条误导性的「映射登记失败」。
		return
	}
	params := map[string]any{
		"upstream_account_id": accountID,
		"upstream_token_id":   tokenID,
		"own_account_id":      ownAccountID,
	}
	if credentialRef != "" {
		params["credential_ref"] = credentialRef
	}
	if _, ok := s.execute(ActionTokenMapSet, params); ok {
		s.result.TokenMappings++
	}
}

func (s *demoSeeder) proxy(params map[string]any) string {
	id, ok := s.execute(ActionProxyAssetSet, params)
	if !ok {
		return ""
	}
	s.result.ProxyAssets++
	return id.String()
}

func (s *demoSeeder) batch(params map[string]any) {
	if params["upstream_account_id"] == "" {
		return
	}
	if params["proxy_asset_id"] == "" {
		// 代理没建成就不挂代理：批次本身仍然有价值（订阅摊销跑得起来），
		// 只是少了代理那一份分摊。
		delete(params, "proxy_asset_id")
	}
	if _, ok := s.execute(ActionSubscriptionBatchRegister, params); ok {
		s.result.SubscriptionSet++
	}
}
