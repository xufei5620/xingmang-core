package adminsettings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryRepo struct {
	settings Settings
	secret   SecretEnvelope
	has      bool
}

func (m *memoryRepo) Get(context.Context) (Settings, error) {
	if m.settings.Revision == 0 {
		return Settings{}, ErrNotConfigured
	}
	s := m.settings
	s.SMTPSecretConfigured = m.has
	return s, nil
}
func (m *memoryRepo) Update(_ context.Context, in UpdateInput, expected int64, a Actor) (Settings, error) {
	if m.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	m.settings = Settings{IssuerName: in.IssuerName, ServiceItem: FixedServiceItem, MinimumRequestMinor: in.MinimumRequestMinor, EligibilityStartAt: in.EligibilityStartAt, EligibilityPolicyVersion: 1, SMTPHost: in.SMTPHost, SMTPPort: in.SMTPPort, SMTPFrom: in.SMTPFrom, SMTPFromName: in.SMTPFromName, SMTPStartTLS: in.SMTPStartTLS, AdminCIDRs: in.AdminCIDRs, Revision: expected + 1, UpdatedBy: a.ID}
	return m.settings, nil
}
func (m *memoryRepo) UpdateSMTP(_ context.Context, in UpdateInput, change SMTPSecretChange, e SecretEnvelope, expected int64, a Actor) (Settings, error) {
	if m.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	switch change {
	case SMTPSecretUnchanged:
	case SMTPSecretSet:
		m.secret, m.has = e, true
	case SMTPSecretClear:
		m.secret, m.has = SecretEnvelope{}, false
	default:
		return Settings{}, ErrInvalidSettings
	}
	m.settings.SMTPHost, m.settings.SMTPPort, m.settings.SMTPFrom = in.SMTPHost, in.SMTPPort, in.SMTPFrom
	m.settings.SMTPFromName, m.settings.SMTPStartTLS = in.SMTPFromName, in.SMTPStartTLS
	m.settings.SMTPSecretConfigured = m.has
	m.settings.Revision++
	m.settings.UpdatedBy = a.ID
	return m.settings, nil
}
func (m *memoryRepo) StoreSMTPSecret(_ context.Context, e SecretEnvelope, expected int64, _ Actor) (Settings, error) {
	if m.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	m.secret = e
	m.has = true
	m.settings.Revision++
	m.settings.SMTPSecretConfigured = true
	return m.settings, nil
}
func (m *memoryRepo) ClearSMTPSecret(_ context.Context, expected int64, _ Actor) (Settings, error) {
	if m.settings.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	m.has = false
	m.settings.Revision++
	m.settings.SMTPSecretConfigured = false
	return m.settings, nil
}
func (m *memoryRepo) LoadSMTPSecret(context.Context) (SecretEnvelope, error) {
	if !m.has {
		return SecretEnvelope{}, ErrSecretMissing
	}
	return m.secret, nil
}

type testBox struct{}

func (testBox) Seal(_ context.Context, p []byte) (SecretEnvelope, error) {
	return SecretEnvelope{Ciphertext: append([]byte("sealed:"), p...), KeyVersion: "v1"}, nil
}
func (testBox) Open(_ context.Context, e SecretEnvelope) ([]byte, error) {
	if len(e.Ciphertext) < 7 {
		return nil, errors.New("bad")
	}
	return e.Ciphertext[7:], nil
}

type failingSealBox struct{}

func (failingSealBox) Seal(context.Context, []byte) (SecretEnvelope, error) {
	return SecretEnvelope{}, errors.New("key service unavailable")
}
func (failingSealBox) Open(context.Context, SecretEnvelope) ([]byte, error) {
	return nil, errors.New("not used")
}
func validInput() UpdateInput {
	return UpdateInput{IssuerName: "开票主体", MinimumRequestMinor: 20_000, EligibilityStartAt: RequiredEligibilityStartAt, SMTPHost: "smtp.qq.com", SMTPPort: 587, SMTPFrom: "invoice@qq.com", SMTPFromName: "发票中心", SMTPStartTLS: true, AdminCIDRs: []string{"203.0.113.8/32"}}
}
func TestServiceValidationAndSecretNonDisclosure(t *testing.T) {
	repo := &memoryRepo{}
	service := NewService(repo, testBox{})
	settings, err := service.Update(context.Background(), validInput(), 0, Actor{ID: "admin", RequestID: "req"})
	if err != nil {
		t.Fatal(err)
	}
	if settings.ServiceItem != FixedServiceItem {
		t.Fatal(settings.ServiceItem)
	}
	bad := validInput()
	bad.MinimumRequestMinor = 19_999
	if _, err = service.Update(context.Background(), bad, 1, Actor{ID: "admin", RequestID: "req2"}); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("minimum got %v", err)
	}
	settings, err = service.SetSMTPSecret(context.Background(), "authorization-code", 1, Actor{ID: "admin", RequestID: "req3"})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.SMTPSecretConfigured {
		t.Fatal("secret flag false")
	}
	got, err := service.Get(context.Background())
	if err != nil || !got.SMTPSecretConfigured {
		t.Fatal(err)
	}
	plain, err := service.SMTPSecretForDelivery(context.Background())
	if err != nil || plain != "authorization-code" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
}

func TestEligibilityStartUsesExactShanghaiBoundaryAndCannotBeChangedBySettings(t *testing.T) {
	parsed, err := time.Parse(time.RFC3339, EligibilityStartAtRFC3339)
	if err != nil || !parsed.UTC().Equal(RequiredEligibilityStartAt) ||
		parsed.UTC().Format(time.RFC3339) != "2026-08-31T16:00:00Z" {
		t.Fatalf("eligibility boundary parsed=%s err=%v", parsed, err)
	}
	for _, changed := range []time.Time{
		RequiredEligibilityStartAt.Add(-time.Microsecond),
		RequiredEligibilityStartAt.Add(time.Microsecond),
	} {
		input := validInput()
		input.EligibilityStartAt = changed
		if _, err = normalize(input); !errors.Is(err, ErrInvalidSettings) {
			t.Fatalf("changed eligibility boundary %s accepted: %v", changed, err)
		}
	}
}

func TestSMTPAllowlistSupportsQQAndGmailOnly(t *testing.T) {
	for _, host := range []string{"smtp.qq.com", "smtp.exmail.qq.com", "smtp.gmail.com"} {
		input := validInput()
		input.SMTPHost = host
		input.SMTPFrom = "invoice@gmail.com"
		if _, err := normalize(input); err != nil {
			t.Fatalf("host=%s err=%v", host, err)
		}
	}
	for _, host := range []string{"127.0.0.1", "metadata.google.internal", "smtp.example.com", "smtp.gmail.com.evil.invalid"} {
		input := validInput()
		input.SMTPHost = host
		if _, err := normalize(input); !errors.Is(err, ErrInvalidSettings) {
			t.Fatalf("unsafe host=%s err=%v", host, err)
		}
	}
	input := validInput()
	input.SMTPHost = "smtp.gmail.com"
	input.SMTPPort = 465
	if _, err := normalize(input); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("implicit TLS port accepted: %v", err)
	}
}
func TestCIDRNormalizationAndCAS(t *testing.T) {
	repo := &memoryRepo{}
	service := NewService(repo, testBox{})
	in := validInput()
	in.AdminCIDRs = []string{"203.0.113.9/24", "203.0.113.0/24"}
	got, err := service.Update(context.Background(), in, 0, Actor{ID: "admin", RequestID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AdminCIDRs) != 1 || got.AdminCIDRs[0] != "203.0.113.0/24" {
		t.Fatalf("CIDRs=%v", got.AdminCIDRs)
	}
	if _, err = service.Update(context.Background(), in, 0, Actor{ID: "admin", RequestID: "stale"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("CAS got %v", err)
	}
}

func TestCIDRRejectsWorldOpenAndOverlyBroadNetworks(t *testing.T) {
	service := NewService(&memoryRepo{}, testBox{})
	for _, cidr := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/8", "2001:db8::/32", "224.0.0.0/24", "169.254.0.0/24"} {
		in := validInput()
		in.AdminCIDRs = []string{cidr}
		if _, err := service.Update(context.Background(), in, 0, Actor{ID: "admin", RequestID: "unsafe"}); !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("CIDR %s accepted: %v", cidr, err)
		}
	}
	for _, cidr := range []string{"127.0.0.1/32", "203.0.113.8/32", "2001:db8::1/128", "::1/128", "203.0.113.8", "2001:db8::1"} {
		in := validInput()
		in.AdminCIDRs = []string{cidr}
		if _, err := normalize(in); err != nil {
			t.Errorf("CIDR %s rejected: %v", cidr, err)
		}
	}
	in := validInput()
	in.AdminCIDRs = []string{"203.0.113.8", "2001:db8::1"}
	normalized, err := normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.AdminCIDRs[0] != "2001:db8::1/128" || normalized.AdminCIDRs[1] != "203.0.113.8/32" {
		t.Fatalf("bare IP normalization=%v", normalized.AdminCIDRs)
	}
}

func TestSMTPPortAndDisplayNameLimits(t *testing.T) {
	service := NewService(&memoryRepo{}, testBox{})
	badPort := validInput()
	badPort.SMTPPort = 465
	if _, err := service.Update(context.Background(), badPort, 0, Actor{ID: "admin", RequestID: "port"}); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("port got %v", err)
	}
	longName := validInput()
	longName.SMTPFromName = strings.Repeat("名", 129)
	if _, err := service.Update(context.Background(), longName, 0, Actor{ID: "admin", RequestID: "name"}); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("name got %v", err)
	}
	longIssuer := validInput()
	longIssuer.IssuerName = strings.Repeat("企", 201)
	if _, err := service.Update(context.Background(), longIssuer, 0, Actor{ID: "admin", RequestID: "issuer"}); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("issuer got %v", err)
	}
}

func TestUpdateSMTPIsOneRevisionAndEncryptionFailureChangesNothing(t *testing.T) {
	now := time.Now().UTC()
	initial := Settings{IssuerName: "开票主体", ServiceItem: FixedServiceItem, MinimumRequestMinor: MinimumMinor, EligibilityStartAt: RequiredEligibilityStartAt, SMTPHost: "smtp.qq.com", SMTPPort: 587, SMTPFrom: "old@qq.com", SMTPFromName: "旧名称", SMTPStartTLS: true, AdminCIDRs: []string{"203.0.113.8/32"}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	repo := NewMemoryRepository(initial)
	input := validInput()
	input.SMTPFrom = "new@qq.com"
	failed := NewService(repo, failingSealBox{})
	if _, err := failed.UpdateSMTP(context.Background(), input, SMTPSecretSet, "secret", 1, Actor{ID: "admin", RequestID: "failed"}); err == nil {
		t.Fatal("encryption failure accepted")
	}
	unchanged, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 || unchanged.SMTPFrom != "old@qq.com" || unchanged.SMTPSecretConfigured {
		t.Fatalf("state changed after encryption failure: %+v", unchanged)
	}
	service := NewService(repo, testBox{})
	updated, err := service.UpdateSMTP(context.Background(), input, SMTPSecretSet, "secret", 1, Actor{ID: "admin", RequestID: "success"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.SMTPFrom != "new@qq.com" || !updated.SMTPSecretConfigured {
		t.Fatalf("atomic update=%+v", updated)
	}
}
