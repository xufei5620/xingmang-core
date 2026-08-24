package adminsettings

import (
	"context"
	"sync"
	"time"
)

// MemoryRepository is used only by the local mock server. Production uses the
// PostgreSQL repository and an external, durable encryption key.
type MemoryRepository struct {
	mu         sync.Mutex
	settings   Settings
	configured bool
	secret     SecretEnvelope
}

func NewMemoryRepository(initial Settings) *MemoryRepository {
	if initial.EligibilityStartAt.IsZero() {
		initial.EligibilityStartAt = RequiredEligibilityStartAt
	}
	if initial.EligibilityPolicyVersion == 0 {
		initial.EligibilityPolicyVersion = 1
	}
	return &MemoryRepository{settings: initial, configured: initial.Revision > 0}
}
func (r *MemoryRepository) Get(context.Context) (Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.configured {
		return Settings{}, ErrNotConfigured
	}
	value := r.settings
	value.AdminCIDRs = append([]string(nil), value.AdminCIDRs...)
	value.SMTPSecretConfigured = len(r.secret.Ciphertext) > 0
	return value, nil
}
func (r *MemoryRepository) Update(_ context.Context, in UpdateInput, expected int64, actor Actor) (Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.configured && expected != 0 {
		return Settings{}, ErrRevisionConflict
	}
	if r.configured && r.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	now := time.Now().UTC()
	revision := int64(1)
	created := now
	if r.configured {
		revision = r.settings.Revision + 1
		created = r.settings.CreatedAt
	}
	policyVersion := r.settings.EligibilityPolicyVersion
	if policyVersion == 0 {
		policyVersion = 1
	}
	r.settings = Settings{IssuerName: in.IssuerName, ServiceItem: FixedServiceItem, MinimumRequestMinor: in.MinimumRequestMinor, EligibilityStartAt: in.EligibilityStartAt, EligibilityPolicyVersion: policyVersion, SMTPHost: in.SMTPHost, SMTPPort: in.SMTPPort, SMTPFrom: in.SMTPFrom, SMTPFromName: in.SMTPFromName, SMTPStartTLS: in.SMTPStartTLS, SMTPSecretConfigured: len(r.secret.Ciphertext) > 0, AdminCIDRs: append([]string(nil), in.AdminCIDRs...), Revision: revision, UpdatedBy: actor.ID, CreatedAt: created, UpdatedAt: now}
	r.configured = true
	return r.settings, nil
}
func (r *MemoryRepository) UpdateSMTP(_ context.Context, in UpdateInput, change SMTPSecretChange, envelope SecretEnvelope, expected int64, actor Actor) (Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.configured {
		return Settings{}, ErrNotConfigured
	}
	if r.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	switch change {
	case SMTPSecretUnchanged:
	case SMTPSecretSet:
		r.secret = SecretEnvelope{Ciphertext: append([]byte(nil), envelope.Ciphertext...), KeyVersion: envelope.KeyVersion}
	case SMTPSecretClear:
		r.secret = SecretEnvelope{}
	default:
		return Settings{}, ErrInvalidSettings
	}
	r.settings.SMTPHost = in.SMTPHost
	r.settings.SMTPPort = in.SMTPPort
	r.settings.SMTPFrom = in.SMTPFrom
	r.settings.SMTPFromName = in.SMTPFromName
	r.settings.SMTPStartTLS = in.SMTPStartTLS
	r.settings.SMTPSecretConfigured = len(r.secret.Ciphertext) > 0
	r.settings.Revision++
	r.settings.UpdatedBy = actor.ID
	r.settings.UpdatedAt = time.Now().UTC()
	value := r.settings
	value.AdminCIDRs = append([]string(nil), value.AdminCIDRs...)
	return value, nil
}
func (r *MemoryRepository) StoreSMTPSecret(_ context.Context, envelope SecretEnvelope, expected int64, actor Actor) (Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.configured {
		return Settings{}, ErrNotConfigured
	}
	if r.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	r.secret = SecretEnvelope{Ciphertext: append([]byte(nil), envelope.Ciphertext...), KeyVersion: envelope.KeyVersion}
	r.settings.Revision++
	r.settings.UpdatedBy = actor.ID
	r.settings.UpdatedAt = time.Now().UTC()
	r.settings.SMTPSecretConfigured = true
	return r.settings, nil
}
func (r *MemoryRepository) ClearSMTPSecret(_ context.Context, expected int64, actor Actor) (Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.configured {
		return Settings{}, ErrNotConfigured
	}
	if r.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	r.secret = SecretEnvelope{}
	r.settings.Revision++
	r.settings.UpdatedBy = actor.ID
	r.settings.UpdatedAt = time.Now().UTC()
	r.settings.SMTPSecretConfigured = false
	return r.settings, nil
}
func (r *MemoryRepository) LoadSMTPSecret(context.Context) (SecretEnvelope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.secret.Ciphertext) == 0 {
		return SecretEnvelope{}, ErrSecretMissing
	}
	return SecretEnvelope{Ciphertext: append([]byte(nil), r.secret.Ciphertext...), KeyVersion: r.secret.KeyVersion}, nil
}
