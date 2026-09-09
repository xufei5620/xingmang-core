package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

// smsMode / parseSMSMode 现在是领域包的别名（XM-SMS2 #7）。
//
// 解析与装配搬进 internal/platform/sms：platform-worker 的巡检任务要建**同一组**
// 供应商，两份解析迟早会分叉成「API 能买号、worker 不认识这家」。
type smsMode = sms.Mode

const (
	smsModeOff  = sms.ModeOff
	smsModeFake = sms.ModeFake
	smsModeReal = sms.ModeReal
)

func parseSMSMode(raw string) (smsMode, error) { return sms.ParseMode(raw) }

// smsConfig 只剩一件事：这个进程打不打真实供应商。
//
// **开哪几家不在这里。**那是运营随时会改的决定（换供应商、某家挂了先停掉），
// 落进环境变量意味着每次改都要改服务器配置再重启，代价与决定的分量不匹配。
// 开关在 sms.provider_status.enabled，管理后台直接点。
//
// Mode 则留在环境变量里，因为它是另一类东西：它决定这个进程会不会花真钱。
// 做成后台可改，意味着一次误操作能让开发环境开始买真号，或者让生产悄悄切到
// 替身而页面上一切正常。
type smsConfig struct {
	Mode smsMode
}

func loadSMSConfig(getenv func(string) string) (smsConfig, error) {
	mode, err := parseSMSMode(getenv("XM_SMS_MODE"))
	if err != nil {
		return smsConfig{}, err
	}
	// 残留的旧变量要喊出来，不能默默忽略：一个还写着 XM_SMS_PROVIDERS=sms62
	// 的配置文件会让人确信 hero_sms 已经关掉了，而它其实由库里的开关说了算。
	if strings.TrimSpace(getenv("XM_SMS_PROVIDERS")) != "" {
		return smsConfig{}, fmt.Errorf("XM_SMS_PROVIDERS 已废弃，请从配置里删掉；供应商启用开关改在管理后台「接码中心 → 供应商」")
	}
	return smsConfig{Mode: mode}, nil
}

// smsCredentialRef 从供应商 id 推出它的密钥引用。
//
// **推导而不是配置**，与卡片同一条：少一个环境变量就是少一处可以配错的
// 地方，而配错引用的症状（「密钥没配」）与值填错了长得一模一样。
//
// 管理端「密钥引用」页按同一个 scope 写文件，客户端按同一个 scope 读。
//
// 下划线要换成连字符：scope 的规则是 ^[a-z0-9][a-z0-9-]{0,63}$，而供应商 id
// 是 hero_sms。直接拼会得到一个解析不过的引用，且只在 XM_SMS_MODE=real 时
// 才炸——症状是「密钥引用非法」，看起来像运营填错了，可这串根本不是人填的。
func smsCredentialRef(provider string) string {
	if spec, ok := sms.Spec(provider); ok {
		return spec.CredentialRef()
	}
	return "secret://" + strings.ReplaceAll(strings.ToLower(provider), "_", "-") + "/api-key"
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
	providers, err := buildSMSProviders(cfg.Mode, secretProvider)
	if err != nil {
		return nil, nil, err
	}
	store := sms.NewPgStore(pool, environment, time.Now)
	return sms.NewService(providers, store, notifier, time.Now), store, nil
}

// buildSMSProviders 走领域包的装配（见 sms.BuildProviders）。
func buildSMSProviders(mode smsMode, secretProvider secrets.SecretProvider) ([]sms.Provider, error) {
	return sms.BuildProviders(mode, secretProvider, time.Now)
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

// smsProviderIDs 是页面上要显示的供应商清单。
//
// 取**已装配的**那份而不是 sms.AllProviders：两者今天相同，但如果哪天
// 某家因为凭据缺失没装配上，页面该看到的是装配后的事实。
func smsProviderIDs(svc *sms.Service) []string {
	if svc == nil {
		return nil
	}
	return svc.Providers()
}

func smsExtrasOrNil(svc *sms.Service) httpapi.SMSExtrasReader {
	if svc == nil {
		return nil
	}
	return svc
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

// smsExpectedCredentials 把接码要用的引用登记到「密钥引用」页。
//
// 运营在那一页填值与轮换，写进去的就是 SecretProvider 读的文件——
// 不用登服务器、不用改代码。缺一条的症状很难查：功能整体是通的，只有那条
// 对应的能力静默失灵（比如推送不发而取码正常），因为「没配」在这套代码里
// 一律是安静跳过。页面上有槽位，运营才知道有这么一样东西要填。
func smsExpectedCredentials(cfg smsConfig) []credentials.ExpectedRef {
	if cfg.Mode == smsModeOff {
		return nil
	}
	out := make([]credentials.ExpectedRef, 0, len(sms.AllProviders)+1)
	for _, id := range sms.AllProviders {
		out = append(out, credentials.ExpectedRef{
			Ref: smsCredentialRef(id), Platform: "sms",
			Purpose: "接码供应商 " + id + " 的 API 密钥（填好后到「接码中心」做连接测试再启用）",
		})
	}
	out = append(out, credentials.ExpectedRef{
		Ref: smsNotifyWebhookRef, Platform: "sms",
		Purpose: "接码验证码推送的企业微信群机器人 Webhook（群成员都能看到码，建议指向只有自己的群）",
	})
	return out
}
