package main

import (
	"log/slog"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// assuranceProbeSecretsFromEnv 装配探测专用凭据（core.connector_config.
// probe_credential_ref）的 Provider。
//
// 与 sub2apiSecretsFromEnv/newapiSecretsFromEnv 不同：那两个是"一个固定引用
// + env 兜底"的链（refText 在进程启动时就从环境变量读出来，指向一个具体的
// secret:// 引用）。探测凭据的引用**因平台而异、存在数据库里**（每平台各自
// 的 core.connector_config.probe_credential_ref，只有在 Job 真正执行的那一
// 刻才知道要解析哪一个），因此这里只需要一个"能解析任意 secret:// 引用"的
// 通用 Provider——调用既有的 connectorSecretsChain 并把 refText/两个 env
// 变量名都留空，得到的正是"只有文件一环"的链（见该函数对 refText=="" 分支
// 的注释），不新增任何装配逻辑。
func assuranceProbeSecretsFromEnv(
	getenv func(string) string, logger *slog.Logger, environment, secretRoot string,
) (secrets.SecretProvider, error) {
	return connectorSecretsChain(getenv, logger, environment, secretRoot, "", "", "")
}
