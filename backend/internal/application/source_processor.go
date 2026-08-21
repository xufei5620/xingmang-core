package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

type SourceEventProcessor struct {
	Service   *Service
	BatchSize int
	Now       func() time.Time
}

type sourceDependencyWait struct {
	Kind    string
	KeyHMAC string
}

// Exact HMAC wakeups are the normal recovery path. This long fallback prevents
// thousands of not-yet-migrated accounts from rewriting their queue rows every
// few minutes while still recovering from a missed wakeup without operator
// intervention.
const sourceDependencyFallbackRetry = 12 * time.Hour

func (e *sourceDependencyWait) Error() string {
	return "source projection dependency is not available yet"
}

func (p SourceEventProcessor) RunOnce(ctx context.Context) (int, error) {
	if p.Service == nil {
		return 0, errors.New("source event processor is not configured")
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	claims, err := p.Service.store.ClaimUnprocessedSourceEvents(ctx, p.BatchSize, now())
	if err != nil {
		return 0, fmt.Errorf("claim source events: %w", err)
	}
	processed := 0
	for _, claim := range claims {
		processErr := p.Service.ProcessSourceEvent(ctx, claim)
		if processErr != nil {
			var dependency *sourceDependencyWait
			if errors.As(processErr, &dependency) {
				if err = p.Service.store.MarkSourceEventWaitingDependency(ctx, claim,
					dependency.Kind, dependency.KeyHMAC, now().Add(sourceDependencyFallbackRetry)); err != nil {
					return processed, fmt.Errorf("mark source dependency wait: %w", err)
				}
				processed++
				continue
			}
			if err = p.Service.store.MarkSourceEventFailed(ctx, claim, "PROJECTION_FAILED", now().Add(5*time.Minute)); err != nil {
				return processed, fmt.Errorf("mark source event failed: %w", err)
			}
			processed++
			continue
		}
		if err = p.Service.store.MarkSourceEventProcessed(ctx, claim, now()); err != nil {
			return processed, fmt.Errorf("mark source event processed: %w", err)
		}
		processed++
	}
	return processed, nil
}

func (s *Service) dependencyKey(kind, sourceID, value string) (string, error) {
	return s.keys.BlindIndex("source-dependency/"+kind, sourceID+"\n"+value)
}

func (s *Service) waitForDependency(kind, sourceID, value string) error {
	key, err := s.dependencyKey(kind, sourceID, value)
	if err != nil {
		return err
	}
	return &sourceDependencyWait{Kind: kind, KeyHMAC: key}
}

func (s *Service) wakeDependency(ctx context.Context, kind, sourceID, value string) error {
	key, err := s.dependencyKey(kind, sourceID, value)
	if err != nil {
		return err
	}
	_, err = s.store.RequeueSourceDependency(ctx, kind, key)
	return err
}

func (s *Service) SourceIngestHealth(ctx context.Context) (postgresstore.SourceIngestHealth, error) {
	return s.store.SourceIngestHealth(ctx)
}

func (s *Service) SourceHealth(ctx context.Context) (postgresstore.SourceHealthReport, error) {
	return s.store.SourceHealth(ctx, s.sourceFreshnessPolicy())
}

func (s *Service) ProcessSourceEvent(ctx context.Context, claim postgresstore.SourceEventClaim) error {
	plaintext, err := s.keys.Decrypt(claim.PayloadCiphertext,
		ingestEventAAD(claim.SourceInstanceID, claim.StreamID, claim.EventID, claim.PayloadHash))
	if err != nil {
		return fmt.Errorf("decrypt source event: %w", err)
	}
	if claim.Operation == "tombstone" {
		return s.processSourceTombstone(ctx, claim, plaintext)
	}
	if claim.Operation != "upsert" {
		return errors.New("unsupported source operation")
	}
	switch claim.EntityType {
	case "identity_binding":
		return s.processIdentityBinding(ctx, claim, plaintext)
	case "payment_order":
		return s.processPaymentOrder(ctx, claim, plaintext)
	case "payment_candidate":
		return s.processPaymentCandidate(ctx, claim, plaintext)
	case "payment_adjustment":
		return s.processPaymentAdjustment(ctx, claim, plaintext)
	case "usage_daily":
		// Usage is durably retained for future reporting but does not grant or
		// reduce invoice entitlement in V1.
		return nil
	case "cutover_manifest":
		return s.processCutoverManifest(ctx, claim, plaintext)
	case "usage_event":
		return s.processUsageEvent(ctx, claim, plaintext)
	case "credit_event":
		return s.processCreditEvent(ctx, claim, plaintext)
	case "balance_checkpoint":
		return s.processBalanceCheckpoint(ctx, claim, plaintext)
	case "subscription_purchase":
		return s.processSubscriptionPurchase(ctx, claim, plaintext)
	default:
		return errors.New("unsupported source entity type")
	}
}

type identityBindingPayload struct {
	ExternalUserID  string  `json:"external_user_id"`
	ProviderType    string  `json:"provider_type"`
	ProviderKey     string  `json:"provider_key"`
	ProviderSubject string  `json:"provider_subject"`
	Issuer          string  `json:"issuer"`
	VerifiedAt      *string `json:"verified_at"`
	UserStatus      string  `json:"user_status"`
	DeletedAt       *string `json:"deleted_at"`
	UpdatedAt       string  `json:"updated_at"`
}

type paymentOrderPayload struct {
	ExternalOrderID        string  `json:"external_order_id"`
	ExternalUserID         string  `json:"external_user_id"`
	Status                 string  `json:"status"`
	OrderType              string  `json:"order_type"`
	Amount                 string  `json:"amount"`
	PayAmount              string  `json:"pay_amount"`
	Currency               string  `json:"currency"`
	RefundAmount           string  `json:"refund_amount"`
	GatewayRefundAmount    string  `json:"gateway_refund_amount"`
	CompletedAt            *string `json:"completed_at"`
	RefundAt               *string `json:"refund_at"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
	PaymentType            string  `json:"payment_type"`
	ProviderKey            string  `json:"provider_key"`
	ExternalTradeRefHMAC   string  `json:"external_trade_ref_hmac"`
	WalletCashServiceUnits *string `json:"wallet_cash_service_units"`
	WalletUnitCode         *string `json:"wallet_unit_code"`
	SourceCursor           string  `json:"source_cursor"`
	CausalDomain           string  `json:"causal_domain"`
	CausalOrder            *string `json:"causal_order"`
	CutoverManifestHash    string  `json:"cutover_manifest_hash"`
	ConfigurationHash      string  `json:"configuration_hash"`
}

type paymentCandidatePayload struct {
	ExternalOrderID        string  `json:"external_order_id"`
	ExternalUserID         string  `json:"external_user_id"`
	SourceStatus           string  `json:"source_status"`
	OrderType              string  `json:"order_type"`
	QuotedAmount           string  `json:"quoted_amount"`
	ObservedPayAmount      string  `json:"observed_pay_amount"`
	Currency               *string `json:"currency"`
	VerificationState      string  `json:"verification_state"`
	VerificationReason     string  `json:"verification_reason"`
	CompletedAt            *string `json:"completed_at"`
	CreatedAt              string  `json:"created_at"`
	ObservedAt             string  `json:"observed_at"`
	PaymentType            string  `json:"payment_type"`
	ProviderKey            string  `json:"provider_key"`
	WalletCashServiceUnits *string `json:"wallet_cash_service_units"`
	WalletUnitCode         *string `json:"wallet_unit_code"`
	SourceCursor           string  `json:"source_cursor"`
	CausalDomain           string  `json:"causal_domain"`
	CausalOrder            *string `json:"causal_order"`
	CutoverManifestHash    string  `json:"cutover_manifest_hash"`
	ConfigurationHash      string  `json:"configuration_hash"`
}

type paymentAdjustmentPayload struct {
	ExternalOrderID     string  `json:"external_order_id"`
	ExternalUserID      string  `json:"external_user_id"`
	AdjustmentType      string  `json:"adjustment_type"`
	Amount              string  `json:"amount"`
	Currency            string  `json:"currency"`
	EffectiveAt         *string `json:"effective_at"`
	SourceStatus        string  `json:"source_status"`
	SourceUpdatedAt     string  `json:"source_updated_at"`
	Basis               string  `json:"basis"`
	SourceCursor        string  `json:"source_cursor"`
	CausalDomain        string  `json:"causal_domain"`
	CausalOrder         *string `json:"causal_order"`
	CutoverManifestHash string  `json:"cutover_manifest_hash"`
	ConfigurationHash   string  `json:"configuration_hash"`
}

type cutoverManifestPayload struct {
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

type usageEventPayload struct {
	ExternalUserID      string  `json:"external_user_id"`
	ExternalUsageID     string  `json:"external_usage_id"`
	OccurredAt          string  `json:"occurred_at"`
	ServiceUnits        string  `json:"service_units"`
	UnitCode            string  `json:"unit_code"`
	BillingScope        string  `json:"billing_scope"`
	SourceCursor        string  `json:"source_cursor"`
	CausalDomain        string  `json:"causal_domain"`
	CausalOrder         *string `json:"causal_order"`
	CutoverManifestHash string  `json:"cutover_manifest_hash"`
	ConfigurationHash   string  `json:"configuration_hash"`
}

type creditEventPayload struct {
	ExternalUserID      string  `json:"external_user_id"`
	ExternalCreditID    string  `json:"external_credit_id"`
	OccurredAt          string  `json:"occurred_at"`
	ServiceUnits        string  `json:"service_units"`
	UnitCode            string  `json:"unit_code"`
	CreditKind          string  `json:"credit_kind"`
	SourceCursor        string  `json:"source_cursor"`
	CausalDomain        string  `json:"causal_domain"`
	CausalOrder         *string `json:"causal_order"`
	CutoverManifestHash string  `json:"cutover_manifest_hash"`
	ConfigurationHash   string  `json:"configuration_hash"`
}

type balanceCheckpointPayload struct {
	ExternalUserID      string  `json:"external_user_id"`
	CheckpointID        string  `json:"checkpoint_id"`
	CheckpointKind      string  `json:"checkpoint_kind"`
	AsOf                string  `json:"as_of"`
	BalanceServiceUnits string  `json:"balance_service_units"`
	UnitCode            string  `json:"unit_code"`
	SourceSnapshotID    string  `json:"source_snapshot_id"`
	SnapshotRowCount    string  `json:"snapshot_row_count"`
	BalanceNegative     bool    `json:"balance_negative"`
	BaselineMember      *bool   `json:"baseline_member"`
	SourceCursor        string  `json:"source_cursor"`
	CausalDomain        string  `json:"causal_domain"`
	CausalOrder         *string `json:"causal_order"`
	CutoverManifestHash string  `json:"cutover_manifest_hash"`
	ConfigurationHash   string  `json:"configuration_hash"`
}

type subscriptionPurchasePayload struct {
	ExternalUserID      string  `json:"external_user_id"`
	ExternalOrderID     string  `json:"external_order_id"`
	CompletedAt         string  `json:"completed_at"`
	PaidMinor           string  `json:"paid_minor"`
	Currency            string  `json:"currency"`
	VerificationState   string  `json:"verification_state"`
	Refunded            bool    `json:"refunded"`
	SourceCursor        string  `json:"source_cursor"`
	CausalDomain        string  `json:"causal_domain"`
	CausalOrder         *string `json:"causal_order"`
	CutoverManifestHash string  `json:"cutover_manifest_hash"`
	ConfigurationHash   string  `json:"configuration_hash"`
}

type tombstonePayload struct {
	ExternalID  string `json:"external_id"`
	Reason      string `json:"reason"`
	ConfirmedAt string `json:"confirmed_at"`
}

func strictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func cnyMinor(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "+eE") {
		return 0, errors.New("invalid CNY decimal")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("invalid CNY decimal")
	}
	if len(parts) == 2 {
		parts[1] = strings.TrimRight(parts[1], "0")
		if len(parts[1]) > 2 {
			return 0, errors.New("CNY decimal has sub-cent precision")
		}
	}
	integer, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || integer > math.MaxInt64/100 {
		return 0, errors.New("CNY decimal is outside int64 range")
	}
	fraction := int64(0)
	if len(parts) == 2 && parts[1] != "" {
		fractionText := parts[1]
		if len(fractionText) == 1 {
			fractionText += "0"
		}
		fraction, err = strconv.ParseInt(fractionText, 10, 64)
		if err != nil {
			return 0, errors.New("invalid CNY fraction")
		}
	}
	if integer == math.MaxInt64/100 && fraction > math.MaxInt64%100 {
		return 0, errors.New("CNY decimal is outside int64 range")
	}
	return integer*100 + fraction, nil
}

func parseOptionalTime(value *string) (time.Time, error) {
	if value == nil {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	return parsed.UTC(), err
}

func causalOrderValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Service) processCutoverManifest(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SchemaVersion != "3.0" || claim.StreamID != "balances" {
		return errors.New("cutover manifest is restricted to the v3 balances stream")
	}
	var payload cutoverManifestPayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	claimedManifestHash := payload.ManifestHash
	if payload.SigningKeyID != claim.SigningKeyID {
		return errors.New("cutover manifest signing key does not match authenticated batch")
	}
	payload.ManifestHash = ""
	canonicalManifest, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	manifestSum := sha256.Sum256(canonicalManifest)
	if hex.EncodeToString(manifestSum[:]) != claimedManifestHash {
		return errors.New("cutover manifest canonical content hash mismatch")
	}
	payload.ManifestHash = claimedManifestHash
	cutoverAt, err := time.Parse(time.RFC3339Nano, payload.CutoverAt)
	if err != nil {
		return err
	}
	databaseClock, err := time.Parse(time.RFC3339Nano, payload.DatabaseClock)
	if err != nil {
		return err
	}
	rowCount, err := strconv.ParseInt(payload.BaselineRowCount, 10, 64)
	if err != nil || rowCount < 0 || rowCount > 2_000_000 || payload.SourceInstanceID != claim.SourceInstanceID ||
		payload.SourceRuntimeVersion == "" || payload.ManifestHash == "" || payload.ConfigurationHash == "" {
		return errors.New("invalid signed cutover manifest")
	}
	err = s.store.RegisterCutoverManifest(ctx, postgresstore.CutoverManifest{
		SourceInstanceID: claim.SourceInstanceID, ManifestHash: payload.ManifestHash,
		SourceRuntimeVersion: payload.SourceRuntimeVersion, ProjectionContract: payload.ProjectionContract,
		ConfigurationHash: payload.ConfigurationHash, UnitCode: payload.UnitCode,
		PaymentsCeiling: payload.PaymentsCeiling, UsageCeiling: payload.UsageCeiling,
		CreditsCeiling: payload.CreditsCeiling, BalancesCeiling: payload.BalancesCeiling,
		BaselineSnapshotID: payload.BaselineSnapshotHash, BaselineSnapshotHash: payload.BaselineSnapshotHash,
		BaselineRowCount: rowCount, SigningKeyID: claim.SigningKeyID,
		CutoverAt: cutoverAt.UTC(), DatabaseClock: databaseClock.UTC(), StreamWatermarkAt: claim.ScanCeilingAt,
		ExternalEventID: claim.EventID, BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID,
		SourceRevision: claim.PayloadHash,
	}, auditActor(ctx, "source_connector", claim.SourceInstanceID, "verified v3 cutover manifest"))
	if err != nil {
		return err
	}
	return s.wakeDependency(ctx, "source_cutover_manifest", claim.SourceInstanceID, claim.SourceInstanceID)
}

func (s *Service) processUsageEvent(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SchemaVersion != "3.0" || claim.StreamID != "usage" {
		return errors.New("usage fact is restricted to the v3 usage stream")
	}
	var payload usageEventPayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if _, err := s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID); err != nil {
		return err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, payload.OccurredAt)
	if err != nil {
		return err
	}
	err = s.store.ObserveUsageEvent(ctx, postgresstore.UsageObservation{
		SourceInstanceID: claim.SourceInstanceID, ExternalUserID: payload.ExternalUserID,
		ExternalEventID: claim.EventID, ExternalUsageID: payload.ExternalUsageID,
		EventTime: occurredAt.UTC(), ObservedAt: claim.ObservedAt, StreamWatermarkAt: claim.ScanCeilingAt,
		ServiceUnits: payload.ServiceUnits, UnitCode: payload.UnitCode, BillingScope: payload.BillingScope,
		CausalDomain: payload.CausalDomain, CausalOrder: causalOrderValue(payload.CausalOrder),
		SourceCursor: payload.SourceCursor, SourceRevision: claim.PayloadHash,
		CutoverManifestHash: payload.CutoverManifestHash, ConfigurationHash: payload.ConfigurationHash,
		SourceSequence: claim.BatchSequence, BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID,
	}, auditActor(ctx, "source_connector", claim.SourceInstanceID, "verified v3 usage fact"))
	if errors.Is(err, domain.ErrSourceUnavailable) {
		return s.waitForDependency("source_eligibility_cutover", claim.SourceInstanceID, payload.ExternalUserID)
	}
	return err
}

func (s *Service) processCreditEvent(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SchemaVersion != "3.0" || claim.StreamID != "credits" {
		return errors.New("credit fact is restricted to the v3 credits stream")
	}
	var payload creditEventPayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if _, err := s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID); err != nil {
		return err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, payload.OccurredAt)
	if err != nil {
		return err
	}
	err = s.store.ObserveCreditEvent(ctx, postgresstore.CreditObservation{
		SourceInstanceID: claim.SourceInstanceID, ExternalUserID: payload.ExternalUserID,
		ExternalEventID: claim.EventID, ExternalCreditID: payload.ExternalCreditID,
		EventTime: occurredAt.UTC(), ObservedAt: claim.ObservedAt, StreamWatermarkAt: claim.ScanCeilingAt,
		ServiceUnits: payload.ServiceUnits, UnitCode: payload.UnitCode, CreditKind: strings.ToUpper(payload.CreditKind),
		CausalDomain: payload.CausalDomain, CausalOrder: causalOrderValue(payload.CausalOrder),
		SourceCursor: payload.SourceCursor, SourceRevision: claim.PayloadHash,
		CutoverManifestHash: payload.CutoverManifestHash, ConfigurationHash: payload.ConfigurationHash,
		SourceSequence: claim.BatchSequence, BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID,
	}, auditActor(ctx, "source_connector", claim.SourceInstanceID, "verified v3 non-cash credit fact"))
	if errors.Is(err, domain.ErrSourceUnavailable) {
		return s.waitForDependency("source_eligibility_cutover", claim.SourceInstanceID, payload.ExternalUserID)
	}
	return err
}

func (s *Service) processBalanceCheckpoint(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SchemaVersion != "3.0" || claim.StreamID != "balances" {
		return errors.New("balance checkpoint is restricted to the v3 balances stream")
	}
	var payload balanceCheckpointPayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if payload.BaselineMember == nil {
		return errors.New("balance checkpoint baseline membership proof is required")
	}
	manifestReady, err := s.store.CutoverManifestExists(ctx, claim.SourceInstanceID)
	if err != nil {
		return err
	}
	if !manifestReady {
		return s.waitForDependency("source_cutover_manifest", claim.SourceInstanceID, claim.SourceInstanceID)
	}
	if _, err := s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID); err != nil {
		return err
	}
	asOf, err := time.Parse(time.RFC3339Nano, payload.AsOf)
	if err != nil {
		return err
	}
	baselineID := ""
	if payload.CheckpointKind == "cutover" {
		baselineID = payload.SourceSnapshotID
	}
	err = s.store.ObserveBalanceCheckpoint(ctx, postgresstore.BalanceCheckpointObservation{
		SourceInstanceID: claim.SourceInstanceID, ExternalUserID: payload.ExternalUserID,
		ExternalEventID: claim.EventID, CheckpointID: payload.CheckpointID,
		CheckpointKind: payload.CheckpointKind, BalanceServiceUnits: payload.BalanceServiceUnits,
		UnitCode: payload.UnitCode, BaselineSnapshotID: baselineID, SourceSnapshotID: payload.SourceSnapshotID,
		SnapshotRowCount: payload.SnapshotRowCount,
		AsOf:             asOf.UTC(), ObservedAt: claim.ObservedAt, StreamWatermarkAt: claim.ScanCeilingAt,
		SourceCursor: payload.SourceCursor, SourceRevision: claim.PayloadHash,
		CutoverManifestHash: payload.CutoverManifestHash, ConfigurationHash: payload.ConfigurationHash,
		SourceSequence: claim.BatchSequence, BalanceNegative: payload.BalanceNegative,
		BaselineMember: *payload.BaselineMember,
		CatchupKeyHMAC: claim.CatchupKeyHMAC, BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID,
	}, auditActor(ctx, "source_connector", claim.SourceInstanceID, "verified v3 balance checkpoint"))
	if err != nil {
		return s.balanceCheckpointDependencyError(err, claim, payload)
	}
	return s.wakeDependency(ctx, "source_eligibility_cutover", claim.SourceInstanceID, payload.ExternalUserID)
}

func (s *Service) balanceCheckpointDependencyError(err error, claim postgresstore.SourceEventClaim, payload balanceCheckpointPayload) error {
	if errors.Is(err, domain.ErrSourceUnavailable) && payload.BaselineMember != nil &&
		payload.CheckpointKind == "reconciliation" && *payload.BaselineMember {
		return s.waitForDependency("source_eligibility_cutover", claim.SourceInstanceID, payload.ExternalUserID)
	}
	return err
}

func (s *Service) processSubscriptionPurchase(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SchemaVersion != "3.0" || claim.StreamID != "payments" {
		return errors.New("subscription purchase is restricted to the v3 payments stream")
	}
	var payload subscriptionPurchasePayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	account, err := s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID)
	if err != nil {
		return err
	}
	completedAt, err := time.Parse(time.RFC3339Nano, payload.CompletedAt)
	if err != nil {
		return err
	}
	paidMinor, err := strconv.ParseInt(payload.PaidMinor, 10, 64)
	if err != nil || paidMinor <= 0 || payload.Currency != domain.CurrencyCNY {
		return errors.New("invalid verified subscription amount")
	}
	verification := domain.VerificationPending
	if payload.VerificationState == "verified" && !payload.Refunded {
		verification = domain.VerificationVerified
	} else if payload.VerificationState == "frozen" || payload.Refunded {
		verification = domain.VerificationFrozen
	} else if payload.VerificationState != "pending" {
		return errors.New("invalid subscription verification state")
	}
	unitCode := "SUB2_BALANCE_1E8"
	if claim.SourceType == domain.SourceNewAPI {
		unitCode = "NEWAPI_QUOTA"
	}
	capMinor := int64(0)
	if verification == domain.VerificationVerified {
		capMinor = paidMinor
	}
	eventKind := "payment"
	if payload.Refunded {
		eventKind = "refund"
	}
	_, err = s.ObserveFundingLot(ctx, FundingObservation{
		Lot: domain.FundingLot{PrincipalID: account.PrincipalID, SourceInstanceID: claim.SourceInstanceID,
			SourceType: claim.SourceType, ExternalOrderID: payload.ExternalOrderID, Currency: domain.CurrencyCNY,
			OriginalMinor: paidMinor, CurrentCapMinor: capMinor, Verification: verification,
			SourceStatus: "subscription:" + payload.VerificationState, SourceRevision: claim.PayloadHash,
			CompletedAt: completedAt.UTC(), ObservedAt: claim.ObservedAt},
		ExternalUserID: payload.ExternalUserID, EventKind: eventKind, ExternalEventID: claim.EventID,
		SchemaVersion: "source-agent-v3.0", Payload: body, SourceUpdatedAt: completedAt.UTC(),
		SourceSequence: claim.BatchSequence, EligibilityKind: domain.EligibilitySubscriptionCash,
		CashServiceUnits: "0", WalletUnitCode: unitCode, CutoverManifestHash: payload.CutoverManifestHash,
		ConfigurationHash: payload.ConfigurationHash, CausalDomain: payload.CausalDomain,
		CausalOrder: causalOrderValue(payload.CausalOrder), SourceCursor: payload.SourceCursor,
		BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID, StreamWatermarkAt: claim.ScanCeilingAt,
	})
	if errors.Is(err, domain.ErrSourceUnavailable) {
		return s.waitForDependency("source_eligibility_cutover", claim.SourceInstanceID, payload.ExternalUserID)
	}
	return err
}

func (s *Service) processIdentityBinding(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	var payload identityBindingPayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if payload.ProviderType != "oidc" || payload.Issuer == "" || payload.ProviderSubject == "" || payload.ExternalUserID == "" {
		return errors.New("invalid central OIDC identity projection")
	}
	issuerURL, err := url.Parse(payload.Issuer)
	if err != nil || issuerURL.Scheme != "https" || issuerURL.Hostname() == "" || issuerURL.User != nil || issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
		return errors.New("invalid central OIDC issuer")
	}
	user, err := s.store.FindUserByOIDC(ctx, payload.Issuer, payload.ProviderSubject)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.waitForDependency("invoice_oidc_user", "", payload.Issuer+"\n"+payload.ProviderSubject)
		}
		return fmt.Errorf("resolve invoice user for identity projection: %w", err)
	}
	subjectHMAC, err := s.keys.BlindIndex("external-oidc/"+claim.SourceInstanceID, payload.Issuer+"\n"+payload.ProviderSubject)
	if err != nil {
		return err
	}
	status := "pending"
	verifiedAt, err := parseOptionalTime(payload.VerifiedAt)
	if err != nil {
		return err
	}
	if !verifiedAt.IsZero() {
		status = "verified"
	}
	if payload.UserStatus == "deleted" {
		status = "revoked"
	} else if payload.UserStatus == "suspended" || user.Status != "active" {
		status = "frozen"
	}
	sourceObservedAt := claim.ObservedAt
	if claim.SourceType == domain.SourceSub2API {
		sourceObservedAt, err = time.Parse(time.RFC3339Nano, payload.UpdatedAt)
		if err != nil || sourceObservedAt.After(claim.ObservedAt.Add(5*time.Minute)) {
			return errors.New("invalid Sub2API identity update time")
		}
	}
	saved, _, err := s.store.BindExternalAccountFromSource(ctx, postgresstore.ExternalAccountRecord{
		PrincipalID: user.ID, SourceInstanceID: claim.SourceInstanceID,
		ExternalUserID: payload.ExternalUserID, ExternalSubjectHMAC: subjectHMAC,
		BindingMethod: "source_signed_oidc_projection", BindingStatus: status, VerifiedAt: verifiedAt,
		SourceObservedAt: sourceObservedAt.UTC(), SourceSequence: claim.BatchSequence,
	}, postgresstore.AuditActor{Type: "source_connector", ID: claim.SourceInstanceID, Reason: "signed central OIDC identity projection"})
	if err != nil {
		return err
	}
	if saved.BindingStatus == "verified" {
		return s.wakeDependency(ctx, "source_external_account", claim.SourceInstanceID, payload.ExternalUserID)
	}
	return nil
}

func (s *Service) verifiedExternalAccount(ctx context.Context, claim postgresstore.SourceEventClaim, externalUserID string) (postgresstore.ExternalAccountRecord, error) {
	account, err := s.store.GetExternalAccountBySourceUser(ctx, claim.SourceInstanceID, externalUserID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return postgresstore.ExternalAccountRecord{}, s.waitForDependency("source_external_account", claim.SourceInstanceID, externalUserID)
		}
		return postgresstore.ExternalAccountRecord{}, err
	}
	if account.BindingStatus == "pending" {
		return postgresstore.ExternalAccountRecord{}, s.waitForDependency("source_external_account", claim.SourceInstanceID, externalUserID)
	}
	if account.BindingStatus != "verified" {
		return postgresstore.ExternalAccountRecord{}, domain.ErrForbidden
	}
	return account, nil
}

func (s *Service) staleFundingProjection(ctx context.Context, claim postgresstore.SourceEventClaim, externalOrderID string, sourceTime time.Time) (bool, error) {
	currentTime, currentSequence, err := s.store.FundingSourceVersion(ctx, claim.SourceInstanceID, externalOrderID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stale := sourceTime.Before(currentTime) || sourceTime.Equal(currentTime) && claim.BatchSequence <= currentSequence
	if stale {
		err = s.store.AuditStaleFundingProjection(ctx, claim.SourceInstanceID, externalOrderID,
			sourceTime, claim.BatchSequence, postgresstore.AuditActor{Type: "source_connector", ID: claim.SourceInstanceID,
				Reason: "out-of-order signed source event ignored"})
	}
	return stale, err
}

func (s *Service) processPaymentOrder(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SourceType != domain.SourceSub2API {
		return errors.New("only Sub2API may emit verified payment orders")
	}
	var payload paymentOrderPayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if payload.Currency != domain.CurrencyCNY || payload.ExternalOrderID == "" || payload.ExternalUserID == "" {
		return errors.New("unsupported payment order")
	}
	validStatus := map[string]bool{
		"PENDING": true, "PAID": true, "RECHARGING": true, "COMPLETED": true,
		"EXPIRED": true, "CANCELLED": true, "FAILED": true,
		"REFUND_REQUESTED": true, "REFUNDING": true, "REFUND_PENDING": true,
		"PARTIALLY_REFUNDED": true, "REFUNDED": true, "REFUND_FAILED": true,
	}
	if !validStatus[payload.Status] {
		return errors.New("unsupported Sub2API payment status")
	}
	sourceAmountMinor, err := cnyMinor(payload.Amount)
	if err != nil {
		return err
	}
	payMinor, err := cnyMinor(payload.PayAmount)
	if err != nil {
		return err
	}
	sourceRefundMinor, err := cnyMinor(payload.RefundAmount)
	if err != nil {
		return err
	}
	gatewayRefundMinor, err := cnyMinor(payload.GatewayRefundAmount)
	if err != nil {
		return err
	}
	expectedGatewayRefund, err := proratedCNYRefundMinor(sourceAmountMinor, payMinor, sourceRefundMinor)
	if err != nil || gatewayRefundMinor != expectedGatewayRefund || gatewayRefundMinor > payMinor {
		return errors.New("inconsistent payment amounts")
	}
	capMinor := payMinor - gatewayRefundMinor
	completedAt, err := parseOptionalTime(payload.CompletedAt)
	if err != nil {
		return err
	}
	sourceUpdatedAt, err := time.Parse(time.RFC3339Nano, payload.UpdatedAt)
	if err != nil || sourceUpdatedAt.After(claim.ObservedAt.Add(5*time.Minute)) {
		return errors.New("invalid Sub2API payment update time")
	}
	stale, err := s.staleFundingProjection(ctx, claim, payload.ExternalOrderID, sourceUpdatedAt.UTC())
	if err != nil {
		return err
	}
	if stale {
		return s.wakeDependency(ctx, "source_funding_lot", claim.SourceInstanceID, payload.ExternalOrderID)
	}
	account, err := s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID)
	if err != nil {
		return err
	}
	verification := domain.VerificationPending
	if payload.Status == "COMPLETED" && !completedAt.IsZero() && payMinor > 0 && gatewayRefundMinor == 0 {
		verification = domain.VerificationVerified
	} else if gatewayRefundMinor > 0 || strings.Contains(payload.Status, "REFUND") {
		verification = domain.VerificationFrozen
	}
	eligibilityKind := domain.EligibilityKind("")
	cashUnits, unitCode, causalOrder := "", "", ""
	if claim.SchemaVersion == "3.0" {
		if payload.WalletCashServiceUnits == nil || payload.WalletUnitCode == nil || payload.CausalOrder == nil ||
			payload.SourceCursor == "" || payload.CausalDomain == "" || payload.CutoverManifestHash == "" || payload.ConfigurationHash == "" {
			return errors.New("incomplete v3 wallet payment eligibility fact")
		}
		eligibilityKind = domain.EligibilityWalletCash
		cashUnits, unitCode, causalOrder = *payload.WalletCashServiceUnits, *payload.WalletUnitCode, *payload.CausalOrder
	}
	_, err = s.ObserveFundingLot(ctx, FundingObservation{
		Lot: domain.FundingLot{
			PrincipalID: account.PrincipalID, SourceInstanceID: claim.SourceInstanceID,
			SourceType: claim.SourceType, ExternalOrderID: payload.ExternalOrderID,
			TradeNo: payload.ExternalOrderID, Currency: domain.CurrencyCNY,
			OriginalMinor: payMinor, CurrentCapMinor: capMinor,
			Verification: verification, SourceStatus: payload.Status,
			SourceRevision: claim.PayloadHash, CompletedAt: completedAt, ObservedAt: claim.ObservedAt,
		}, ExternalUserID: payload.ExternalUserID, EventKind: "payment",
		ExternalEventID: claim.EventID, SchemaVersion: "source-agent-v" + claim.SchemaVersion, Payload: body,
		SourceUpdatedAt: sourceUpdatedAt.UTC(), SourceSequence: claim.BatchSequence,
		EligibilityKind: eligibilityKind, CashServiceUnits: cashUnits, WalletUnitCode: unitCode,
		CutoverManifestHash: payload.CutoverManifestHash, ConfigurationHash: payload.ConfigurationHash,
		CausalDomain: payload.CausalDomain, CausalOrder: causalOrder, SourceCursor: payload.SourceCursor,
		BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID, StreamWatermarkAt: claim.ScanCeilingAt,
	})
	if err != nil {
		if errors.Is(err, domain.ErrSourceUnavailable) {
			return s.waitForDependency("source_eligibility_cutover", claim.SourceInstanceID, payload.ExternalUserID)
		}
		return err
	}
	return s.wakeDependency(ctx, "source_funding_lot", claim.SourceInstanceID, payload.ExternalOrderID)
}

func (s *Service) processPaymentCandidate(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SourceType != domain.SourceNewAPI {
		return errors.New("payment candidates are restricted to New API")
	}
	var payload paymentCandidatePayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if payload.VerificationState != "pending_manual" || payload.ExternalOrderID == "" || payload.ExternalUserID == "" {
		return errors.New("invalid New API payment candidate")
	}
	if payload.Currency != nil && *payload.Currency != domain.CurrencyCNY {
		return errors.New("unsupported candidate currency")
	}
	stale, err := s.staleFundingProjection(ctx, claim, payload.ExternalOrderID, claim.ObservedAt)
	if err != nil {
		return err
	}
	if stale {
		return s.wakeDependency(ctx, "source_funding_lot", claim.SourceInstanceID, payload.ExternalOrderID)
	}
	account, err := s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID)
	if err != nil {
		return err
	}
	observedPayMinor, err := cnyMinor(payload.ObservedPayAmount)
	if err != nil {
		return err
	}
	completedAt, err := parseOptionalTime(payload.CompletedAt)
	if err != nil {
		return err
	}
	eligibilityKind := domain.EligibilityKind("")
	cashUnits, unitCode, causalOrder := "", "", ""
	if claim.SchemaVersion == "3.0" {
		if payload.CausalOrder == nil || payload.SourceCursor == "" || payload.CausalDomain == "" ||
			payload.CutoverManifestHash == "" || payload.ConfigurationHash == "" ||
			(payload.WalletCashServiceUnits == nil) != (payload.WalletUnitCode == nil) {
			return errors.New("incomplete v3 payment candidate eligibility fact")
		}
		causalOrder = *payload.CausalOrder
		if payload.WalletCashServiceUnits != nil {
			eligibilityKind = domain.EligibilityWalletCash
			cashUnits, unitCode = *payload.WalletCashServiceUnits, *payload.WalletUnitCode
		}
	}
	_, err = s.ObserveFundingLot(ctx, FundingObservation{
		Lot: domain.FundingLot{
			PrincipalID: account.PrincipalID, SourceInstanceID: claim.SourceInstanceID,
			SourceType: claim.SourceType, ExternalOrderID: payload.ExternalOrderID,
			// New API's restricted projection deliberately does not read trade_no.
			// ExternalOrderID is the top_ups.id display/reference value.
			TradeNo: "", Currency: domain.CurrencyCNY,
			OriginalMinor: observedPayMinor, CurrentCapMinor: 0,
			Verification:   domain.VerificationPending,
			SourceStatus:   "candidate:" + payload.SourceStatus,
			SourceRevision: claim.PayloadHash, CompletedAt: completedAt, ObservedAt: claim.ObservedAt,
		}, ExternalUserID: payload.ExternalUserID, EventKind: "payment",
		ExternalEventID: claim.EventID, SchemaVersion: "source-agent-v" + claim.SchemaVersion, Payload: body,
		SourceUpdatedAt: claim.ObservedAt, SourceSequence: claim.BatchSequence,
		EligibilityKind: eligibilityKind, CashServiceUnits: cashUnits, WalletUnitCode: unitCode,
		CutoverManifestHash: payload.CutoverManifestHash, ConfigurationHash: payload.ConfigurationHash,
		CausalDomain: payload.CausalDomain, CausalOrder: causalOrder, SourceCursor: payload.SourceCursor,
		BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID, StreamWatermarkAt: claim.ScanCeilingAt,
	})
	if err != nil {
		if errors.Is(err, domain.ErrSourceUnavailable) {
			return s.waitForDependency("source_eligibility_cutover", claim.SourceInstanceID, payload.ExternalUserID)
		}
		return err
	}
	return s.wakeDependency(ctx, "source_funding_lot", claim.SourceInstanceID, payload.ExternalOrderID)
}

func (s *Service) processPaymentAdjustment(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	if claim.SourceType != domain.SourceSub2API {
		return errors.New("payment adjustments are restricted to Sub2API")
	}
	var payload paymentAdjustmentPayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if payload.Currency != domain.CurrencyCNY || payload.AdjustmentType != "refund" ||
		payload.Basis != "absolute_cumulative_gateway_refund" {
		return errors.New("invalid cumulative payment adjustment")
	}
	if claim.SchemaVersion == "3.0" {
		if payload.SourceCursor == "" || payload.CausalDomain == "" || payload.CausalOrder == nil ||
			payload.CutoverManifestHash == "" || payload.ConfigurationHash == "" {
			return errors.New("incomplete v3 payment adjustment metadata")
		}
		if err := s.store.ValidateEconomicFactContext(ctx, claim.SourceInstanceID, "payments", claim.EventID,
			claim.BatchID, claim.ScanCycleID, claim.PayloadHash, claim.ScanCeilingAt); err != nil {
			return err
		}
	}
	sourceUpdatedAt, err := time.Parse(time.RFC3339Nano, payload.SourceUpdatedAt)
	if err != nil || sourceUpdatedAt.After(claim.ObservedAt.Add(5*time.Minute)) {
		return errors.New("invalid payment adjustment source time")
	}
	stale, err := s.staleFundingProjection(ctx, claim, payload.ExternalOrderID, sourceUpdatedAt.UTC())
	if err != nil {
		return err
	}
	if stale {
		return nil
	}
	if _, err = s.verifiedExternalAccount(ctx, claim, payload.ExternalUserID); err != nil {
		return err
	}
	lot, err := s.store.GetFundingLotByExternalOrder(ctx, claim.SourceInstanceID, payload.ExternalOrderID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.waitForDependency("source_funding_lot", claim.SourceInstanceID, payload.ExternalOrderID)
		}
		return err
	}
	adjustmentMinor, err := cnyMinor(payload.Amount)
	if err != nil || adjustmentMinor > lot.OriginalMinor {
		return errors.New("invalid payment adjustment amount")
	}
	newCap := lot.OriginalMinor - adjustmentMinor
	if newCap > lot.CurrentCapMinor {
		newCap = lot.CurrentCapMinor
	}
	lot.CurrentCapMinor = newCap
	lot.SourceStatus = payload.SourceStatus
	lot.SourceRevision = claim.PayloadHash
	lot.ObservedAt = claim.ObservedAt
	_, err = s.ObserveFundingLot(ctx, FundingObservation{
		Lot: lot, ExternalUserID: payload.ExternalUserID, EventKind: "refund",
		ExternalEventID: claim.EventID, SchemaVersion: "source-agent-v" + claim.SchemaVersion, Payload: body,
		SourceUpdatedAt: sourceUpdatedAt.UTC(), SourceSequence: claim.BatchSequence,
		BatchID: claim.BatchID, ScanCycleID: claim.ScanCycleID, StreamWatermarkAt: claim.ScanCeilingAt,
	})
	return err
}

// proratedCNYRefundMinor independently verifies the source agent's exact
// Sub2API refund calculation using integer/rational arithmetic. The source
// amount is Sub2API's credited balance or subscription price; paidMinor is the
// actual gateway money. Positive half cents round up, matching Sub2API's
// shopspring/decimal.Round behavior.
func proratedCNYRefundMinor(sourceAmountMinor, paidMinor, sourceRefundMinor int64) (int64, error) {
	if sourceAmountMinor <= 0 || paidMinor < 0 || sourceRefundMinor < 0 || sourceRefundMinor > sourceAmountMinor {
		return 0, errors.New("inconsistent source refund ratio")
	}
	if sourceRefundMinor == 0 || paidMinor == 0 {
		return 0, nil
	}
	if sourceRefundMinor == sourceAmountMinor {
		return paidMinor, nil
	}
	numerator := new(big.Int).Mul(big.NewInt(paidMinor), big.NewInt(sourceRefundMinor))
	denominator := big.NewInt(sourceAmountMinor)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Int64() > paidMinor {
		return 0, errors.New("prorated refund is outside paid amount")
	}
	return quotient.Int64(), nil
}

func (s *Service) processSourceTombstone(ctx context.Context, claim postgresstore.SourceEventClaim, body []byte) error {
	var payload tombstonePayload
	if err := strictJSON(body, &payload); err != nil {
		return err
	}
	if payload.ExternalID == "" || payload.Reason == "" {
		return errors.New("invalid source tombstone")
	}
	confirmedAt, err := time.Parse(time.RFC3339Nano, payload.ConfirmedAt)
	if err != nil || confirmedAt.After(s.now().Add(5*time.Minute)) {
		return errors.New("invalid source tombstone confirmation time")
	}
	if claim.EntityType == "identity_binding" {
		err = s.store.RevokeExternalAccountFromSource(ctx, claim.SourceInstanceID,
			payload.ExternalID, claim.PayloadHash, confirmedAt.UTC(), claim.BatchSequence,
			auditActor(ctx, "source_connector", claim.SourceInstanceID, "repeated-miss identity tombstone applied"))
		if errors.Is(err, domain.ErrNotFound) {
			return s.waitForDependency("source_external_account", claim.SourceInstanceID, payload.ExternalID)
		}
		return err
	}
	if claim.EntityType != "payment_order" {
		return errors.New("unsupported source tombstone entity")
	}
	lot, err := s.store.GetFundingLotByExternalOrder(ctx, claim.SourceInstanceID, payload.ExternalID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.waitForDependency("source_funding_lot", claim.SourceInstanceID, payload.ExternalID)
		}
		return err
	}
	// External user ID is not present in a payment tombstone.  The existing
	// verified account is resolved from the funding lot by ObserveFundingLot's
	// ownership checks; use a dedicated store lookup below.
	externalUserID, err := s.store.ExternalUserIDForFundingLot(ctx, lot.ID)
	if err != nil {
		return err
	}
	lot.Verification = domain.VerificationFrozen
	// A hard-deleted source payment can no longer prove any remaining
	// invoiceable value. Quarantine the complete cap; the existing funding
	// transaction releases unissued reservations and opens issued attention.
	lot.CurrentCapMinor = 0
	lot.SourceStatus = "tombstone:" + payload.Reason
	lot.SourceRevision = claim.PayloadHash
	lot.ObservedAt = claim.ObservedAt
	_, err = s.ObserveFundingLot(ctx, FundingObservation{
		Lot: lot, ExternalUserID: externalUserID, EventKind: "tombstone",
		ExternalEventID: claim.EventID, SchemaVersion: "source-agent-v2", Payload: body,
		SourceUpdatedAt: confirmedAt.UTC(), SourceSequence: claim.BatchSequence,
	})
	return err
}
