package sourceagent

import (
	"context"
	"encoding/json"
	"errors"
)

const (
	SchemaVersionV1 = "1.0"
	SchemaVersionV2 = "2.0"
	SchemaVersionV3 = "3.0"

	SourceSub2API = "sub2api"
	SourceNewAPI  = "newapi"
	SourceDesktop = "desktop"

	EntityPaymentOrder         = "payment_order"
	EntityPaymentCandidate     = "payment_candidate"
	EntityPaymentAdjustment    = "payment_adjustment"
	EntityIdentityBinding      = "identity_binding"
	EntityUsageDaily           = "usage_daily"
	EntityUsageEvent           = "usage_event"
	EntityCreditEvent          = "credit_event"
	EntityBalanceCheckpoint    = "balance_checkpoint"
	EntitySubscriptionPurchase = "subscription_purchase"
	EntityCutoverManifest      = "cutover_manifest"
	EntityTombstone            = "tombstone"

	StreamPayments   = "payments"
	StreamIdentities = "identities"
	StreamUsage      = "usage"
	StreamCredits    = "credits"
	StreamBalances   = "balances"
)

var (
	ErrBodyHashMismatch      = errors.New("source-agent: body hash mismatch")
	ErrSourceMismatch        = errors.New("source-agent: source mismatch")
	ErrReplay                = errors.New("source-agent: replay rejected")
	ErrOutOfOrder            = errors.New("source-agent: sequence out of order")
	ErrPreviousHashMismatch  = errors.New("source-agent: previous batch hash mismatch")
	ErrPayloadHashMismatch   = errors.New("source-agent: payload hash mismatch")
	ErrInvalidBatch          = errors.New("source-agent: invalid batch")
	ErrOutboundDisabled      = errors.New("source-agent: outbound ingestion disabled")
	ErrUnsafeOrigin          = errors.New("source-agent: unsafe upstream origin")
	ErrCredentialUnavailable = errors.New("source-agent: credential unavailable")
	ErrContractUnavailable   = errors.New("source-agent: required upstream contract unavailable")
)

// Batch is the versioned outbound source-agent envelope. The source identity
// is still derived from mTLS by the receiver; this field is not authoritative.
type Batch struct {
	SchemaVersion    string `json:"schema_version"`
	SourceInstanceID string `json:"source_instance_id"`
	// StreamID is mandatory in schema 2.0 and deliberately omitted from the
	// legacy schema 1.0 mock envelope.  Sequence/hash chains are scoped to the
	// exact (source_instance_id, stream_id) pair.
	StreamID             string  `json:"stream_id,omitempty"`
	SourceType           string  `json:"source_type"`
	SourceRuntimeVersion string  `json:"source_runtime_version"`
	AgentVersion         string  `json:"agent_version"`
	BatchID              string  `json:"batch_id"`
	Sequence             uint64  `json:"sequence"`
	PreviousBatchHash    *string `json:"previous_batch_hash"`
	CapturedAt           string  `json:"captured_at"`
	Mode                 string  `json:"mode"`
	ProjectionStatus     string  `json:"projection_status"`
	// V3 watermarks are source facts, not wall-clock heartbeats.  They advance
	// only after a bounded scan through the matching ceiling has completed.
	// Empty batches carry them as well, so an actually empty stream can make
	// auditable progress without inventing a record.
	StreamWatermarkAt    string   `json:"stream_watermark_at,omitempty"`
	SourceCursor         string   `json:"source_cursor,omitempty"`
	ScanCeilingAt        string   `json:"scan_ceiling_at,omitempty"`
	ScanCeilingCursor    string   `json:"scan_ceiling_cursor,omitempty"`
	ScanCycleID          string   `json:"scan_cycle_id,omitempty"`
	ScanComplete         bool     `json:"scan_complete,omitempty"`
	ScanSnapshotID       string   `json:"scan_snapshot_id,omitempty"`
	ScanSnapshotRowCount *int64   `json:"scan_snapshot_row_count,omitempty"`
	Records              []Record `json:"records"`
}

func (b Batch) MarshalJSON() ([]byte, error) {
	if b.SchemaVersion == SchemaVersionV3 {
		return json.Marshal(struct {
			SchemaVersion        string   `json:"schema_version"`
			SourceInstanceID     string   `json:"source_instance_id"`
			StreamID             string   `json:"stream_id"`
			SourceType           string   `json:"source_type"`
			SourceRuntimeVersion string   `json:"source_runtime_version"`
			AgentVersion         string   `json:"agent_version"`
			BatchID              string   `json:"batch_id"`
			Sequence             uint64   `json:"sequence"`
			PreviousBatchHash    *string  `json:"previous_batch_hash"`
			CapturedAt           string   `json:"captured_at"`
			Mode                 string   `json:"mode"`
			ProjectionStatus     string   `json:"projection_status"`
			StreamWatermarkAt    string   `json:"stream_watermark_at"`
			SourceCursor         string   `json:"source_cursor"`
			ScanCeilingAt        string   `json:"scan_ceiling_at"`
			ScanCeilingCursor    string   `json:"scan_ceiling_cursor"`
			ScanCycleID          string   `json:"scan_cycle_id"`
			ScanComplete         bool     `json:"scan_complete"`
			ScanSnapshotID       string   `json:"scan_snapshot_id,omitempty"`
			ScanSnapshotRowCount *int64   `json:"scan_snapshot_row_count,omitempty"`
			Records              []Record `json:"records"`
		}{b.SchemaVersion, b.SourceInstanceID, b.StreamID, b.SourceType, b.SourceRuntimeVersion, b.AgentVersion, b.BatchID, b.Sequence, b.PreviousBatchHash, b.CapturedAt, b.Mode, b.ProjectionStatus, b.StreamWatermarkAt, b.SourceCursor, b.ScanCeilingAt, b.ScanCeilingCursor, b.ScanCycleID, b.ScanComplete, b.ScanSnapshotID, b.ScanSnapshotRowCount, b.Records})
	}
	if b.SchemaVersion != SchemaVersionV1 {
		type batchAlias Batch
		return json.Marshal(batchAlias(b))
	}
	// V1 is an offline/mock compatibility contract and must not acquire the V2
	// projection health field.
	return json.Marshal(struct {
		SchemaVersion        string   `json:"schema_version"`
		SourceInstanceID     string   `json:"source_instance_id"`
		SourceType           string   `json:"source_type"`
		SourceRuntimeVersion string   `json:"source_runtime_version"`
		AgentVersion         string   `json:"agent_version"`
		BatchID              string   `json:"batch_id"`
		Sequence             uint64   `json:"sequence"`
		PreviousBatchHash    *string  `json:"previous_batch_hash"`
		CapturedAt           string   `json:"captured_at"`
		Mode                 string   `json:"mode"`
		Records              []Record `json:"records"`
	}{b.SchemaVersion, b.SourceInstanceID, b.SourceType, b.SourceRuntimeVersion,
		b.AgentVersion, b.BatchID, b.Sequence, b.PreviousBatchHash, b.CapturedAt, b.Mode, b.Records})
}

type Record struct {
	EventID       string          `json:"event_id"`
	Operation     string          `json:"operation"`
	ObservedAt    string          `json:"observed_at"`
	PayloadSHA256 string          `json:"payload_sha256"`
	EntityType    string          `json:"entity_type"`
	Payload       json.RawMessage `json:"payload"`
}

// PaymentOrderPayload preserves both Sub2API's business-unit amounts and the
// actual gateway-money amounts. Amount and RefundAmount are denominated in the
// source product/balance unit and MUST NOT be subtracted from PayAmount.
// GatewayRefundAmount is the cumulative money returned by the gateway,
// calculated as pay_amount * refund_amount / amount with currency rounding.
type PaymentOrderPayload struct {
	ExternalOrderID        string  `json:"external_order_id"`
	ExternalUserID         string  `json:"external_user_id"`
	Status                 string  `json:"status"`
	OrderType              string  `json:"order_type"`
	Amount                 string  `json:"amount"`
	PayAmount              string  `json:"pay_amount"`
	Currency               string  `json:"currency"`
	RefundAmount           string  `json:"refund_amount"`
	GatewayRefundAmount    string  `json:"gateway_refund_amount"`
	CompletedAt            *string `json:"completed_at,omitempty"`
	RefundAt               *string `json:"refund_at,omitempty"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
	PaymentType            string  `json:"payment_type,omitempty"`
	ProviderKey            string  `json:"provider_key,omitempty"`
	ExternalTradeRefHMAC   string  `json:"external_trade_ref_hmac,omitempty"`
	WalletCashServiceUnits *string `json:"wallet_cash_service_units,omitempty"`
	WalletUnitCode         *string `json:"wallet_unit_code,omitempty"`
	FactMetadata
}

// PaymentCandidatePayload is deliberately not an invoice entitlement. It is
// used for sources such as New API whose read-only TopUp DTO cannot prove the
// settlement currency, distinguish an administrator's manual completion from
// provider settlement, or report refunds. The invoice service may surface the
// candidate for manual reconciliation but must never auto-claim it.
type PaymentCandidatePayload struct {
	ExternalOrderID        string  `json:"external_order_id"`
	ExternalUserID         string  `json:"external_user_id"`
	SourceStatus           string  `json:"source_status"`
	OrderType              string  `json:"order_type"`
	QuotedAmount           string  `json:"quoted_amount"`
	ObservedPayAmount      string  `json:"observed_pay_amount"`
	Currency               *string `json:"currency,omitempty"`
	VerificationState      string  `json:"verification_state"`
	VerificationReason     string  `json:"verification_reason"`
	CompletedAt            *string `json:"completed_at,omitempty"`
	CreatedAt              string  `json:"created_at"`
	ObservedAt             string  `json:"observed_at"`
	PaymentType            string  `json:"payment_type,omitempty"`
	ProviderKey            string  `json:"provider_key,omitempty"`
	WalletCashServiceUnits *string `json:"wallet_cash_service_units,omitempty"`
	WalletUnitCode         *string `json:"wallet_unit_code,omitempty"`
	FactMetadata
}

// PaymentAdjustmentPayload represents the absolute cumulative gateway-money
// refund at the referenced source version. Amount is already prorated from
// Sub2API's business-unit refund and rounded to Currency's minor unit. It is
// an observed fact, never an instruction to mutate the upstream.
type PaymentAdjustmentPayload struct {
	ExternalOrderID string  `json:"external_order_id"`
	ExternalUserID  string  `json:"external_user_id"`
	AdjustmentType  string  `json:"adjustment_type"`
	Amount          string  `json:"amount"`
	Currency        string  `json:"currency"`
	EffectiveAt     *string `json:"effective_at,omitempty"`
	SourceStatus    string  `json:"source_status"`
	SourceUpdatedAt string  `json:"source_updated_at"`
	Basis           string  `json:"basis"`
	FactMetadata
}

// FactMetadata is repeated in every V3 economic fact. SourceCursor is stable
// within CausalDomain; cursors from different domains are deliberately not
// comparable. The receiver must freeze an equal-timestamp cross-domain tie
// instead of silently choosing a precedence.
type FactMetadata struct {
	SourceCursor        string  `json:"source_cursor,omitempty"`
	CausalDomain        string  `json:"causal_domain,omitempty"`
	CausalOrder         *string `json:"causal_order,omitempty"`
	CutoverManifestHash string  `json:"cutover_manifest_hash,omitempty"`
	ConfigurationHash   string  `json:"configuration_hash,omitempty"`
}

type CutoverManifestPayload struct {
	SourceInstanceID     string `json:"source_instance_id"`
	ManifestHash         string `json:"manifest_hash"`
	CutoverAt            string `json:"cutover_at"`
	DatabaseClock        string `json:"database_clock"`
	SourceRuntimeVersion string `json:"source_runtime_version"`
	ConfigurationHash    string `json:"configuration_hash"`
	ProjectionContract   string `json:"projection_contract"`
	UnitCode             string `json:"unit_code"`
	PaymentsCeiling      string `json:"payments_ceiling"`
	UsageCeiling         string `json:"usage_ceiling"`
	CreditsCeiling       string `json:"credits_ceiling"`
	BalancesCeiling      string `json:"balances_ceiling"`
	BaselineSnapshotHash string `json:"baseline_snapshot_hash"`
	BaselineRowCount     string `json:"baseline_row_count"`
	SigningKeyID         string `json:"signing_key_id"`
}

type UsageEventPayload struct {
	ExternalUserID  string `json:"external_user_id"`
	ExternalUsageID string `json:"external_usage_id"`
	OccurredAt      string `json:"occurred_at"`
	ServiceUnits    string `json:"service_units"`
	UnitCode        string `json:"unit_code"`
	BillingScope    string `json:"billing_scope"`
	FactMetadata
}

type CreditEventPayload struct {
	ExternalUserID   string `json:"external_user_id"`
	ExternalCreditID string `json:"external_credit_id"`
	OccurredAt       string `json:"occurred_at"`
	ServiceUnits     string `json:"service_units"`
	UnitCode         string `json:"unit_code"`
	CreditKind       string `json:"credit_kind"`
	FactMetadata
}

type BalanceCheckpointPayload struct {
	ExternalUserID      string `json:"external_user_id"`
	CheckpointID        string `json:"checkpoint_id"`
	CheckpointKind      string `json:"checkpoint_kind"`
	AsOf                string `json:"as_of"`
	BalanceServiceUnits string `json:"balance_service_units"`
	UnitCode            string `json:"unit_code"`
	SourceSnapshotID    string `json:"source_snapshot_id"`
	SnapshotRowCount    string `json:"snapshot_row_count"`
	BalanceNegative     bool   `json:"balance_negative"`
	// DeficitServiceUnits (XM-INV-NEGATIVE-DEFICIT) is the magnitude of a
	// negative upstream balance in service units, "0" when the balance is
	// not negative. Absent only in evidence sealed before the bridge learned
	// to report it; the receiver stores that as "unknown", never as zero.
	DeficitServiceUnits string `json:"deficit_service_units,omitempty"`
	BaselineMember      bool   `json:"baseline_member"`
	FactMetadata
}

type SubscriptionPurchasePayload struct {
	ExternalUserID    string `json:"external_user_id"`
	ExternalOrderID   string `json:"external_order_id"`
	CompletedAt       string `json:"completed_at"`
	PaidMinor         string `json:"paid_minor"`
	Currency          string `json:"currency"`
	VerificationState string `json:"verification_state"`
	Refunded          bool   `json:"refunded"`
	FactMetadata
}

type IdentityBindingPayload struct {
	ExternalUserID  string  `json:"external_user_id"`
	ProviderType    string  `json:"provider_type"`
	ProviderKey     string  `json:"provider_key"`
	ProviderSubject string  `json:"provider_subject"`
	Issuer          string  `json:"issuer,omitempty"`
	VerifiedAt      *string `json:"verified_at"`
	UserStatus      string  `json:"user_status"`
	DeletedAt       *string `json:"deleted_at"`
	UpdatedAt       string  `json:"updated_at"`
}

type UsageDailyPayload struct {
	ExternalUserID  string `json:"external_user_id"`
	UsageDate       string `json:"usage_date"`
	TotalRequests   uint64 `json:"total_requests"`
	TotalTokens     uint64 `json:"total_tokens"`
	TotalCost       string `json:"total_cost"`
	TotalActualCost string `json:"total_actual_cost"`
	CostCurrency    string `json:"cost_currency"`
}

type TombstonePayload struct {
	ExternalID  string `json:"external_id"`
	Reason      string `json:"reason"`
	ConfirmedAt string `json:"confirmed_at"`
}

// Sub2APIOrderProjection and NewAPITopupProjection are adapter-side minimal
// inputs. They intentionally contain no email, IP, notes, API key or raw
// provider metadata. A New API top-up cannot become invoice entitlement until
// PaymentVerified is true.
type Sub2APIOrderProjection struct {
	ID           int64
	UserID       int64
	Status       string
	OrderType    string
	Amount       string
	PayAmount    string
	Currency     string
	RefundAmount string
	CompletedAt  *string
	RefundAt     *string
	CreatedAt    string
	UpdatedAt    string
}

type NewAPITopupProjection struct {
	TradeNumber       string
	UserID            int64
	Status            string
	QuotedAmount      string
	PaymentVerified   bool
	VerifiedPayAmount string
	VerifiedCurrency  string
	CreatedAt         string
	UpdatedAt         string
}

type ScanMode string

const (
	ScanFull        ScanMode = "full"
	ScanIncremental ScanMode = "incremental"
	ScanReconcile   ScanMode = "reconcile"
)

// ScanCursor is source-owned opaque progress. DB projections use
// (updated_at,id); API fallbacks use page/boundary fields and must periodically
// perform a complete rescan because offset pagination cannot prove absence.
type ScanCursor struct {
	Revision            uint64 `json:"-"`
	Version             int    `json:"version"`
	UpdatedAt           string `json:"updated_at,omitempty"`
	ID                  int64  `json:"id,omitempty"`
	Page                int    `json:"page,omitempty"`
	BoundaryID          int64  `json:"boundary_id,omitempty"`
	Completed           bool   `json:"completed,omitempty"`
	Domain              string `json:"domain,omitempty"`
	CutoverAt           string `json:"cutover_at,omitempty"`
	WatermarkAt         string `json:"watermark_at,omitempty"`
	WatermarkCursor     string `json:"watermark_cursor,omitempty"`
	CeilingAt           string `json:"ceiling_at,omitempty"`
	CeilingCursor       string `json:"ceiling_cursor,omitempty"`
	PositionCursor      string `json:"position_cursor,omitempty"`
	SnapshotID          string `json:"snapshot_id,omitempty"`
	SnapshotRowCount    int64  `json:"snapshot_row_count,omitempty"`
	HasSnapshotMetadata bool   `json:"has_snapshot_metadata,omitempty"`
	ScanCycleID         string `json:"scan_cycle_id,omitempty"`
	ProjectionBlocked   bool   `json:"projection_blocked,omitempty"`
	// ReconcileBaselineCursor and ReconcileWindowBounded implement the XM-INV-AGENT-RESTART-GRACE
	// rolling reconcile window (usage stream only; see economics_db.go prepareCursor).
	// ReconcileBaselineCursor is the domain cursor position where the last
	// completed ScanReconcile cycle finished; the next reconcile starts there
	// instead of rewinding to the cutover manifest. Empty means no reconcile
	// has completed under this logic yet (the reconcile then starts from the
	// live WatermarkCursor -- no rewind).
	ReconcileBaselineCursor string `json:"reconcile_baseline_cursor,omitempty"`
	// ReconcileWindowBounded is true once the currently in-flight (or last
	// started) reconcile cycle's starting position was computed by the
	// bounded rolling-window logic. A stored cursor from before this field
	// existed decodes it as false, which lets prepareCursor recognize and
	// abandon a legacy in-flight reconcile cycle (started by an older agent
	// binary that always rewound to the cutover manifest) instead of
	// resuming it: see shouldAbandonLegacyReconcileCycle.
	ReconcileWindowBounded bool `json:"reconcile_window_bounded,omitempty"`
}

type ScanRequest struct {
	Mode     ScanMode
	Cursor   ScanCursor
	Limit    int
	WatchIDs []int64
}

type Projection struct {
	EntityType string
	ExternalID string
	ObservedAt string
	Operation  string
	Payload    any
}

type ScanPage struct {
	Projections []Projection
	NextCursor  ScanCursor
	HasMore     bool
	// ReconcileBlocked means the source can prove that some rows were hidden
	// by a safety filter (for example an unprovable currency). Such a scan may
	// refresh visible rows but must never count absence or emit tombstones.
	ReconcileBlocked     bool
	Warnings             []string
	StreamWatermarkAt    string
	SourceCursor         string
	ScanCeilingAt        string
	ScanCeilingCursor    string
	ScanCycleID          string
	ScanComplete         bool
	ScanSnapshotID       string
	ScanSnapshotRowCount *int64
}

// Connector performs source reads only. Implementations must not expose a
// mutation method and must select only the documented projection columns.
type Connector interface {
	SourceType() string
	Scan(context.Context, ScanRequest) (ScanPage, error)
}

type ValidatedBatch struct {
	Batch     Batch
	RawBody   []byte
	BodyHash  string
	Duplicate bool
}

type IngestAck struct {
	Accepted         bool   `json:"accepted"`
	SourceInstanceID string `json:"source_instance_id"`
	StreamID         string `json:"stream_id"`
	BatchID          string `json:"batch_id"`
	Sequence         uint64 `json:"sequence"`
	AcceptedRecords  int    `json:"accepted_records"`
	Duplicate        bool   `json:"duplicate"`
}

// IngestClient is outbound-only. Implementations send a validated batch to an
// ingestion service. No inbound HTTP server is part of this package.
type IngestClient interface {
	Send(ctx context.Context, batch ValidatedBatch) (IngestAck, error)
}

type DisabledIngestClient struct{}

func (DisabledIngestClient) Send(context.Context, ValidatedBatch) (IngestAck, error) {
	return IngestAck{}, ErrOutboundDisabled
}
