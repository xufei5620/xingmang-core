package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// stubProvider 是可编程的 Provider 成员：按需返回值、错误或可用性，
// 并记录被调用次数，用来证明链在该停的地方停了。
type stubProvider struct {
	id        string
	value     string
	err       error
	available bool
	calls     int
}

func (s *stubProvider) ID() string { return s.id }

func (s *stubProvider) Resolve(_ context.Context, ref CredentialRef, _ string) (SecretValue, error) {
	s.calls++
	if s.err != nil {
		return SecretValue{}, fmt.Errorf("%s: %s: %w", s.id, ref, s.err)
	}
	return NewSecretValue([]byte(s.value)), nil
}

func (s *stubProvider) Metadata(_ context.Context, ref CredentialRef) (SecretMetadata, error) {
	if s.err != nil && !chainMiss(s.err) {
		return SecretMetadata{Ref: ref, Provider: s.id}, s.err
	}
	return SecretMetadata{Ref: ref, Provider: s.id, Available: s.available}, nil
}

var chainRef = MustCredentialRef("secret://sub2api/read-token")

func TestChainReturnsFirstSuccess(t *testing.T) {
	first := &stubProvider{id: "file", value: "test-value-file"}
	second := &stubProvider{id: "env", value: "test-value-env"}
	chain := NewChain(first, second)

	v, err := chain.Resolve(context.Background(), chainRef, "t")
	if err != nil || v.Reveal() != "test-value-file" {
		t.Fatalf("应命中第一个成员: %q, %v", v.Reveal(), err)
	}
	if second.calls != 0 {
		t.Fatal("第一个成员命中后不该再问第二个")
	}
}

func TestChainFallsThroughOnMiss(t *testing.T) {
	for _, miss := range []error{ErrNotFound, ErrUnknownScope, ErrUnmappedRef} {
		first := &stubProvider{id: "file", err: miss}
		second := &stubProvider{id: "env", value: "test-value-env"}
		v, err := NewChain(first, second).Resolve(context.Background(), chainRef, "t")
		if err != nil || v.Reveal() != "test-value-env" {
			t.Fatalf("%v 应视为未命中并落到下一个成员: %q, %v", miss, v.Reveal(), err)
		}
	}
}

func TestChainAllMissIsNotFound(t *testing.T) {
	first := &stubProvider{id: "file", err: ErrNotFound}
	second := &stubProvider{id: "env", err: ErrUnmappedRef}
	_, err := NewChain(first, second).Resolve(context.Background(), chainRef, "t")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("全部未命中应归 ErrNotFound, got %v", err)
	}
	if !errors.Is(err, ErrUnmappedRef) {
		t.Fatalf("最后一个成员的原因应保留在错误链里, got %v", err)
	}
	if errKind(err) != "not_found" {
		t.Fatalf("审计 error_code 应为 not_found, got %q", errKind(err))
	}
}

func TestChainStopsOnHardFailure(t *testing.T) {
	// 文件读取的 IO 错误（权限、目录损坏）不是「没有」，不许拿 env 的值盖过去。
	ioErr := errors.New("permission denied")
	for _, hard := range []error{ioErr, ErrEmptySecret} {
		first := &stubProvider{id: "file", err: hard}
		second := &stubProvider{id: "env", value: "test-value-env"}
		_, err := NewChain(first, second).Resolve(context.Background(), chainRef, "t")
		if err == nil || !errors.Is(err, hard) {
			t.Fatalf("硬失败 %v 应原地停下并透传, got %v", hard, err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Fatalf("硬失败不该被伪装成 not_found: %v", err)
		}
		if second.calls != 0 {
			t.Fatalf("硬失败后不该再问下一个成员（%v）", hard)
		}
	}
}

func TestChainSkipsNilAndRejectsEmpty(t *testing.T) {
	only := &stubProvider{id: "env", value: "test-value-env"}
	v, err := NewChain(nil, only, nil).Resolve(context.Background(), chainRef, "t")
	if err != nil || v.Reveal() != "test-value-env" {
		t.Fatalf("nil 成员应被跳过: %q, %v", v.Reveal(), err)
	}
	if _, err := NewChain(nil).Resolve(context.Background(), chainRef, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空链应 ErrNotFound（fail closed）, got %v", err)
	}
	m, err := NewChain().Metadata(context.Background(), chainRef)
	if err != nil || m.Available {
		t.Fatalf("空链 Metadata 应为不可用且无错: %+v, %v", m, err)
	}
}

func TestChainMetadataPrefersFirstAvailable(t *testing.T) {
	file := &stubProvider{id: "file", available: false}
	env := &stubProvider{id: "env", available: true}
	m, err := NewChain(file, env).Metadata(context.Background(), chainRef)
	if err != nil || !m.Available || m.Provider != "env" {
		t.Fatalf("应返回第一个可用成员的元数据: %+v, %v", m, err)
	}
	// 都不可用：返回最后一个成员的结果，调用方看得出最终落到哪儿。
	none := &stubProvider{id: "env", available: false}
	m, err = NewChain(file, none).Metadata(context.Background(), chainRef)
	if err != nil || m.Available || m.Provider != "env" {
		t.Fatalf("都不可用时应返回最后一个成员: %+v, %v", m, err)
	}
}

func TestChainWithRealProvidersFilePreferredEnvFallback(t *testing.T) {
	root := t.TempDir()
	writeSecretFile(t, root, "sub2api/read-token", "test-value-file\n")
	env, err := NewEnvProvider(
		map[string]string{
			"secret://sub2api/read-token": "XM_A",
			"secret://sub2api/env-only":   "XM_B",
		},
		WithLookup(fakeLookup(map[string]string{"XM_A": "test-value-env", "XM_B": "test-value-env-only"})),
	)
	if err != nil {
		t.Fatal(err)
	}
	chain := NewChain(NewFileProvider(root), env)
	ctx := context.Background()

	v, err := chain.Resolve(ctx, chainRef, "t")
	if err != nil || v.Reveal() != "test-value-file" {
		t.Fatalf("文件存在时应以文件为准: %q, %v", v.Reveal(), err)
	}
	v, err = chain.Resolve(ctx, MustCredentialRef("secret://sub2api/env-only"), "t")
	if err != nil || v.Reveal() != "test-value-env-only" {
		t.Fatalf("文件缺失时应落到 env: %q, %v", v.Reveal(), err)
	}
	_, err = chain.Resolve(ctx, MustCredentialRef("secret://sub2api/nowhere"), "t")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("两边都没有应 ErrNotFound, got %v", err)
	}
	if strings.Contains(err.Error(), "test-value") {
		t.Fatalf("错误信息不得含任何值: %v", err)
	}
}
