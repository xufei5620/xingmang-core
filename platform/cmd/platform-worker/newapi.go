package main

import (
	"log/slog"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// newapiTokenEnvVar 是只读凭据在 env Provider 下的落点。
//
// Connector **不认识**这个名字：它只拿到 secret://<scope>/<name> 形式的引用，
// 由这里的登记表决定去哪儿取（ADR-014：环境变量只是内部适配实现，
// 不属于 Connector 契约）。将来换 SOPS/Vault，改的只有本文件。
//
// ⚠️ 这个变量装的是 NewAPI **管理员**的 access token：上游没有只读角色
// （普查报告核实过），所以这把 token 在上游是全权限的。平台侧靠四道只读闸
// 加一份写端点黑名单机械保证碰不到写路径，但凭据本身的权限平台管不了——
// 见 docs/runbooks/SWITCH-NEWAPI-REAL.md 的风险说明。
const newapiTokenEnvVar = "XM_NEWAPI_TOKEN"

// newapiSecretsFromEnv 装配 NewAPI 只读凭据的 Provider：审计过的文件
// Provider（XM_SECRET_ROOT，后台写入）在前，显式登记的 EnvProvider 兜底
// （XM-CRED0，装配细节见 connectorSecretsChain）。
//
// 与 sub2apiSecretsFromEnv 是同一套装配、两份实例：两条采集链路各用各的
// 登记表，共用一个 Provider 会让其中一边的凭据轮换影响到另一边。
func newapiSecretsFromEnv(
	getenv func(string) string, logger *slog.Logger, environment, refText, secretRoot string,
) (secrets.SecretProvider, error) {
	return connectorSecretsChain(getenv, logger, environment, secretRoot,
		refText, "XM_NEWAPI_CREDENTIAL_REF", newapiTokenEnvVar)
}
