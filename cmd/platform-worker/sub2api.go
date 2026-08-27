package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// sub2apiTokenEnvVar 是只读凭据在 env Provider 下的落点。
//
// Connector **不认识**这个名字：它只拿到 secret://<scope>/<name> 形式的引用，
// 由这里的登记表决定去哪儿取（ADR-014：环境变量只是内部适配实现，
// 不属于 Connector 契约）。将来换 SOPS/Vault，改的只有本文件。
const sub2apiTokenEnvVar = "XM_SUB2API_TOKEN"

// sub2apiSecretsFromEnv 按 worker 既有模式装配 Sub2API 只读凭据的 Provider：
// 显式登记的 EnvProvider + 每次读取都留审计的 Audited 装饰器。
//
// refText 为空时返回 (nil, nil)——「没配凭据引用」不是启动错误：real 模式会
// 在每轮同步把它写成一条说得清缺哪个变量的 SyncFailed 观测，而 fake 模式
// 根本用不到 Provider。让一个还没配好的采集通道拖垮整个 worker 是更糟的失败。
func sub2apiSecretsFromEnv(
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
		return nil, fmt.Errorf("XM_SUB2API_CREDENTIAL_REF: %w", err)
	}
	provider, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): sub2apiTokenEnvVar},
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
	// 「这个只读账号什么时候被谁用过」才查得出来（规格 §4.5）。
	return secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment), nil
}
