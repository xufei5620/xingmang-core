package application

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/securefields"
)

func TestEligibilityFreezeResolutionAADEncryptsEvidenceAndNote(t *testing.T) {
	keys := testKeys()
	freezeID := "61000000-0000-4000-8000-000000000001"
	hash := strings.Repeat("a", 64)
	evidence := []byte("ticket://private-evidence")
	note := []byte("private resolution note")
	evidenceCiphertext, err := keys.Encrypt(evidence, eligibilityFreezeEvidenceAAD(freezeID, hash))
	if err != nil {
		t.Fatal(err)
	}
	noteCiphertext, err := keys.Encrypt(note, eligibilityFreezeNoteAAD(freezeID, hash))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(evidenceCiphertext, evidence) || bytes.Contains(noteCiphertext, note) {
		t.Fatal("freeze resolution plaintext appeared in ciphertext")
	}
	if plaintext, err := keys.Decrypt(evidenceCiphertext, eligibilityFreezeEvidenceAAD(freezeID, hash)); err != nil || !bytes.Equal(plaintext, evidence) {
		t.Fatal("freeze evidence AAD round trip failed")
	}
	if _, err = keys.Decrypt(evidenceCiphertext, eligibilityFreezeEvidenceAAD(freezeID, strings.Repeat("b", 64))); err == nil {
		t.Fatal("freeze evidence decrypted under another hash")
	}
}

type fixedSettings struct{ value adminsettings.Settings }

func (s *fixedSettings) Get(context.Context) (adminsettings.Settings, error) { return s.value, nil }

func testKeys() securefields.Keyring {
	return securefields.Keyring{
		CurrentKeyID:   "test-v1",
		EncryptionKeys: map[string][]byte{"test-v1": bytes.Repeat([]byte{0x31}, 32)},
		IndexKey:       bytes.Repeat([]byte{0x52}, 32),
	}
}

func TestNewServiceFailsClosedOnUnsafeConfiguration(t *testing.T) {
	store := postgresstore.New(nil)
	settings := &fixedSettings{value: adminsettings.Settings{}}
	if _, err := NewService(store, testKeys(), settings, Options{DownloadBaseURL: "http://invoice.example"}); err == nil {
		t.Fatal("accepted non-HTTPS download URL")
	}
	if _, err := NewService(store, testKeys(), settings, Options{DownloadBaseURL: "https://user:secret@invoice.example"}); err == nil {
		t.Fatal("accepted credentials in download URL")
	}
	if _, err := NewService(store, testKeys(), settings, Options{PublicBaseURL: "https://invoice.example/base"}); err == nil {
		t.Fatal("accepted a path in the public origin")
	}
	if _, err := NewService(store, testKeys(), settings, Options{DownloadBaseURL: "https://invoice.example", MinimumRequestMinor: domain.MinimumRequestMinor - 1}); !errors.Is(err, domain.ErrMinimumAmount) {
		t.Fatalf("minimum error=%v", err)
	}
}

func TestConfirmManualIssueFailsClosedForEveryIssuerPlaceholderVariant(t *testing.T) {
	for _, issuer := range []string{"", "待配置开票主体", " 待配置实际开票主体（上线前必须修改） ", "请替换为实际开票主体全称"} {
		service := &Service{settings: &fixedSettings{value: adminsettings.Settings{
			IssuerName: issuer, ServiceItem: domain.FixedServiceItem, Revision: 1,
		}}}
		if _, err := service.ConfirmManualIssue(context.Background(), "admin", "request", 1); !errors.Is(err, ErrIssuerNotConfigured) {
			t.Errorf("issuer %q confirmation error=%v", issuer, err)
		}
	}
}

func TestValidateIssueSnapshotRejectsHistoricalPlaceholderIssuer(t *testing.T) {
	policyStart := time.Date(2026, time.August, 31, 16, 0, 0, 0, time.UTC)
	settings := adminsettings.Settings{EligibilityStartAt: policyStart, EligibilityPolicyVersion: 1}
	for _, issuer := range []string{"待配置开票主体", "待配置实际开票主体（上线前必须修改）", "请替换为实际开票主体全称"} {
		snapshot := IssueSnapshot{IssuerName: issuer, ServiceItem: domain.FixedServiceItem, SettingsRevision: 1, EligibilityStartAt: policyStart, EligibilityPolicyVersion: 1}
		if err := validateIssueSnapshot(snapshot, 1, settings); !errors.Is(err, ErrIssuerNotConfigured) {
			t.Errorf("historical issuer %q validation error=%v", issuer, err)
		}
	}
	valid := IssueSnapshot{IssuerName: "示例科技有限公司", ServiceItem: domain.FixedServiceItem, SettingsRevision: 1, EligibilityStartAt: policyStart, EligibilityPolicyVersion: 1}
	if err := validateIssueSnapshot(valid, 1, settings); err != nil {
		t.Fatalf("real historical issuer rejected: %v", err)
	}
}

func TestCanonicalEmailRejectsDisplayName(t *testing.T) {
	if _, err := canonicalEmail("User <user@example.com>"); err == nil {
		t.Fatal("display-name mailbox must not be accepted as normalized identity")
	}
	got, err := canonicalEmail("USER@Example.COM")
	if err != nil || got != "user@example.com" {
		t.Fatalf("canonical email=%q err=%v", got, err)
	}
}
