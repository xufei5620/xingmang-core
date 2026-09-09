package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

const (
	financeSecretProviderEnvVar = "XM_FINANCE_COLLECT_SECRET_PROVIDER"
	financeSecretRootEnvVar     = "XM_FINANCE_COLLECT_SECRET_ROOT"
	financeSecretScopesEnvVar   = "XM_FINANCE_COLLECT_SECRET_SCOPES"
	financeSecretEnvPrefix      = "XM_FINANCE_SECRET_"
	// 固定容器路径；宿主机可通过只读 bind mount 提供嵌套文件。
	defaultFinanceSecretRoot = "/run/xm/finance-secrets"
)

// parseFinanceSecretScopes 只解析逗号分隔的 CredentialRef scope，不读取任何
// 值。空项忽略、重复项去重；非法 scope 在启动前 fail-closed。
func parseFinanceSecretScopes(raw string) ([]string, error) {
	seen := make(map[string]struct{})
	var scopes []string
	for _, item := range strings.Split(raw, ",") {
		scope := strings.TrimSpace(item)
		if scope == "" {
			continue
		}
		if _, err := secrets.ParseCredentialRef("secret://" + scope + "/placeholder"); err != nil {
			return nil, fmt.Errorf("%s scope 非法", financeSecretScopesEnvVar)
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// financeSecretsFromEnv 装配成本采集用的动态 SecretProvider。
//
// Provider 按每次 Resolve 的 CredentialRef 现场映射，不在启动时枚举数据库，
// 也不缓存明文。env/file 是显式互斥来源，scope 必须白名单；解析结果再包一层
// Audited，调用方只能拿到 SecretValue（日志/JSON 自动脱敏）。
func financeSecretsFromEnv(
	getenv func(string) string,
	logger *slog.Logger,
	environment string,
	mode jobs.FinanceCollectMode,
	providerMode string,
	root string,
	scopes []string,
) (secrets.SecretProvider, error) {
	if getenv == nil {
		return nil, fmt.Errorf("environment reader is required")
	}
	return financeSecretsFromLookup(func(name string) (string, bool) {
		value := getenv(name)
		return value, value != ""
	}, logger, environment, mode, providerMode, root, scopes)
}

// financeSecretsFromLookup is the lossless variant used by the real worker
// entrypoint. os.LookupEnv lets the provider distinguish an unset variable
// (ErrNotFound) from an explicitly empty one (ErrEmptySecret).
func financeSecretsFromLookup(
	lookup func(string) (string, bool),
	logger *slog.Logger,
	environment string,
	mode jobs.FinanceCollectMode,
	providerMode string,
	root string,
	scopes []string,
) (secrets.SecretProvider, error) {
	if lookup == nil {
		return nil, fmt.Errorf("environment lookup is required")
	}
	providerMode = strings.TrimSpace(providerMode)
	if providerMode == "" {
		// 未装配不是启动错误；real 采集器会按既有契约将其记为 not_supported。
		return nil, nil
	}
	if providerMode != "env" && providerMode != "file" {
		return nil, fmt.Errorf("%s 只接受 env 或 file", financeSecretProviderEnvVar)
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%s 已启用但 scope allowlist 为空", financeSecretScopesEnvVar)
	}
	if strings.TrimSpace(environment) == "" {
		return nil, fmt.Errorf("environment is required")
	}

	var provider secrets.SecretProvider
	var err error
	switch providerMode {
	case "env":
		provider, err = secrets.NewConventionEnvProvider(
			financeSecretEnvPrefix,
			scopes,
			lookup,
		)
	case "file":
		root = strings.TrimSpace(root)
		if root == "" {
			root = defaultFinanceSecretRoot
		}
		provider, err = secrets.NewScopedFileProvider(root, scopes)
	}
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	// mode 仅用于调用方日志与测试语义；provider 本身不因 fake 解析任何值。
	_ = mode
	return secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment), nil
}
