package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"invoice-system/backend/internal/securefields"
)

type FlowPurpose string

const (
	FlowLogin          FlowPurpose = "login"
	FlowAdminStepUp    FlowPurpose = "admin_step_up"
	FlowAccountBinding FlowPurpose = "account_binding"
)

func (p FlowPurpose) Valid() bool {
	return p == FlowLogin || p == FlowAdminStepUp || p == FlowAccountBinding
}

type AuthorizationFlow struct {
	StateHash              string
	NonceHash              string
	BrowserBindingHash     string
	CodeVerifierCiphertext []byte
	CodeVerifierKeyVersion string
	Purpose                FlowPurpose
	ExpectedIdentityHash   string
	ExistingSessionID      string
	ReturnPath             string
	CreatedAt              time.Time
	ExpiresAt              time.Time
	ConsumedAt             *time.Time
}

type FlowStore interface {
	Create(ctx context.Context, flow AuthorizationFlow) error
	Consume(ctx context.Context, stateHash, browserBindingHash string, now time.Time) (AuthorizationFlow, error)
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

type FlowSecretProtector interface {
	Seal(plaintext []byte, aad string) (ciphertext []byte, keyVersion string, err error)
	Open(ciphertext []byte, keyVersion, aad string) ([]byte, error)
}

// SecureFieldsFlowProtector reuses the application's versioned AES-GCM
// keyring. The state hash is AAD, so encrypted PKCE verifiers cannot be moved
// between authorization rows.
type SecureFieldsFlowProtector struct{ Keyring securefields.Keyring }

func (p SecureFieldsFlowProtector) Seal(plaintext []byte, aad string) ([]byte, string, error) {
	ciphertext, err := p.Keyring.Encrypt(plaintext, aad)
	return ciphertext, p.Keyring.CurrentKeyID, err
}

func (p SecureFieldsFlowProtector) Open(ciphertext []byte, keyVersion, aad string) ([]byte, error) {
	if strings.TrimSpace(keyVersion) == "" {
		return nil, errors.New("authorization flow key version is missing")
	}
	return p.Keyring.Decrypt(ciphertext, aad)
}

type MemoryFlowStore struct {
	mu    sync.Mutex
	flows map[string]AuthorizationFlow
}

func NewMemoryFlowStore() *MemoryFlowStore {
	return &MemoryFlowStore{flows: make(map[string]AuthorizationFlow)}
}

func (m *MemoryFlowStore) Create(_ context.Context, flow AuthorizationFlow) error {
	if err := validateFlow(flow); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.flows[flow.StateHash]; exists {
		return errors.New("authorization state collision")
	}
	flow.CodeVerifierCiphertext = append([]byte(nil), flow.CodeVerifierCiphertext...)
	m.flows[flow.StateHash] = flow
	return nil
}

func (m *MemoryFlowStore) Consume(_ context.Context, stateHash, browserBindingHash string, now time.Time) (AuthorizationFlow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	flow, ok := m.flows[stateHash]
	if !ok || flow.ConsumedAt != nil || !flow.ExpiresAt.After(now) || !secureEqualHex(flow.BrowserBindingHash, browserBindingHash) {
		return AuthorizationFlow{}, ErrInvalidFlow
	}
	consumed := now.UTC()
	flow.ConsumedAt = &consumed
	m.flows[stateHash] = flow
	flow.CodeVerifierCiphertext = append([]byte(nil), flow.CodeVerifierCiphertext...)
	return flow, nil
}

func (m *MemoryFlowStore) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for key, flow := range m.flows {
		if !flow.ExpiresAt.After(now) || flow.ConsumedAt != nil && flow.ConsumedAt.Before(now.Add(-time.Hour)) {
			delete(m.flows, key)
			count++
		}
	}
	return count, nil
}

func validateFlow(flow AuthorizationFlow) error {
	if len(flow.StateHash) != 64 || len(flow.NonceHash) != 64 || len(flow.BrowserBindingHash) != 64 {
		return errors.New("authorization flow hashes are invalid")
	}
	if len(flow.CodeVerifierCiphertext) == 0 || strings.TrimSpace(flow.CodeVerifierKeyVersion) == "" {
		return errors.New("authorization flow PKCE verifier is missing")
	}
	if !flow.Purpose.Valid() || flow.CreatedAt.IsZero() || !flow.ExpiresAt.After(flow.CreatedAt) || flow.ExpiresAt.Sub(flow.CreatedAt) > 20*time.Minute {
		return errors.New("authorization flow metadata is invalid")
	}
	if err := validateReturnPath(flow.ReturnPath); err != nil {
		return err
	}
	if flow.Purpose == FlowLogin && (flow.ExpectedIdentityHash != "" || flow.ExistingSessionID != "") {
		return errors.New("login flows cannot be bound to an existing identity or session")
	}
	if flow.Purpose != FlowLogin && (len(flow.ExpectedIdentityHash) != 64 || !validUUIDString(flow.ExistingSessionID)) {
		return errors.New("step-up and binding flows must be bound to an existing identity and session")
	}
	return nil
}

func validateReturnPath(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n\x00") {
		return errors.New("return path must be a safe absolute application path")
	}
	return nil
}

func randomURLToken(bytes int) (string, error) {
	if bytes < 32 {
		return "", errors.New("security token entropy must be at least 256 bits")
	}
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
