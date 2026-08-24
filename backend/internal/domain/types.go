package domain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const (
	CurrencyCNY               = "CNY"
	MinimumRequestMinor int64 = 20_000
	FixedServiceItem          = "技术服务"
)

type SourceType string

const (
	SourceSub2API SourceType = "sub2api"
	SourceNewAPI  SourceType = "newapi"
)

func (s SourceType) Valid() bool { return s == SourceSub2API || s == SourceNewAPI }

type VerificationState string

const (
	VerificationPending  VerificationState = "pending"
	VerificationVerified VerificationState = "verified"
	VerificationFrozen   VerificationState = "frozen"
)

type EligibilityKind string

const (
	EligibilityLegacyNonInvoiceable EligibilityKind = "LEGACY_NON_INVOICEABLE"
	EligibilityWalletCash           EligibilityKind = "WALLET_CASH"
	EligibilitySubscriptionCash     EligibilityKind = "SUBSCRIPTION_CASH"
	EligibilityNonCash              EligibilityKind = "NON_CASH"
)

func (k EligibilityKind) Valid() bool {
	return k == EligibilityLegacyNonInvoiceable || k == EligibilityWalletCash ||
		k == EligibilitySubscriptionCash || k == EligibilityNonCash
}

type ProfileType string

const (
	ProfilePersonal   ProfileType = "personal"
	ProfileEnterprise ProfileType = "enterprise"
)

type RequestStatus string

const (
	StatusPendingReview          RequestStatus = "pending_review"
	StatusNeedsChanges           RequestStatus = "needs_changes"
	StatusApproved               RequestStatus = "approved"
	StatusRejected               RequestStatus = "rejected"
	StatusUserCancelled          RequestStatus = "user_cancelled"
	StatusManualIssuing          RequestStatus = "manual_issuing"
	StatusIssuedAwaitingDocument RequestStatus = "issued_awaiting_document"
	StatusIssued                 RequestStatus = "issued"
	StatusRefundAttention        RequestStatus = "refund_attention"
)

type FundingLot struct {
	ID                   string            `json:"id"`
	PrincipalID          string            `json:"principal_id"`
	SourceInstanceID     string            `json:"source_instance_id"`
	SourceType           SourceType        `json:"source_type"`
	ExternalOrderID      string            `json:"external_order_id"`
	TradeNo              string            `json:"trade_no"`
	Currency             string            `json:"currency"`
	OriginalMinor        int64             `json:"original_minor"`
	CurrentCapMinor      int64             `json:"current_cap_minor"`
	ReservedMinor        int64             `json:"reserved_minor"`
	IssuedMinor          int64             `json:"issued_minor"`
	VerifiedCashMinor    int64             `json:"verified_cash_minor"`
	ConsumedCashMinor    int64             `json:"consumed_cash_minor"`
	EligibilityKind      EligibilityKind   `json:"eligibility_kind"`
	EligibilityCutoverAt time.Time         `json:"eligibility_cutover_at,omitempty"`
	RefundFrozen         bool              `json:"refund_frozen"`
	EligibilityRevision  int64             `json:"eligibility_revision"`
	EligibilityStatus    string            `json:"eligibility_status"`
	Verification         VerificationState `json:"verification"`
	SourceStatus         string            `json:"source_status"`
	SourceRevision       string            `json:"source_revision"`
	CompletedAt          time.Time         `json:"completed_at"`
	ObservedAt           time.Time         `json:"observed_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	ManualReviewStage    string            `json:"manual_review_stage,omitempty"`
}

func (l FundingLot) AvailableMinor() int64 {
	if l.EligibilityKind == EligibilitySubscriptionCash {
		// Current signed source contracts expose wallet usage only. Subscription
		// payments remain auditable but cannot become invoiceable without an
		// authoritative purchase-to-usage linkage.
		return 0
	}
	cap := l.ConsumedCashMinor
	// Empty eligibility kind exists only in the in-memory/bootstrap fixture
	// service retained for unit tests. Persistent production rows always carry
	// an explicit post-cutover kind.
	if l.EligibilityKind == "" {
		cap = l.CurrentCapMinor
	}
	if l.EligibilityKind == EligibilityLegacyNonInvoiceable ||
		l.EligibilityKind == EligibilityNonCash || l.RefundFrozen ||
		l.Verification != VerificationVerified ||
		(l.EligibilityKind != "" && l.EligibilityStatus != "active") {
		return 0
	}
	remaining := cap - l.ReservedMinor - l.IssuedMinor
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (l FundingLot) Validate() error {
	if l.ID == "" || l.PrincipalID == "" || l.SourceInstanceID == "" || l.ExternalOrderID == "" {
		return errors.New("funding lot identity fields are required")
	}
	if !l.SourceType.Valid() {
		return errors.New("unsupported source type")
	}
	if l.Currency != CurrencyCNY {
		return errors.New("V1 only supports CNY")
	}
	if l.OriginalMinor < 0 || l.CurrentCapMinor < 0 || l.ReservedMinor < 0 || l.IssuedMinor < 0 {
		return errors.New("funding lot amounts cannot be negative")
	}
	if l.VerifiedCashMinor < 0 || l.VerifiedCashMinor > l.OriginalMinor ||
		l.ConsumedCashMinor < 0 || l.ConsumedCashMinor > l.VerifiedCashMinor {
		return errors.New("consumed cash is outside the verified payment bound")
	}
	if l.CurrentCapMinor > l.OriginalMinor {
		return errors.New("current cap cannot exceed original amount")
	}
	if l.IssuedMinor > l.OriginalMinor || l.ReservedMinor > l.OriginalMinor-l.IssuedMinor {
		return errors.New("allocated amount cannot exceed original amount")
	}
	if l.EligibilityKind != "" {
		if !l.EligibilityKind.Valid() {
			return errors.New("unsupported invoice eligibility kind")
		}
		if l.ReservedMinor+l.IssuedMinor > l.ConsumedCashMinor {
			return errors.New("allocated amount cannot exceed consumed cash")
		}
		if l.EligibilityKind == EligibilityLegacyNonInvoiceable && !l.EligibilityCutoverAt.IsZero() {
			return errors.New("legacy lot cannot carry a cutover timestamp")
		}
		if l.EligibilityKind != EligibilityLegacyNonInvoiceable && l.EligibilityCutoverAt.IsZero() {
			return errors.New("post-cutover lot requires a cutover timestamp")
		}
	}
	// Compatibility for the in-memory fixture service: it predates the
	// persistent cutover fields and never crosses a production boundary.
	if l.EligibilityKind == "" && l.VerifiedCashMinor == 0 && l.ConsumedCashMinor == 0 {
		return nil
	}
	return nil
}

type InvoiceProfile struct {
	ID            string      `json:"id"`
	PrincipalID   string      `json:"principal_id"`
	Type          ProfileType `json:"type"`
	Title         string      `json:"title"`
	TaxID         string      `json:"tax_id,omitempty"`
	Email         string      `json:"email"`
	EmailVerified bool        `json:"email_verified"`
	Address       string      `json:"address,omitempty"`
	Phone         string      `json:"phone,omitempty"`
	BankName      string      `json:"bank_name,omitempty"`
	BankAccount   string      `json:"bank_account,omitempty"`
	IsDefault     bool        `json:"is_default"`
	Revision      int64       `json:"revision"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

func (p InvoiceProfile) Validate() error {
	if p.PrincipalID == "" || strings.TrimSpace(p.Title) == "" {
		return errors.New("profile owner and title are required")
	}
	if p.Type != ProfilePersonal && p.Type != ProfileEnterprise {
		return errors.New("unsupported profile type")
	}
	if p.Type == ProfileEnterprise && strings.TrimSpace(p.TaxID) == "" {
		return errors.New("enterprise tax ID is required")
	}
	if !p.EmailVerified {
		return errors.New("delivery email must be verified")
	}
	fields := []struct {
		name  string
		value string
		max   int
	}{
		{"title", p.Title, 200}, {"tax ID", p.TaxID, 64}, {"email", p.Email, 320},
		{"address", p.Address, 500}, {"phone", p.Phone, 64},
		{"bank name", p.BankName, 200}, {"bank account", p.BankAccount, 128},
	}
	for _, field := range fields {
		if len(field.value) > field.max {
			return fmt.Errorf("profile %s is too long", field.name)
		}
		if strings.ContainsAny(field.value, "\r\n\x00") {
			return fmt.Errorf("profile %s contains forbidden control characters", field.name)
		}
	}
	emailValue := strings.TrimSpace(p.Email)
	address, err := mail.ParseAddress(emailValue)
	if err != nil || address.Address != emailValue {
		return errors.New("delivery email is invalid")
	}
	return nil
}

type ProfileSnapshot struct {
	Type        ProfileType `json:"type"`
	Title       string      `json:"title"`
	TaxID       string      `json:"tax_id,omitempty"`
	Email       string      `json:"email"`
	Address     string      `json:"address,omitempty"`
	Phone       string      `json:"phone,omitempty"`
	BankName    string      `json:"bank_name,omitempty"`
	BankAccount string      `json:"bank_account,omitempty"`
	Revision    int64       `json:"revision"`
}

func SnapshotProfile(p InvoiceProfile) ProfileSnapshot {
	return ProfileSnapshot{
		Type: p.Type, Title: p.Title, TaxID: p.TaxID, Email: p.Email,
		Address: p.Address, Phone: p.Phone, BankName: p.BankName,
		BankAccount: p.BankAccount, Revision: p.Revision,
	}
}

type Allocation struct {
	FundingLotID  string `json:"funding_lot_id"`
	ExternalOrder string `json:"external_order_id"`
	AmountMinor   int64  `json:"amount_minor"`
}

type InvoiceRequest struct {
	ID               string          `json:"id"`
	RequestNo        string          `json:"request_no"`
	PrincipalID      string          `json:"principal_id"`
	SourceInstanceID string          `json:"source_instance_id"`
	SourceType       SourceType      `json:"source_type"`
	Currency         string          `json:"currency"`
	IssuerCode       string          `json:"issuer_code"`
	ServiceItem      string          `json:"service_item"`
	AmountMinor      int64           `json:"amount_minor"`
	Status           RequestStatus   `json:"status"`
	Profile          ProfileSnapshot `json:"profile"`
	Allocations      []Allocation    `json:"allocations"`
	ReviewNote       string          `json:"review_note,omitempty"`
	ReviewedBy       string          `json:"reviewed_by,omitempty"`
	IssuedBy         string          `json:"issued_by,omitempty"`
	Version          int64           `json:"version"`
	SubmittedAt      time.Time       `json:"submitted_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type InvoiceDocument struct {
	ID            string    `json:"id"`
	RequestID     string    `json:"request_id"`
	InvoiceNumber string    `json:"invoice_number"`
	ObjectKey     string    `json:"object_key"`
	ObjectVersion string    `json:"object_version"`
	SHA256        string    `json:"sha256"`
	SizeBytes     int64     `json:"size_bytes"`
	MIME          string    `json:"mime"`
	ScanStatus    string    `json:"scan_status"`
	UploadedBy    string    `json:"uploaded_by"`
	IssuedAt      time.Time `json:"issued_at"`
	CreatedAt     time.Time `json:"created_at"`
}

func (d InvoiceDocument) Validate() error {
	if strings.TrimSpace(d.RequestID) == "" || strings.TrimSpace(d.InvoiceNumber) == "" ||
		strings.TrimSpace(d.ObjectKey) == "" || strings.TrimSpace(d.ObjectVersion) == "" || d.SHA256 == "" {
		return errors.New("document identity fields are required")
	}
	if len(d.InvoiceNumber) > 128 || len(d.ObjectKey) > 1024 || len(d.ObjectVersion) > 256 {
		return errors.New("document identity field is too long")
	}
	if strings.ContainsAny(d.InvoiceNumber, "\r\n\x00") || strings.ContainsAny(d.ObjectKey, "\r\n\x00") || strings.ContainsAny(d.ObjectVersion, "\r\n\x00") {
		return errors.New("document identity field contains forbidden control characters")
	}
	if len(d.SHA256) != 64 {
		return errors.New("document SHA-256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(d.SHA256); err != nil {
		return errors.New("document SHA-256 is invalid")
	}
	if d.MIME != "application/pdf" {
		return errors.New("V1 only accepts PDF documents")
	}
	if d.SizeBytes <= 0 {
		return errors.New("document is empty")
	}
	if d.ScanStatus != "clean" {
		return errors.New("document has not passed security scanning")
	}
	if d.IssuedAt.IsZero() {
		return errors.New("invoice issue time is required")
	}
	return nil
}

type EmailOutbox struct {
	ID              string    `json:"id"`
	RequestID       string    `json:"request_id"`
	DocumentID      string    `json:"document_id"`
	RecipientHash   string    `json:"recipient_hash"`
	TemplateVersion string    `json:"template_version"`
	Status          string    `json:"status"`
	Attempts        int       `json:"attempts"`
	NextAttemptAt   time.Time `json:"next_attempt_at"`
	CreatedAt       time.Time `json:"created_at"`
}

var (
	ErrNotFound           = errors.New("not found")
	ErrForbidden          = errors.New("forbidden")
	ErrConflict           = errors.New("conflict")
	ErrInsufficientAmount = errors.New("insufficient available amount")
	ErrMinimumAmount      = fmt.Errorf("minimum invoice amount is %d minor units", MinimumRequestMinor)
	ErrSourceMixing       = errors.New("allocations from different source instances cannot be combined")
	ErrUnverifiedPayment  = errors.New("payment is not verified")
	ErrSourceUnavailable  = errors.New("source synchronization is unavailable or stale")
	ErrInvalidState       = errors.New("invalid request state transition")
	ErrVersionConflict    = errors.New("request version conflict")
)
