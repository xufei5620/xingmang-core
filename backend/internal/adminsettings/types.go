package adminsettings

import (
	"context"
	"errors"
	"time"
)

const (
	FixedServiceItem = "技术服务"
	MinimumMinor     = int64(20_000)
)

var (
	ErrNotConfigured    = errors.New("admin settings are not configured")
	ErrRevisionConflict = errors.New("admin settings revision conflict")
	ErrInvalidSettings  = errors.New("invalid admin settings")
	ErrSecretMissing    = errors.New("SMTP secret is not configured")
)

type Settings struct {
	// IssuerName is admin-only configuration. User-facing APIs must not expose it.
	IssuerName           string    `json:"issuer_name"`
	ServiceItem          string    `json:"service_item"`
	MinimumRequestMinor  int64     `json:"minimum_request_minor"`
	SMTPHost             string    `json:"smtp_host"`
	SMTPPort             int       `json:"smtp_port"`
	SMTPFrom             string    `json:"smtp_from"`
	SMTPFromName         string    `json:"smtp_from_name"`
	SMTPStartTLS         bool      `json:"smtp_starttls"`
	SMTPSecretConfigured bool      `json:"credential_configured"`
	AdminCIDRs           []string  `json:"admin_cidrs"`
	Revision             int64     `json:"revision"`
	UpdatedBy            string    `json:"updated_by"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type UpdateInput struct {
	IssuerName          string
	MinimumRequestMinor int64
	SMTPHost            string
	SMTPPort            int
	SMTPFrom            string
	SMTPFromName        string
	SMTPStartTLS        bool
	AdminCIDRs          []string
}

type Actor struct {
	ID           string
	RequestID    string
	SourceIPHash string
	Reason       string
}

type SecretEnvelope struct {
	Ciphertext []byte
	KeyVersion string
}

type SMTPSecretChange string

const (
	SMTPSecretUnchanged SMTPSecretChange = "unchanged"
	SMTPSecretSet       SMTPSecretChange = "set"
	SMTPSecretClear     SMTPSecretChange = "clear"
)

type Repository interface {
	Get(context.Context) (Settings, error)
	Update(context.Context, UpdateInput, int64, Actor) (Settings, error)
	UpdateSMTP(context.Context, UpdateInput, SMTPSecretChange, SecretEnvelope, int64, Actor) (Settings, error)
	StoreSMTPSecret(context.Context, SecretEnvelope, int64, Actor) (Settings, error)
	ClearSMTPSecret(context.Context, int64, Actor) (Settings, error)
	LoadSMTPSecret(context.Context) (SecretEnvelope, error)
}

type SecretBox interface {
	Seal(context.Context, []byte) (SecretEnvelope, error)
	Open(context.Context, SecretEnvelope) ([]byte, error)
}
