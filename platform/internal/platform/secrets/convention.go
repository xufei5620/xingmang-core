package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ConventionEnvProvider resolves dynamic CredentialRefs using a documented,
// injective environment-variable convention.  It deliberately does not
// enumerate or cache refs: account/token mappings can be added or rotated in
// the registry without restarting the worker.
type ConventionEnvProvider struct {
	prefix string
	scopes map[string]struct{}
	lookup func(string) (string, bool)
}

var envPrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_$`)

// NewConventionEnvProvider constructs a provider for an explicit scope
// allowlist.  Empty allowlists are rejected so a caller cannot accidentally
// turn an arbitrary registry scope into an environment-variable lookup.
func NewConventionEnvProvider(
	prefix string,
	allowedScopes []string,
	lookup func(string) (string, bool),
) (*ConventionEnvProvider, error) {
	prefix = strings.TrimSpace(prefix)
	if !envPrefixPattern.MatchString(prefix) {
		return nil, fmt.Errorf("secret env prefix 非法")
	}
	scopes, err := normalizeSecretScopes(allowedScopes)
	if err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("secret scope allowlist 不能为空")
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &ConventionEnvProvider{prefix: prefix, scopes: scopes, lookup: lookup}, nil
}

func (p *ConventionEnvProvider) ID() string { return "env-convention" }

// ConventionEnvName returns the injective variable name for a valid ref.
// CredentialRef parts cannot contain underscores, so '-'→'_' remains
// reversible within this convention; the double underscore separates parts.
func ConventionEnvName(prefix string, ref CredentialRef) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if !envPrefixPattern.MatchString(prefix) {
		return "", fmt.Errorf("secret env prefix 非法")
	}
	if ref.IsZero() {
		return "", fmt.Errorf("credential ref 不能为空")
	}
	encode := func(part string) string {
		return strings.ToUpper(strings.ReplaceAll(part, "-", "_"))
	}
	return prefix + encode(ref.Scope()) + "__" + encode(ref.Name()), nil
}

func (p *ConventionEnvProvider) allowed(scope string) bool {
	_, ok := p.scopes[scope]
	return ok
}

func (p *ConventionEnvProvider) Resolve(
	_ context.Context, ref CredentialRef, _ string,
) (SecretValue, error) {
	if ref.IsZero() {
		return SecretValue{}, fmt.Errorf("env convention: %w", ErrNotFound)
	}
	if !p.allowed(ref.Scope()) {
		return SecretValue{}, fmt.Errorf("env convention: scope %q: %w", ref.Scope(), ErrUnknownScope)
	}
	name, err := ConventionEnvName(p.prefix, ref)
	if err != nil {
		return SecretValue{}, err
	}
	value, ok := p.lookup(name)
	if !ok {
		return SecretValue{}, fmt.Errorf("env convention: %s: %w", ref, ErrNotFound)
	}
	if value == "" {
		return SecretValue{}, fmt.Errorf("env convention: %s: %w", ref, ErrEmptySecret)
	}
	return NewSecretValue([]byte(value)), nil
}

func (p *ConventionEnvProvider) Metadata(
	_ context.Context, ref CredentialRef,
) (SecretMetadata, error) {
	meta := SecretMetadata{Ref: ref, Provider: p.ID()}
	if ref.IsZero() {
		return meta, nil
	}
	if !p.allowed(ref.Scope()) {
		return meta, fmt.Errorf("env convention: scope %q: %w", ref.Scope(), ErrUnknownScope)
	}
	name, err := ConventionEnvName(p.prefix, ref)
	if err != nil {
		return meta, err
	}
	value, ok := p.lookup(name)
	meta.Available = ok && value != ""
	return meta, nil
}

// ScopedFileProvider is the file equivalent of ConventionEnvProvider.  It
// keeps the existing nested FileProvider semantics while enforcing the same
// explicit scope allowlist.
type ScopedFileProvider struct {
	inner  *FileProvider
	scopes map[string]struct{}
}

// NewScopedFileProvider uses the nested <root>/<scope>/<name> layout.
func NewScopedFileProvider(root string, allowedScopes []string) (*ScopedFileProvider, error) {
	root = strings.TrimSpace(root)
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return nil, fmt.Errorf("secret file root 必须是非根绝对路径")
	}
	scopes, err := normalizeSecretScopes(allowedScopes)
	if err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("secret scope allowlist 不能为空")
	}
	return &ScopedFileProvider{inner: NewFileProvider(root), scopes: scopes}, nil
}

func (p *ScopedFileProvider) ID() string { return "file-scoped" }

func (p *ScopedFileProvider) allowed(scope string) bool {
	_, ok := p.scopes[scope]
	return ok
}

func (p *ScopedFileProvider) Resolve(
	ctx context.Context, ref CredentialRef, purpose string,
) (SecretValue, error) {
	if ref.IsZero() {
		return SecretValue{}, fmt.Errorf("file convention: %w", ErrNotFound)
	}
	if !p.allowed(ref.Scope()) {
		return SecretValue{}, fmt.Errorf("file convention: scope %q: %w", ref.Scope(), ErrUnknownScope)
	}
	return p.inner.Resolve(ctx, ref, purpose)
}

func (p *ScopedFileProvider) Metadata(
	ctx context.Context, ref CredentialRef,
) (SecretMetadata, error) {
	meta := SecretMetadata{Ref: ref, Provider: p.ID()}
	if ref.IsZero() {
		return meta, nil
	}
	if !p.allowed(ref.Scope()) {
		return meta, fmt.Errorf("file convention: scope %q: %w", ref.Scope(), ErrUnknownScope)
	}
	inner, err := p.inner.Metadata(ctx, ref)
	inner.Provider = p.ID()
	return inner, err
}

func normalizeSecretScopes(raw []string) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(raw))
	for _, scope := range raw {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if !refPart.MatchString(scope) {
			return nil, fmt.Errorf("secret scope %q 非法", scope)
		}
		out[scope] = struct{}{}
	}
	return out, nil
}
