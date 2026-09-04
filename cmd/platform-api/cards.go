package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
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

// cardsConfig 是卡片功能的进程级配置。
type cardsConfig struct {
	Mode cardsMode
	// BaseURL 是 Infini API 的根地址。不写死在代码里：换环境、换网关
	// 都不该改代码。
	BaseURL string
	// KeyIDRef / SecretRef 是 CredentialRef（secret://<scope>/<name>），
	// 禁止内联明文（ADR-014、宪法条款 7）。
	KeyIDRef  string
	SecretRef string
	// PerOperationLimit / PerDayLimit 是金额上限（十进制文本，
	// 单位与申请金额一致，不做汇率换算）。**未配齐时领域层 fail closed**。
	PerOperationLimit string
	PerDayLimit       string
	// SyncInterval 供读端点的新鲜度判定，与 worker 的同步周期保持一致。
	SyncInterval time.Duration
}

// loadCardsConfig 从环境变量读取配置。
func loadCardsConfig(getenv func(string) string) (cardsConfig, error) {
	mode, err := parseCardsMode(getenv("XM_CARDS_MODE"))
	if err != nil {
		return cardsConfig{}, err
	}

	cfg := cardsConfig{
		Mode:              mode,
		BaseURL:           strings.TrimSpace(getenv("XM_CARDS_BASE_URL")),
		KeyIDRef:          strings.TrimSpace(getenv("XM_CARDS_KEY_ID_REF")),
		SecretRef:         strings.TrimSpace(getenv("XM_CARDS_SECRET_REF")),
		PerOperationLimit: strings.TrimSpace(getenv("XM_CARDS_LIMIT_PER_OPERATION")),
		PerDayLimit:       strings.TrimSpace(getenv("XM_CARDS_LIMIT_PER_DAY")),
		SyncInterval:      5 * time.Minute,
	}

	if mode == cardsModeReal {
		// real 模式下这四样一个都不能少。启动期硬拒绝，
		// 而不是等到有人点开卡时才发现配置不全——那时的错误会长得像
		// 上游故障，排查方向完全错。
		missing := []string{}
		if cfg.BaseURL == "" {
			missing = append(missing, "XM_CARDS_BASE_URL")
		}
		if cfg.KeyIDRef == "" {
			missing = append(missing, "XM_CARDS_KEY_ID_REF")
		}
		if cfg.SecretRef == "" {
			missing = append(missing, "XM_CARDS_SECRET_REF")
		}
		if cfg.PerOperationLimit == "" || cfg.PerDayLimit == "" {
			missing = append(missing, "XM_CARDS_LIMIT_PER_OPERATION 与 XM_CARDS_LIMIT_PER_DAY")
		}
		if len(missing) > 0 {
			return cardsConfig{}, fmt.Errorf(
				"XM_CARDS_MODE=real 但缺少 %s——花钱的通道不接受默认值",
				strings.Join(missing, "、"))
		}
	}

	return cfg, nil
}

// buildCards 组装卡片功能。mode=off 时返回 (nil, nil, nil)，
// 调用方据此既不注册 Action 也不挂端点。
func buildCards(
	ctx context.Context,
	cfg cardsConfig,
	pool *pgxpool.Pool,
	secretProvider secrets.SecretProvider,
	environment string,
) (*cards.Service, *cards.PgStore, error) {
	if cfg.Mode == cardsModeOff {
		return nil, nil, nil
	}
	if pool == nil {
		return nil, nil, fmt.Errorf("卡片功能需要数据库连接")
	}

	store := cards.NewPgStore(pool, environment, time.Now)
	limits := cards.Limits{
		PerOperation: cfg.PerOperationLimit,
		PerDay:       cfg.PerDayLimit,
	}

	var client infini.CardClient
	switch cfg.Mode {
	case cardsModeFake:
		client = infini.NewFake()
	case cardsModeReal:
		real, err := newInfiniClient(ctx, cfg, secretProvider)
		if err != nil {
			return nil, nil, err
		}
		client = real
	}

	return cards.NewService(client, store, limits, time.Now), store, nil
}

// newInfiniClient 组装真实客户端。
//
// 凭据**不在这里解析**：只把 CredentialRef 传下去，由客户端每次调用时经
// SecretProvider 解析（ADR-014 的轮换要求——密钥换了不必重启进程）。
// 启动期只校验引用格式，格式错要在启动就炸，而不是等到有人点开卡。
func newInfiniClient(
	_ context.Context,
	cfg cardsConfig,
	provider secrets.SecretProvider,
) (*infini.Client, error) {
	if provider == nil {
		return nil, fmt.Errorf("卡片功能 real 模式需要 SecretProvider")
	}

	keyIDRef, err := secrets.ParseCredentialRef(cfg.KeyIDRef)
	if err != nil {
		return nil, fmt.Errorf("XM_CARDS_KEY_ID_REF: %w", err)
	}
	secretRef, err := secrets.ParseCredentialRef(cfg.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("XM_CARDS_SECRET_REF: %w", err)
	}

	host, err := hostFromBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	// allowlist 只放这一个主机：写通道的 fail-closed 语义要求它非空，
	// 而放宽到多个主机没有任何业务理由。
	return infini.NewClient(cfg.BaseURL, provider, keyIDRef, secretRef, []string{host}), nil
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
