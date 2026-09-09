package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

const refScheme = "secret://"

// refPart：小写字母数字开头，后接小写字母数字或连字符，总长 1~64。
var refPart = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// CredentialRef 是对凭据的间接引用，形如 secret://<scope>/<name>。
// 字段不导出：只能经 ParseCredentialRef 构造，保证任何存在的实例都合法。
// Ref 本身不是秘密，可安全出现在日志、配置与审计中。
type CredentialRef struct {
	scope string
	name  string
}

// ParseCredentialRef 解析并校验 secret://<scope>/<name> 形式的引用。
func ParseCredentialRef(s string) (CredentialRef, error) {
	rest, ok := strings.CutPrefix(s, refScheme)
	if !ok {
		return CredentialRef{}, fmt.Errorf("credential ref %q: 缺少 %s 前缀", s, refScheme)
	}
	scope, name, ok := strings.Cut(rest, "/")
	if !ok {
		return CredentialRef{}, fmt.Errorf("credential ref %q: 需要 <scope>/<name> 两段", s)
	}
	if !refPart.MatchString(scope) {
		return CredentialRef{}, fmt.Errorf("credential ref %q: scope 非法（^[a-z0-9][a-z0-9-]{0,63}$）", s)
	}
	if !refPart.MatchString(name) {
		return CredentialRef{}, fmt.Errorf("credential ref %q: name 非法（^[a-z0-9][a-z0-9-]{0,63}$）", s)
	}
	return CredentialRef{scope: scope, name: name}, nil
}

// MustCredentialRef 供测试与静态装配使用；解析失败即 panic。
func MustCredentialRef(s string) CredentialRef {
	ref, err := ParseCredentialRef(s)
	if err != nil {
		panic(err)
	}
	return ref
}

func (r CredentialRef) Scope() string { return r.scope }
func (r CredentialRef) Name() string  { return r.name }
func (r CredentialRef) IsZero() bool  { return r == CredentialRef{} }

func (r CredentialRef) String() string {
	return refScheme + r.scope + "/" + r.name
}

func (r CredentialRef) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *CredentialRef) UnmarshalText(b []byte) error {
	ref, err := ParseCredentialRef(string(b))
	if err != nil {
		return err
	}
	*r = ref
	return nil
}
