package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// cardsMode 决定卡片功能用哪个客户端实现。
type cardsMode string

const (
	// cardsModeOff 完全不挂载卡片端点、不注册卡片 Action。
	//
	// **默认就是 off**，与用户管理选 fake 相反。理由是代价不同：
	// 这些 Action 会花真钱。一个默认挂上开卡按钮的环境，
	// 迟早有人在以为是演示的地方点下去。
	cardsModeOff cardsMode = "off"
	// cardsModeFake 用内存替身，供开发与演示。写操作不触及任何真实资金。
	cardsModeFake cardsMode = "fake"
	// cardsModeReal 连真实的 Infini API。
	cardsModeReal cardsMode = "real"
)

func parseCardsMode(s string) (cardsMode, error) {
	switch mode := cardsMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return cardsModeOff, nil
	case cardsModeOff, cardsModeFake, cardsModeReal:
		return mode, nil
	default:
		return "", fmt.Errorf("XM_CARDS_MODE %q: 只接受 off / fake / real", s)
	}
}

// accountIDPattern 限制账号 id 的字符集。
//
// 账号 id 要拼进环境变量名（XM_CARDS_<ID>_KEY_ID_REF），所以只能是
// 大写字母、数字与下划线——一个带连字符的 id 会拼出取不到值的变量名，
// 而那种失败长得像「凭据没配」，排查方向完全错。
var accountIDPattern = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)

// cardsAccountConfig 是单个账号的配置。
//
// **凭据引用不在这里配**：它由账号 id 推出（cards.CredentialRefsFor），
// 值由运营在管理端「密钥引用」页填写与轮换，写进去的就是 SecretProvider
// 读的那个文件。少两个环境变量就是少两处可以配错的地方。
type cardsAccountConfig struct {
	ID string
	// 金额上限按账号各配一份：两个账号的资金是分开的。
	PerOperationLimit string
	PerDayLimit       string
	// 提现额度**不在这里**：它存在库里，由管理后台的
	// cards.withdraw.limit.set Action 调整（产品负责人 2026-09-05 决定：
	// 要登服务器改文件再重启才能动的数字，实际上没人会去动）。
	// BaseURL 覆盖进程级的 XM_CARDS_BASE_URL。
	//
	// 存在的唯一理由是**沙箱**：沙箱端点是
	// https://openapi-sandbox.infini.money，与生产不同，而沙箱账号需要与
	// 生产账号并存才能在同一套部署里联调回调（回调要公网可达，本地跑不了）。
	//
	// 留空即沿用进程级值。
	BaseURL string
}

// cardsConfig 是卡片功能的进程级配置。
type cardsConfig struct {
	Mode cardsMode
	// BaseURL 所有账号共用：同一个供应商，同一个端点。
	BaseURL  string
	Accounts []cardsAccountConfig
	// SyncInterval 供读端点的新鲜度判定，与 worker 的同步周期保持一致。
	SyncInterval time.Duration
}

// loadCardsConfig 从环境变量读取配置。
//
// 账号清单来自 XM_CARDS_ACCOUNTS（逗号分隔），每个账号的凭据引用与金额
// 上限用 XM_CARDS_<ID>_* 取。**没有单账号的隐式回落**：一个「只配了一对
// 凭据就默默当成唯一账号」的行为，会在加第二个账号时把钱花到错的地方。
func loadCardsConfig(getenv func(string) string) (cardsConfig, error) {
	mode, err := parseCardsMode(getenv("XM_CARDS_MODE"))
	if err != nil {
		return cardsConfig{}, err
	}

	cfg := cardsConfig{
		Mode:         mode,
		BaseURL:      strings.TrimSpace(getenv("XM_CARDS_BASE_URL")),
		SyncInterval: 5 * time.Minute,
	}
	if mode == cardsModeOff {
		return cfg, nil
	}

	ids, err := parseAccountIDs(getenv("XM_CARDS_ACCOUNTS"))
	if err != nil {
		return cardsConfig{}, err
	}
	if len(ids) == 0 {
		return cardsConfig{}, fmt.Errorf(
			"XM_CARDS_MODE=%s 但 XM_CARDS_ACCOUNTS 为空——账号清单必须显式声明", mode)
	}

	for _, id := range ids {
		acct := cardsAccountConfig{
			ID:                id,
			PerOperationLimit: strings.TrimSpace(getenv("XM_CARDS_" + id + "_LIMIT_PER_OPERATION")),
			PerDayLimit:       strings.TrimSpace(getenv("XM_CARDS_" + id + "_LIMIT_PER_DAY")),
			BaseURL:           strings.TrimSpace(getenv("XM_CARDS_" + id + "_BASE_URL")),
		}

		// 金额上限在任何模式下都必填：fake 模式也要跑限额分支，
		// 否则「上限没配」这个 fail-closed 行为在演示环境永远测不到。
		var missing []string
		if acct.PerOperationLimit == "" || acct.PerDayLimit == "" {
			missing = append(missing, "XM_CARDS_"+id+"_LIMIT_PER_OPERATION 与 _LIMIT_PER_DAY")
		}
		if len(missing) > 0 {
			return cardsConfig{}, fmt.Errorf(
				"账号 %s 缺少 %s——花钱的通道不接受默认值", id, strings.Join(missing, "、"))
		}

		cfg.Accounts = append(cfg.Accounts, acct)
	}

	if mode == cardsModeReal {
		// 每个账号都要有可用的端点：进程级的，或它自己覆盖的。
		// 只校验进程级会漏掉「全靠覆盖、进程级留空」这种配法。
		for _, acct := range cfg.Accounts {
			if acct.BaseURL == "" && cfg.BaseURL == "" {
				return cardsConfig{}, fmt.Errorf(
					"XM_CARDS_MODE=real 但账号 %s 没有端点：配 XM_CARDS_BASE_URL 或 XM_CARDS_%s_BASE_URL",
					acct.ID, acct.ID)
			}
		}
	}
	return cfg, nil
}

// parseAccountIDs 解析并校验账号清单。
func parseAccountIDs(raw string) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.ToUpper(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		if !accountIDPattern.MatchString(id) {
			return nil, fmt.Errorf("账号 id %q 非法：只接受大写字母、数字与下划线（它要拼进环境变量名）", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("账号 id %q 重复", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// buildCards 组装卡片功能。mode=off 时返回 (nil, nil, nil)，
// 调用方据此既不注册 Action 也不挂端点。
func buildCards(
	ctx context.Context,
	cfg cardsConfig,
	pool *pgxpool.Pool,
	secretProvider secrets.SecretProvider,
	environment string,
) (*cards.Service, *cards.PgStore, []cards.Account, error) {
	if cfg.Mode == cardsModeOff {
		return nil, nil, nil, nil
	}
	if pool == nil {
		return nil, nil, nil, fmt.Errorf("卡片功能需要数据库连接")
	}

	store := cards.NewPgStore(pool, environment, time.Now)

	accounts, err := buildCardAccounts(ctx, cfg, secretProvider)
	if err != nil {
		return nil, nil, nil, err
	}
	// 账号一并返回：回调处理器要用同一批已签名的客户端做定向刷新，
	// 再造一遍等于把同一份凭据解析两次、也多一处可以配歪的地方。
	return cards.NewService(accounts, store, time.Now), store, accounts, nil
}

// buildCardAccounts 把配置翻成运行时账号。
func buildCardAccounts(
	ctx context.Context,
	cfg cardsConfig,
	provider secrets.SecretProvider,
) ([]cards.Account, error) {
	out := make([]cards.Account, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		limits := cards.Limits{PerOperation: a.PerOperationLimit, PerDay: a.PerDayLimit}

		var client infini.CardClient
		switch cfg.Mode {
		case cardsModeFake:
			client = infini.NewFake()
		case cardsModeReal:
			// 账号自己的端点优先：沙箱账号与生产账号可以并存。
			baseURL := a.BaseURL
			if baseURL == "" {
				baseURL = cfg.BaseURL
			}
			realClient, err := newInfiniClient(ctx, baseURL, a, provider)
			if err != nil {
				return nil, fmt.Errorf("账号 %s: %w", a.ID, err)
			}
			client = realClient
		}

		out = append(out, cards.Account{ID: a.ID, Client: client, Limits: limits})
	}
	return out, nil
}

// newInfiniClient 组装真实客户端。
//
// 凭据**不在这里解析**：只把 CredentialRef 传下去，由客户端每次调用时经
// SecretProvider 解析（ADR-014 的轮换要求——密钥换了不必重启进程）。
// 启动期只校验引用格式，格式错要在启动就炸，而不是等到有人点开卡。
func newInfiniClient(
	_ context.Context,
	baseURL string,
	acct cardsAccountConfig,
	provider secrets.SecretProvider,
) (*infini.Client, error) {
	if provider == nil {
		return nil, fmt.Errorf("real 模式需要 SecretProvider")
	}

	// 引用由账号 id 推出，值在管理端填。启动期不校验「值存不存在」——
	// 那是运营还没填的正常状态，不该拦住进程启动；真去调用时会报 auth，
	// 而密钥引用页上那条会显示成「未配置」。
	keyIDRaw, secretRaw := cards.CredentialRefsFor(acct.ID)
	keyIDRef, err := secrets.ParseCredentialRef(keyIDRaw)
	if err != nil {
		return nil, fmt.Errorf("keyId 引用 %q: %w", keyIDRaw, err)
	}
	secretRef, err := secrets.ParseCredentialRef(secretRaw)
	if err != nil {
		return nil, fmt.Errorf("secret 引用 %q: %w", secretRaw, err)
	}

	host, err := hostFromBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	// allowlist 只放这一个主机：写通道的 fail-closed 语义要求它非空，
	// 而放宽到多个主机没有任何业务理由。
	return infini.NewClient(baseURL, provider, keyIDRef, secretRef, []string{host}), nil
}

// hostFromBaseURL 从 base URL 取主机名，用作写通道的 allowlist。
func hostFromBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("XM_CARDS_BASE_URL %q 无法解析: %w", raw, err)
	}
	if u.Scheme != "https" {
		// 花钱的请求带着签名，必须走 TLS。
		return "", fmt.Errorf("XM_CARDS_BASE_URL 必须是 https，当前 %q", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("XM_CARDS_BASE_URL %q 缺少主机名", raw)
	}
	return u.Hostname(), nil
}

// registerCardActions 注册卡片相关的 Action。svc 为 nil（mode=off）时不注册。
func registerCardActions(reg *action.Registry, svc *cards.Service) error {
	if svc == nil {
		return nil
	}
	return cards.RegisterActions(reg, svc)
}

// registerWithdrawActions 注册提现的两个 Action。
//
// 与卡片 Action 分开注册，但**不按「有没有配额度」决定注不注册**：
// 没配额度的账号在领域层被 ErrLimitsUnconfigured 拒掉，那是一条会
// 报错、能查、进审计的路径；按配置决定注册与否则会让页面上的按钮
// 凭空消失，而「按钮没了」这种症状最难查。
func registerWithdrawActions(
	reg *action.Registry, svc *cards.WithdrawService,
	store cards.WithdrawAddressStore, limits cards.WithdrawLimitStore,
	accounts []cards.Account,
) error {
	if svc == nil {
		return nil
	}
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	return cards.RegisterWithdrawActions(reg, svc, store, limits, ids)
}

// cardQuerierOrNil 把「没启用」翻译成 nil 接口。
//
// 不能直接把 *cards.PgStore 赋给接口字段：一个装着 nil 指针的非 nil 接口
// 会让路由以为端点该挂载，然后每次调用都空指针崩溃。这是 Go 里最容易
// 写错的一处，与 platformUsersOrNil 同一条纪律。
func cardQuerierOrNil(store *cards.PgStore) httpapi.CardQuerier {
	if store == nil {
		return nil
	}
	return store
}

// cardAccountIDs 取出已配置的账号，供读端点回给管理端填下拉。
// svc 为 nil（mode=off）时返回 nil——端点那时也不挂载。
func cardAccountIDs(svc *cards.Service) []string {
	if svc == nil {
		return nil
	}
	return svc.AccountIDs()
}

// cardExpectedCredentials 把配置的账号翻成密钥引用页要显示的条目。
//
// 运营在那一页填值与轮换，写进去的就是 SecretProvider 读的文件——
// 所以「加一个账号」只需要改 XM_CARDS_ACCOUNTS，凭据在界面上补，不用改代码、
// 不用登服务器写文件。
// cardNotifyWebhookRef 是卡片事件推送的目标地址（企业微信群机器人 Webhook）。
//
// 与 alerts 那条分开：卡片推送里会出现 3DS 验证码，而告警群通常人更多。
// 分成两条引用，运营可以把卡片推送指到一个只有自己的群；想用同一个群时
// 把同一个地址填两遍即可。
const cardNotifyWebhookRef = "secret://cards/notify-webhook"

func cardExpectedCredentials(cfg cardsConfig) []credentials.ExpectedRef {
	out := make([]credentials.ExpectedRef, 0, len(cfg.Accounts)*3)
	for _, a := range cfg.Accounts {
		keyIDRef, secretRef := cards.CredentialRefsFor(a.ID)
		out = append(out,
			credentials.ExpectedRef{
				Ref: keyIDRef, Platform: "infini",
				Purpose: "Infini 账号 " + a.ID + " 的 API keyId（公开半边，进 Authorization 头）",
			},
			credentials.ExpectedRef{
				Ref: secretRef, Platform: "infini",
				Purpose: "Infini 账号 " + a.ID + " 的 API 私钥（签名用，绝不回前端）",
			},
			// 回调密钥与 API 密钥各自独立轮换（上游就是这么划分的）。
			// 没配它的账号，回调端点对该账号一律拒绝——不配 = 不启用。
			credentials.ExpectedRef{
				Ref: cards.WebhookSecretRefFor(a.ID), Platform: "infini",
				Purpose: "Infini 账号 " + a.ID + " 的回调验签密钥（Webhook 设置页生成，与 API 密钥不同）",
			},
		)
	}
	// 推送地址与账号无关，只登记一条。没配就不推送，不影响任何其他功能。
	out = append(out, credentials.ExpectedRef{
		Ref: cardNotifyWebhookRef, Platform: "infini",
		Purpose: "卡片事件推送的企业微信群机器人 Webhook（含 3DS 验证码，建议指向只有自己的群）",
	})
	return out
}

// cardBalanceReaderOrNil 把「卡片功能没开」如实变成接口的 nil。
//
// 同 cardQuerierOrNil：直接把 nil 指针赋给接口字段会得到一个非 nil 的
// 接口值，于是路由照挂、每次请求都在 nil 上崩。
func cardBalanceReaderOrNil(svc *cards.Service) httpapi.CardBalanceReader {
	if svc == nil {
		return nil
	}
	return svc
}

// cardWithdrawQuerierOrNil 把「没启用」翻译成 nil 接口。
//
// 同 cardQuerierOrNil 那条纪律：一个装着 nil 指针的非 nil 接口会让路由
// 以为端点该挂载，然后每次调用都空指针崩溃。
func cardWithdrawQuerierOrNil(store *cards.PgStore) httpapi.WithdrawQuerier {
	if store == nil {
		return nil
	}
	return store
}
