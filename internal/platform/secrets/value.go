package secrets

import "log/slog"

const redacted = "[REDACTED]"

// SecretValue 持有解析出的明文。明文只能经 Reveal() 显式取出；
// 打印、%v/%#v、JSON、slog 一律输出 [REDACTED]（宪法 7 条）。
type SecretValue struct {
	v []byte
}

func NewSecretValue(b []byte) SecretValue {
	cp := make([]byte, len(b))
	copy(cp, b)
	return SecretValue{v: cp}
}

// Reveal 返回明文。调用方负责不将其写入日志、错误信息或任何响应。
func (s SecretValue) Reveal() string { return string(s.v) }

func (s SecretValue) IsZero() bool { return len(s.v) == 0 }

func (s SecretValue) String() string   { return redacted }
func (s SecretValue) GoString() string { return redacted }

// MarshalJSON 拒绝序列化：SecretValue 不允许进入任何 JSON 响应或存储。
func (s SecretValue) MarshalJSON() ([]byte, error) { return nil, ErrRedacted }

// LogValue 保证 slog 场景只输出脱敏标记。
func (s SecretValue) LogValue() slog.Value { return slog.StringValue(redacted) }

// SecretMetadata 描述一个引用在某 Provider 下的可用性（不含值）。
type SecretMetadata struct {
	Ref       CredentialRef
	Provider  string
	Available bool
}
