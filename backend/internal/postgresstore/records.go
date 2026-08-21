package postgresstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"invoice-system/backend/internal/domain"
)

// AuditActor is supplied by the authenticated edge.  Only hashes of state are
// written to audit_events; encrypted invoice/profile contents never enter the
// audit log.
type AuditActor struct {
	Type         string
	ID           string
	RequestID    string
	SourceIPHMAC string
	Reason       string
}

func (a AuditActor) normalized() AuditActor {
	if strings.TrimSpace(a.Type) == "" {
		a.Type = "system"
	}
	if strings.TrimSpace(a.ID) == "" {
		a.ID = "system"
	}
	if strings.TrimSpace(a.RequestID) == "" {
		a.RequestID = randomUUID()
	}
	return a
}

type SourceInstanceRecord struct {
	ID                     string
	SourceType             domain.SourceType
	Name                   string
	RuntimeVersion         string
	RuntimeVersionRevision int64
	Enabled                bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type UserRecord struct {
	ID              string
	OIDCIssuer      string
	OIDCSubject     string
	Status          string
	EmailCiphertext []byte
	EmailVerified   bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type VerifiedEmailRecord struct {
	ID                  string
	PrincipalID         string
	EmailCiphertext     []byte
	NormalizedEmailHMAC string
	VerificationSource  string
	VerifiedAt          time.Time
	RevokedAt           time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ExternalAccountRecord struct {
	ID                  string
	PrincipalID         string
	SourceInstanceID    string
	ExternalUserID      string
	ExternalSubjectHMAC string
	BindingMethod       string
	BindingStatus       string
	VerifiedAt          time.Time
	SourceObservedAt    time.Time
	SourceSequence      int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ConnectedSourceAccount struct {
	ID                   string
	SourceInstanceID     string
	SourceType           domain.SourceType
	SourceName           string
	MaskedExternalUserID string
	BindingStatus        string
	VerifiedAt           time.Time
	LastObservedAt       time.Time
}

type SourceObservation struct {
	Lot               domain.FundingLot
	ExternalUserID    string
	EventKind         string
	ExternalEventID   string
	SchemaVersion     string
	PayloadCiphertext []byte
	SourceCreatedAt   time.Time
	SourceUpdatedAt   time.Time
	SourceSequence    int64
	// NewAPIManualCeilingMinor is set only by the administrator evidence flow.
	// It atomically lowers the maximum amount any later dual review may restore.
	NewAPIManualCeilingMinor *int64
	// EligibilityKind and CashServiceUnits are supplied only by the signed v3
	// source contract. Missing/unknown values are fail-closed and the payment is
	// retained as LEGACY_NON_INVOICEABLE rather than inferred from money fields.
	EligibilityKind     domain.EligibilityKind
	CashServiceUnits    string
	WalletUnitCode      string
	CutoverManifestHash string
	ConfigurationHash   string
	CausalDomain        string
	CausalOrder         string
	StreamWatermarkAt   time.Time
	SourceCursor        string
	BatchID             string
	ScanCycleID         string
}

type ObservationResult struct {
	Lot                   domain.FundingLot
	Duplicate             bool
	AttentionRequestIDs   []string
	InvalidatedRequestIDs []string
}

type EligibilityWatermark struct {
	StreamKind     string
	WatermarkAt    time.Time
	SourceSequence int64
	SourceCursor   string
}

type CutoverManifest struct {
	SourceInstanceID, ManifestHash, SourceRuntimeVersion, ProjectionContract, ConfigurationHash string
	UnitCode, PaymentsCeiling, UsageCeiling, CreditsCeiling, BalancesCeiling                    string
	BaselineSnapshotID, BaselineSnapshotHash                                                    string
	BaselineRowCount                                                                            int64
	SigningKeyID                                                                                string
	CutoverAt, DatabaseClock                                                                    time.Time
	StreamWatermarkAt                                                                           time.Time
	ExternalEventID, BatchID, ScanCycleID, SourceRevision                                       string
}

type UsageObservation struct {
	SourceInstanceID, ExternalUserID, ExternalEventID, ExternalUsageID string
	EventTime, ObservedAt, StreamWatermarkAt                           time.Time
	ServiceUnits, UnitCode, BillingScope                               string
	CausalDomain, CausalOrder, SourceCursor, SourceRevision            string
	CutoverManifestHash, ConfigurationHash                             string
	SourceSequence                                                     int64
	BatchID, ScanCycleID                                               string
}

type CreditObservation struct {
	SourceInstanceID, ExternalUserID, ExternalEventID, ExternalCreditID string
	EventTime, ObservedAt, StreamWatermarkAt                            time.Time
	ServiceUnits, UnitCode, CreditKind                                  string
	CausalDomain, CausalOrder, SourceCursor, SourceRevision             string
	CutoverManifestHash, ConfigurationHash                              string
	SourceSequence                                                      int64
	BatchID, ScanCycleID                                                string
}

type BalanceCheckpointObservation struct {
	SourceInstanceID, ExternalUserID, ExternalEventID, CheckpointID string
	CheckpointKind, BalanceServiceUnits, UnitCode                   string
	BaselineSnapshotID                                              string
	SourceSnapshotID                                                string
	SnapshotRowCount                                                string
	AsOf, ObservedAt, StreamWatermarkAt                             time.Time
	SourceCursor, SourceRevision                                    string
	CutoverManifestHash, ConfigurationHash                          string
	SourceSequence                                                  int64
	Watermarks                                                      []EligibilityWatermark
	BalanceNegative                                                 bool
	BaselineMember                                                  bool
	CatchupKeyHMAC                                                  string
	BatchID, ScanCycleID                                            string
}

type SubscriptionObservation struct {
	SourceInstanceID, ExternalUserID, ExternalEventID, ExternalOrderID string
	CompletedAt, ObservedAt, StreamWatermarkAt                         time.Time
	PaidMinor, Currency, VerificationState                             string
	Refunded                                                           bool
	CausalDomain, CausalOrder, SourceCursor, SourceRevision            string
	SourceSequence                                                     int64
	BatchID, ScanCycleID                                               string
}

type PaymentCandidatePageQuery struct {
	Limit            int
	BeforeObservedAt time.Time
	BeforeID         string
	States           []domain.VerificationState
}

type PaymentCandidatePage struct {
	Items                []domain.FundingLot
	HasMore              bool
	NextBeforeObservedAt time.Time
	NextBeforeID         string
}

type PaymentEvidence struct {
	Hash       string
	Ciphertext []byte
}

type ReviewPaymentCandidateInput struct {
	LotID            string
	Action           string
	PaidMinor        int64
	Currency         string
	Evidence         PaymentEvidence
	ReasonCiphertext []byte
	ReasonHash       string
	Actor            AuditActor
}

type RequestRecord struct {
	Request                   domain.InvoiceRequest
	ProfileID                 string
	IdempotencyKey            string
	ProfileSnapshotCiphertext []byte
	IssuerSettingRevision     int64
	IssueSnapshotCiphertext   []byte
}

type RequestPageQuery struct {
	PrincipalID       string
	Admin             bool
	Limit             int
	BeforeSubmittedAt time.Time
	BeforeID          string
	Statuses          []domain.RequestStatus
	SourceInstanceID  string
}

type RequestPage struct {
	Items                 []RequestRecord
	HasMore               bool
	NextBeforeSubmittedAt time.Time
	NextBeforeID          string
}

type AttachDocumentInput struct {
	Document        domain.InvoiceDocument
	ExpectedVersion int64
	RecipientHash   string
	TemplateVersion string
	Actor           AuditActor
}

type ConfirmIssueInput struct {
	AdminID                 string
	RequestID               string
	ExpectedVersion         int64
	IssuerSettingRevision   int64
	IssueSnapshotCiphertext []byte
	Freshness               SourceFreshnessPolicy
	Actor                   AuditActor
}

type OutboxClaim struct {
	ClaimID                   string
	OutboxID                  string
	RequestID                 string
	RequestNo                 string
	PrincipalID               string
	IdempotencyKey            string
	ProfileSnapshotCiphertext []byte
	CreatedAt                 time.Time
}

type InvoiceDeliveryState struct {
	Document domain.InvoiceDocument
	Outbox   domain.EmailOutbox
}

type RefundCase struct {
	ID                     string
	RequestID              string
	FundingLotID           string
	SourceRevision         string
	ObservedRefundMinor    int64
	IssuedExposureMinor    int64
	ObservedCapMinor       int64
	Status                 string
	ResolutionEvidenceHash string
	ResolutionNoteHash     string
	ResolvedBy             string
	OpenedAt               time.Time
	UpdatedAt              time.Time
	ResolvedAt             time.Time
}

type RefundCasePageQuery struct {
	Limit          int
	Status         string
	BeforeOpenedAt time.Time
	BeforeID       string
}

type RefundCasePage struct {
	Items              []RefundCase
	HasMore            bool
	NextBeforeOpenedAt time.Time
	NextBeforeID       string
}

type ResolveRefundCaseInput struct {
	CaseID             string
	ResolutionStatus   string
	EvidenceHash       string
	EvidenceCiphertext []byte
	NoteCiphertext     []byte
	NoteHash           string
	Actor              AuditActor
}

type RefundCaseResolutionRecord struct {
	Case               RefundCase
	EvidenceCiphertext []byte
	NoteCiphertext     []byte
}

func stateHash(value any) string {
	if value == nil {
		return ""
	}
	body, err := json.Marshal(value)
	if err != nil {
		body = []byte("unavailable")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
