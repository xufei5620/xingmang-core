package registry

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	// ErrInvalidEnvironment：环境名不在 development/staging/production 之列。
	ErrInvalidEnvironment = errors.New("invalid environment")
	// ErrInvalidStatus：状态值非法。
	ErrInvalidStatus = errors.New("invalid status")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("missing required field")
	// ErrInvalidFormat：字段格式非法。
	ErrInvalidFormat = errors.New("invalid format")
	// ErrInvalidCredentialRef：凭据引用不是合法 CredentialRef。
	ErrInvalidCredentialRef = errors.New("invalid credential ref")
	// ErrAllowlistRequired：Connector 未声明目标地址 allowlist（ADR-004）。
	ErrAllowlistRequired = errors.New("target allowlist required")
	// ErrKillSwitchRequired：具写能力却未声明 Kill Switch（ADR-004）。
	ErrKillSwitchRequired = errors.New("kill switch required for write capabilities")
)

// identifierPattern 与 CredentialRef 同族字符集，保证标识符可安全用于
// 路径、指标标签与日志字段。
var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidateIdentifier 校验标识符类字段。
func ValidateIdentifier(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s: %w", field, ErrMissingField)
	}
	if !identifierPattern.MatchString(v) {
		return fmt.Errorf("%s=%q 须匹配 ^[a-z0-9][a-z0-9-]{0,63}$: %w", field, v, ErrInvalidFormat)
	}
	return nil
}

func requireNonEmpty(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s: %w", field, ErrMissingField)
	}
	return nil
}
