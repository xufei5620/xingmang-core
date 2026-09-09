package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// alertTelegramTokenEnvVar 是 Telegram Bot Token 在 env Provider 下的落点。
//
// 告警模块**不认识**这个名字：它只拿到 secret://<scope>/<name> 形式的引用，
// 由这里的登记表决定去哪儿取（ADR-014：环境变量只是内部适配实现，
// 不属于契约）。将来换 SOPS/Vault，改的只有本文件。
const alertTelegramTokenEnvVar = "XM_ALERT_TELEGRAM_TOKEN"

// alertSecretsFromEnv 按 worker 既有模式装配告警凭据的 Provider：
// 显式登记的 EnvProvider + 每次读取都留审计的 Audited 装饰器。
//
// 与 sub2apiSecretsFromEnv 的**唯一差别**是它更严：那边引用没配就返回
// (nil, nil)，因为 fake 模式根本用不到 Provider，而 real 模式的缺配会
// 每轮写成一条看得见的 SyncFailed 观测。这里没有那条可见的反馈路径——
// 一个解析不出 token 的 Telegram 渠道只会在每条告警上留一行 notify_error，
// 而那时告警已经该送到人手上了。所以：引用没配 → (nil, nil)，
// 由 jobs.newAlertNotifier 决定是「不启用 Telegram」还是「配了一半，拒绝启动」；
// 引用配了但拼错 → 立刻报错，进程起不来。
func alertSecretsFromEnv(
	getenv func(string) string, logger *slog.Logger, environment, refText string,
) (secrets.SecretProvider, error) {
	if getenv == nil {
		return nil, fmt.Errorf("environment reader is required")
	}
	refText = strings.TrimSpace(refText)
	if refText == "" {
		return nil, nil
	}
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		return nil, fmt.Errorf("XM_ALERT_TELEGRAM_BOT_REF: %w", err)
	}
	provider, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): alertTelegramTokenEnvVar},
		secrets.WithLookup(func(name string) (string, bool) {
			value := getenv(name)
			return value, value != ""
		}),
	)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	// 审计装饰器包在外面：每次解析（无论成败）都留一条不含明文的记录，
	// 「这个 Bot Token 什么时候被用过」才查得出来（规格 §4.5）。
	return secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment), nil
}

// alertWeComWebhookEnvVar 是企业微信群机器人 Webhook 地址在 env Provider
// 下的落点——与 alertTelegramTokenEnvVar 同一条约定（XM-ALERT-WECOM）。
const alertWeComWebhookEnvVar = "XM_ALERT_WECOM_WEBHOOK"

// alertWeComSecretsFromEnv 装配企业微信 Webhook 地址的 Provider：审计过的
// 文件 Provider（XM_SECRET_ROOT，后台写入）在前，显式登记的 EnvProvider
// 兜底——与 sub2api/newapi 同一套装配（XM-CRED0，见 connectorSecretsChain），
// **不是**照抄 Telegram 现在还在用的纯 env 装配（alertSecretsFromEnv）。
//
// 之所以不一样：企微 Webhook 地址要能从后台「设置→凭据」页粘贴、worker
// 不重启就生效，就必须挂上文件 Provider 那一环；而 Telegram 的装配是
// XM-CRED0 之前的旧路径，尚未迁移（见 alertSecretsFromEnv 的注释），
// 本次改动不动它，避免打乱一条已经在跑且有完整测试覆盖的链路。
//
// refText 为空时仍返回只有文件一环的链——同 sub2apiSecretsFromEnv 的注释：
// 引用可以来自登记簿，凭据可以来自后台写的文件，两者都不经过 .env。
// refText 配了但拼错仍是启动错误。
func alertWeComSecretsFromEnv(
	getenv func(string) string, logger *slog.Logger, environment, refText, secretRoot string,
) (secrets.SecretProvider, error) {
	return connectorSecretsChain(getenv, logger, environment, secretRoot,
		refText, "XM_ALERT_WECOM_WEBHOOK_REF", alertWeComWebhookEnvVar)
}
