package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileProvider 从受限目录读取凭据文件。
// 两种布局：
//   - 嵌套（SOPS 解密目录 / 受限文件注入）：<root>/<scope>/<name>
//   - 扁平（Docker Secret 挂载）：<root>/<scope>__<name>
//
// 无任何回退：文件不存在即 ErrNotFound。
type FileProvider struct {
	root string
	id   string
	path func(CredentialRef) string
}

// NewFileProvider 创建嵌套布局 Provider（<root>/<scope>/<name>）。
func NewFileProvider(root string) *FileProvider {
	return &FileProvider{
		root: root,
		id:   "file:" + root,
		path: func(r CredentialRef) string {
			return filepath.Join(root, r.Scope(), r.Name())
		},
	}
}

// NewDockerSecretProvider 创建 Docker Secret Provider（/run/secrets/<scope>__<name>）。
// Compose 侧约定：secret 目标文件名为 <scope>__<name>（双下划线）。
func NewDockerSecretProvider() *FileProvider { return NewDockerSecretProviderAt("/run/secrets") }

// NewDockerSecretProviderAt 同上，根目录可注入（测试用）。
func NewDockerSecretProviderAt(root string) *FileProvider {
	return &FileProvider{
		root: root,
		id:   "docker-secret",
		path: func(r CredentialRef) string {
			return filepath.Join(root, r.Scope()+"__"+r.Name())
		},
	}
}

func (p *FileProvider) ID() string { return p.id }

func (p *FileProvider) Resolve(_ context.Context, ref CredentialRef, _ string) (SecretValue, error) {
	if ref.IsZero() {
		return SecretValue{}, fmt.Errorf("file provider: 零值 ref: %w", ErrNotFound)
	}
	b, err := os.ReadFile(p.path(ref))
	if err != nil {
		if os.IsNotExist(err) {
			return SecretValue{}, fmt.Errorf("file provider: %s: %w", ref, ErrNotFound)
		}
		// 其他 IO 错误原样包装（错误信息含路径，不含内容）
		return SecretValue{}, fmt.Errorf("file provider: %s: %w", ref, err)
	}
	s := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if s == "" {
		return SecretValue{}, fmt.Errorf("file provider: %s: %w", ref, ErrEmptySecret)
	}
	return NewSecretValue([]byte(s)), nil
}

func (p *FileProvider) Metadata(_ context.Context, ref CredentialRef) (SecretMetadata, error) {
	m := SecretMetadata{Ref: ref, Provider: p.id}
	if ref.IsZero() {
		return m, nil
	}
	if _, err := os.Stat(p.path(ref)); err == nil {
		m.Available = true
	}
	return m, nil
}
