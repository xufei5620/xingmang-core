package main

import (
	"log/slog"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// sub2apiTokenEnvVar 是只读凭据在 env Provider 下的落点。
//
// Connector **不认识**这个名字：它只拿到 secret://<scope>/<name> 形式的引用，
// 由这里的登记表决定去哪儿取（ADR-014：环境变量只是内部适配实现，
// 不属于 Connector 契约）。将来换 SOPS/Vault，改的只有本文件。
const sub2apiTokenEnvVar = "XM_SUB2API_TOKEN"

// sub2apiSecretsFromEnv 装配 Sub2API 只读凭据的 Provider：审计过的文件
// Provider（XM_SECRET_ROOT，后台写入）在前，显式登记的 EnvProvider 兜底
// （XM-CRED0，装配细节见 connectorSecretsChain）。
//
// refText 为空时仍返回只有文件一环的链——引用可以来自 core.connector_config，
// 凭据可以来自后台写的文件，两者都不经过 .env。refText 拼错仍是启动错误。
func sub2apiSecretsFromEnv(
	getenv func(string) string, logger *slog.Logger, environment, refText, secretRoot string,
) (secrets.SecretProvider, error) {
	return connectorSecretsChain(getenv, logger, environment, secretRoot,
		refText, "XM_SUB2API_CREDENTIAL_REF", sub2apiTokenEnvVar)
}
