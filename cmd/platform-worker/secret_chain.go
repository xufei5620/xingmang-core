package main

import (
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 连接器凭据的「文件优先、env 兜底」装配（XM-CRED0）。
//
// 管理后台（platform-api）把凭据按 <root>/<scope>/<name> 写进 XM_SECRET_ROOT
// 对应的共享卷；worker 每轮同步现解析 CredentialRef，文件一出现就生效，
// 不用重启。还没在后台填过的引用仍能落到旧的环境变量登记表上——那是过渡期
// 的兜底，不是长期出路（用户 2026-08-30 拍板：凭据只在后台填）。

const (
	// secretRootEnvVar 是文件 Provider 的根目录；默认与 compose 里挂载的
	// 命名卷 xm-secrets 一致（platform-api 读写、platform-worker 只读）。
	secretRootEnvVar  = "XM_SECRET_ROOT"
	defaultSecretRoot = "/run/xm/secrets"
)

// parseSecretRoot 只校验路径形状：必须是绝对路径且不是根目录。
//
// 同时接受 POSIX 与本机形式的绝对路径：进程跑在 Linux 容器里，默认值是
// POSIX 路径；而单元测试在开发机（可能是 Windows）上用 t.TempDir()。
// 这里**不**检查目录是否存在——卷可以晚于进程出现，缺文件的后果是
// Resolve 时的 not_found，那有审计记录，比启动即崩更可诊断。
func parseSecretRoot(raw string) (string, error) {
	root := strings.TrimSpace(raw)
	if root == "" {
		return defaultSecretRoot, nil
	}
	if !path.IsAbs(root) && !filepath.IsAbs(root) {
		return "", fmt.Errorf("%s 必须是绝对路径，got %q", secretRootEnvVar, root)
	}
	if path.Clean(root) == "/" || filepath.Clean(root) == string(filepath.Separator) {
		return "", fmt.Errorf("%s 不能是根目录", secretRootEnvVar)
	}
	return root, nil
}

// connectorSecretsChain 装配一条链：审计过的文件 Provider 在前，审计过的
// env 登记表在后。
//
// refText 为空时只有文件这一环——「没配 env 引用」不再意味着没有 Provider：
// 引用可以来自 core.connector_config，凭据可以来自后台写的文件，两者都不
// 经过 .env。refText 配了但拼错仍是启动错误：那是配置错误，等到采集那天
// 才发现更贵。
//
// 两环各自包 Audited 而不是链整体包一层：审计里要看得出「这次是文件命中的
// 还是 env 兜底的」，链自己不产生审计记录。
func connectorSecretsChain(
	getenv func(string) string, logger *slog.Logger,
	environment, secretRoot, refText, refEnvVar, tokenEnvVar string,
) (secrets.SecretProvider, error) {
	if getenv == nil {
		return nil, fmt.Errorf("environment reader is required")
	}
	root, err := parseSecretRoot(secretRoot)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	recorder := secrets.NewSlogRecorder(logger)
	file := secrets.NewAudited(secrets.NewFileProvider(root), recorder, environment)

	refText = strings.TrimSpace(refText)
	if refText == "" {
		return secrets.NewChain(file), nil
	}
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", refEnvVar, err)
	}
	env, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): tokenEnvVar},
		secrets.WithLookup(func(name string) (string, bool) {
			value := getenv(name)
			return value, value != ""
		}),
	)
	if err != nil {
		return nil, err
	}
	return secrets.NewChain(file, secrets.NewAudited(env, recorder, environment)), nil
}
