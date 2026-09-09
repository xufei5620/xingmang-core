package secrets

import (
	"context"
	"errors"
)

// SecretProvider 是凭据解析的统一接口（签名逐字来自规格 §4.5）。
// purpose 说明用途（如 "sub2api sync"），进入审计，不影响解析结果。
type SecretProvider interface {
	Resolve(ctx context.Context, ref CredentialRef, purpose string) (SecretValue, error)
	Metadata(ctx context.Context, ref CredentialRef) (SecretMetadata, error)
}

var (
	// ErrNotFound：引用在该 Provider 下不存在。
	ErrNotFound = errors.New("secret not found")
	// ErrEmptySecret：找到了但内容为空——按 fail-closed 处理为错误。
	ErrEmptySecret = errors.New("secret is empty")
	// ErrUnknownScope：Router 未登记该 scope（禁止静默回退，规格 §18.1-5）。
	ErrUnknownScope = errors.New("unknown secret scope")
	// ErrUnmappedRef：EnvProvider 未登记该引用的环境变量映射。
	ErrUnmappedRef = errors.New("credential ref not mapped to env var")
	// ErrRedacted：SecretValue 拒绝序列化。
	ErrRedacted = errors.New("secret value is redacted")
)

// errKind 把错误归类为审计用 error_code（不携带路径等细节）。
func errKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrEmptySecret):
		return "empty"
	case errors.Is(err, ErrUnknownScope):
		return "unknown_scope"
	case errors.Is(err, ErrUnmappedRef):
		return "unmapped"
	default:
		return "other"
	}
}
