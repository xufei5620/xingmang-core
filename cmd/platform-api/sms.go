package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xufei5620/xingmang-platform/connectors/herosms"
	"github.com/xufei5620/xingmang-platform/connectors/sms62"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

// smsMode 是接码功能的开关。与 XM_CARDS_MODE 同一套取值。
type smsMode string

const (
	smsModeOff  smsMode = "off"
	smsModeFake smsMode = "fake"
	smsModeReal smsMode = "real"
)

func parseSMSMode(raw string) (smsMode, error) {
	switch v := smsMode(strings.ToLower(strings.TrimSpace(raw))); v {
	case "", smsModeOff:
		return smsModeOff, nil
	case smsModeFake, smsModeReal:
		return v, nil
	default:
		// 逐字列出合法值：上线当天把 XM_CARDS_MODE 写成 live 让生产下线
		// 四分钟，就是因为错误信息没说清能填什么。
		return "", fmt.Errorf("XM_SMS_MODE=%q 非法，只接受 off / fake / real", raw)
	}
}

type smsConfig struct {
	Mode smsMode
	// Providers 是**显式声明**的供应商清单，没有隐式回落。
	//
	// 同 XM_CARDS_ACCOUNTS 那条：一个「只配了一家就默默当成唯一供应商」的
	// 行为，会在加第二家时把钱花到错的地方。
	Providers []string
}

func loadSMSConfig(getenv func(string) string) (smsConfig, error) {
	mode, err := parseSMSMode(getenv("XM_SMS_MODE"))
	if err != nil {
		return smsConfig{}, err
	}
	cfg := smsConfig{Mode: mode}
	if mode == smsModeOff {
		return cfg, nil
	}

	seen := map[string]bool{}
	for _, part := range strings.Split(getenv("XM_SMS_PROVIDERS"), ",") {
		id := strings.ToLower(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		if err := sms.ValidateProvider(id); err != nil {
			return smsConfig{}, fmt.Errorf("XM_SMS_PROVIDERS 含未知供应商 %q，只接受 sms62 / hero_sms", id)
		}
		if seen[id] {
			return smsConfig{}, fmt.Errorf("XM_SMS_PROVIDERS 里 %q 重复", id)
		}
		seen[id] = true
		cfg.Providers = append(cfg.Providers, id)
	}
	if len(cfg.Providers) == 0 {
		return smsConfig{}, fmt.Errorf("XM_SMS_MODE=%s 但 XM_SMS_PROVIDERS 为空——供应商清单必须显式声明", mode)
	}
	return cfg, nil
}

// smsCredentialRef 从供应商 id 推出它的密钥引用。
//
// **推导而不是配置**，与卡片同一条：少一个环境变量就是少一处可以配错的
// 地方，而配错引用的症状（「密钥没配」）与值填错了长得一模一样。
//
// 管理端「密钥引用」页按同一个 scope 写文件，客户端按同一个 scope 读。
func smsCredentialRef(provider string) string {
	return "secret://" + strings.ToLower(provider) + "/api-key"
}

// buildSMS 组装接码功能。mode=off 时返回 (nil, nil, nil)。
func buildSMS(
	cfg smsConfig,
	pool *pgxpool.Pool,
	secretProvider secrets.SecretProvider,
	environment string,
	notifier sms.Notifier,
) (*sms.Service, *sms.PgStore, error) {
	if cfg.Mode == smsModeOff {
		return nil, nil, nil
	}
	if pool == nil {
		return nil, nil, fmt.Errorf("接码功能需要数据库连接")
	}
	store := sms.NewPgStore(pool, environment, time.Now)

	providers := make([]sms.Provider, 0, len(cfg.Providers))
	for _, id := range cfg.Providers {
		adapter, err := buildSMSAdapter(cfg.Mode, id, secretProvider)
		if err != nil {
			return nil, nil, err
		}
		providers = append(providers, sms.Provider{ID: id, Adapter: adapter})
	}
	return sms.NewService(providers, store, notifier, time.Now), store, nil
}

func buildSMSAdapter(mode smsMode, provider string, secretProvider secrets.SecretProvider) (sms.Adapter, error) {
	if mode == smsModeFake {
		return sms.NewFakeAdapter(provider, time.Now), nil
	}
	if secretProvider == nil {
		return nil, fmt.Errorf("real 模式需要 SecretProvider")
	}
	ref, err := secrets.ParseCredentialRef(smsCredentialRef(provider))
	if err != nil {
		return nil, fmt.Errorf("供应商 %s 的密钥引用 %q 非法: %w", provider, smsCredentialRef(provider), err)
	}

	switch provider {
	case sms.ProviderSMS62:
		host, err := hostOf(sms62.ProductionBaseURL)
		if err != nil {
			return nil, err
		}
		// allowlist 只放这一个主机：写通道的 fail-closed 语义要求它非空，
		// 而放宽到多个主机没有任何业务理由。
		return sms.NewSMS62Adapter(sms62.NewClient(secretProvider, ref, []string{host}), time.Now), nil
	case sms.ProviderHero:
		host, err := hostOf(herosms.ProductionBaseURL)
		if err != nil {
			return nil, err
		}
		return sms.NewHeroAdapter(herosms.NewClient(secretProvider, ref, []string{host}), time.Now), nil
	default:
		return nil, fmt.Errorf("%w: %s", sms.ErrProviderUnknown, provider)
	}
}

func hostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("接码端点 %q 无法解析主机名", raw)
	}
	if u.Scheme != "https" {
		// 带密钥的请求必须走 TLS。
		return "", fmt.Errorf("接码端点必须是 https，当前 %q", raw)
	}
	return u.Hostname(), nil
}

// registerSMSActions 注册四个 Action。svc 为 nil（mode=off）时不注册。
func registerSMSActions(reg *action.Registry, svc *sms.Service) error {
	if svc == nil {
		return nil
	}
	return sms.RegisterActions(reg, svc)
}

// smsQuerierOrNil 把「没启用」翻译成 nil 接口。
//
// 不能直接把 *sms.PgStore 赋给接口字段：一个装着 nil 指针的非 nil 接口会让
// 路由以为端点该挂载，然后每次调用都空指针崩溃。这是 Go 里最容易写错的
// 一处，与 cardQuerierOrNil 同一条纪律。
func smsQuerierOrNil(store *sms.PgStore) httpapi.SMSQuerier {
	if store == nil {
		return nil
	}
	return store
}

func smsCatalogOrNil(svc *sms.Service) httpapi.SMSCatalogReader {
	if svc == nil {
		return nil
	}
	return svc
}

// recoverSMSOperations 在启动时把崩溃遗留的未决操作推进到该去的地方。
//
// **恢复失败不阻止启动**：那会让一次数据库抖动变成整个进程起不来。
// 但也不当作已恢复——付费请求会再次检查，而未决索引仍然挡着重复发送。
func recoverSMSOperations(ctx context.Context, svc *sms.Service) (int, error) {
	if svc == nil {
		return 0, nil
	}
	return svc.Recover(ctx)
}

// smsNotifier 把收到的验证码推到企业微信。
//
// 与卡片那条通道**用不同的凭据引用**：接码的码与卡片 3DS 的码可能要发给
// 不同的人。想发同一个群就把同一个地址填两遍；合成一条则会让任一方想换群
// 时把另一方也弄坏。没配这条引用就不推送，不影响取码本身。
func smsNotifier(provider secrets.SecretProvider) sms.Notifier {
	if provider == nil {
		return nil
	}
	return sms.NewWeComNotifier(func(ctx context.Context) (string, error) {
		ref, err := secrets.ParseCredentialRef(smsNotifyWebhookRef)
		if err != nil {
			return "", err
		}
		value, err := provider.Resolve(ctx, ref, "sms.notify_webhook")
		if err != nil {
			// 没配 = 运营还没填，返回空串让推送器安静跳过。
			return "", nil
		}
		// 必须 Reveal()：String() 恒为 "[REDACTED]"，用它当 URL 会一路失败
		// 而错误看起来像「群机器人地址不对」。
		return strings.TrimSpace(value.Reveal()), nil
	}, nil)
}

// smsNotifyWebhookRef 是接码验证码推送的地址引用。
const smsNotifyWebhookRef = "secret://sms/notify-webhook"
