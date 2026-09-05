package sourceagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	externalReferencePattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	sourceIDPattern          = regexp.MustCompile(`^(?:[a-z0-9]|[a-z0-9][a-z0-9_-]{0,62}[a-z0-9])$`)
	decimalPattern           = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,8})?$`)
	serviceUnitsPattern      = regexp.MustCompile(`^(0|[1-9][0-9]{0,77})$`)
	stableCursorPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}:[0-9]{1,20}$`)
	causalDomainPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	checkpointIDPattern      = regexp.MustCompile(`^[a-f0-9]{64}:[1-9][0-9]{0,19}$`)
	currencyPattern          = regexp.MustCompile(`^[A-Z]{3}$`)
	hexHashPattern           = regexp.MustCompile(`^[a-f0-9]{64}$`)
	uuidPattern              = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
)

const (
	MaxBatchBytes         = 4 << 20
	MaxRecordPayloadBytes = 64 << 10
)

type Cursor struct {
	Sequence     uint64
	LastBodyHash string
}

type Validator struct {
	mu             sync.Mutex
	expectedSource string
	expectedStream string
	cursor         Cursor
	seenBatchIDs   map[string]string
	seenEventIDs   map[string]string
}

// NewValidator scopes replay and sequencing to one exact source+stream pair.
// Omitting expectedStream is supported only for the legacy schema 1.0 offline
// mock verifier; schema 2.0 validation fails closed without it.
func NewValidator(expectedSource string, expectedStream ...string) *Validator {
	stream := ""
	if len(expectedStream) == 1 {
		stream = strings.TrimSpace(expectedStream[0])
	} else if len(expectedStream) > 1 {
		stream = "!invalid!"
	}
	return &Validator{
		expectedSource: strings.TrimSpace(expectedSource),
		expectedStream: stream,
		seenBatchIDs:   make(map[string]string),
		seenEventIDs:   make(map[string]string),
	}
}

func (v *Validator) Cursor() Cursor {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cursor
}

// ValidateSigned verifies the detached Ed25519 request signature and its clock
// window before allowing the sequence/replay state transition.
func (v *Validator) ValidateSigned(raw []byte, metadata SignatureMetadata, keys PublicKeyResolver, now time.Time, maxSkew time.Duration) (ValidatedBatch, error) {
	if err := VerifyBatchSignature(raw, metadata, keys, now, maxSkew); err != nil {
		return ValidatedBatch{}, err
	}
	batch, err := decodeBatch(raw)
	if err != nil {
		return ValidatedBatch{}, errors.Join(ErrInvalidBatch, err)
	}
	if batch.SourceInstanceID != metadata.SourceID || batch.StreamID != metadata.StreamID || batch.BatchID != metadata.BatchID || batch.Sequence != metadata.Sequence {
		return ValidatedBatch{}, ErrSourceMismatch
	}
	for _, record := range batch.Records {
		if record.EntityType != EntityCutoverManifest {
			continue
		}
		var payload CutoverManifestPayload
		if err := decodeStrict(record.Payload, &payload); err != nil || payload.SigningKeyID != metadata.KeyID {
			return ValidatedBatch{}, errors.New("cutover manifest signing key does not match verified request key")
		}
	}
	return v.ValidateAndCommit(raw, metadata.BodySHA256)
}

// ValidateAndCommit validates the exact body hash, source identity, sequence,
// previous-body hash, event uniqueness and payload hashes before atomically
// advancing the in-memory cursor. Production persistence can wrap the same
// state transition in a durable transaction.
func (v *Validator) ValidateAndCommit(raw []byte, claimedBodySHA256 string) (ValidatedBatch, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(raw) == 0 || len(raw) > MaxBatchBytes {
		return ValidatedBatch{}, ErrInvalidBatch
	}

	actualBodyHash := SHA256Hex(raw)
	if !hexHashPattern.MatchString(claimedBodySHA256) || claimedBodySHA256 != actualBodyHash {
		return ValidatedBatch{}, ErrBodyHashMismatch
	}

	batch, err := decodeBatch(raw)
	if err != nil {
		return ValidatedBatch{}, errors.Join(ErrInvalidBatch, err)
	}
	if batch.SourceInstanceID != v.expectedSource {
		return ValidatedBatch{}, fmt.Errorf("%w: expected %q got %q", ErrSourceMismatch, v.expectedSource, batch.SourceInstanceID)
	}
	if (batch.SchemaVersion == SchemaVersionV2 || batch.SchemaVersion == SchemaVersionV3) && (v.expectedStream == "" || batch.StreamID != v.expectedStream) {
		return ValidatedBatch{}, fmt.Errorf("%w: expected stream %q got %q", ErrSourceMismatch, v.expectedStream, batch.StreamID)
	}
	if batch.SchemaVersion == SchemaVersionV1 && v.expectedStream != "" {
		return ValidatedBatch{}, fmt.Errorf("%w: schema 1.0 has no stream", ErrSourceMismatch)
	}
	if priorHash, exists := v.seenBatchIDs[batch.BatchID]; exists {
		if priorHash != actualBodyHash {
			return ValidatedBatch{}, ErrReplay
		}
		return ValidatedBatch{Batch: batch, RawBody: append([]byte(nil), raw...), BodyHash: actualBodyHash, Duplicate: true}, nil
	}
	if batch.Sequence <= v.cursor.Sequence {
		return ValidatedBatch{}, ErrReplay
	}
	expectedSequence := v.cursor.Sequence + 1
	if batch.Sequence != expectedSequence {
		return ValidatedBatch{}, fmt.Errorf("%w: expected %d got %d", ErrOutOfOrder, expectedSequence, batch.Sequence)
	}
	if expectedSequence == 1 {
		if batch.PreviousBatchHash != nil && *batch.PreviousBatchHash != "" {
			return ValidatedBatch{}, ErrPreviousHashMismatch
		}
	} else if batch.PreviousBatchHash == nil || *batch.PreviousBatchHash != v.cursor.LastBodyHash {
		return ValidatedBatch{}, ErrPreviousHashMismatch
	}

	newEventIDs := make(map[string]string, len(batch.Records))
	for index := range batch.Records {
		record := &batch.Records[index]
		if priorHash, exists := v.seenEventIDs[record.EventID]; exists {
			if priorHash != record.PayloadSHA256 {
				return ValidatedBatch{}, fmt.Errorf("%w: event id reused with different payload %q", ErrReplay, record.EventID)
			}
		}
		if _, exists := newEventIDs[record.EventID]; exists {
			return ValidatedBatch{}, fmt.Errorf("%w: duplicate event in batch %q", ErrReplay, record.EventID)
		}
		if err := validateRecord(batch.SchemaVersion, batch.SourceType, batch.StreamID, batch.SourceInstanceID, batch.SourceRuntimeVersion, record); err != nil {
			return ValidatedBatch{}, fmt.Errorf("record %d: %w", index, err)
		}
		if batch.SchemaVersion == SchemaVersionV3 {
			if err := validateReceivedV3SnapshotBinding(*record, batch); err != nil {
				return ValidatedBatch{}, fmt.Errorf("record %d: %w", index, err)
			}
		}
		newEventIDs[record.EventID] = record.PayloadSHA256
	}

	v.seenBatchIDs[batch.BatchID] = actualBodyHash
	for eventID, payloadHash := range newEventIDs {
		v.seenEventIDs[eventID] = payloadHash
	}
	v.cursor = Cursor{Sequence: batch.Sequence, LastBodyHash: actualBodyHash}

	return ValidatedBatch{Batch: batch, RawBody: append([]byte(nil), raw...), BodyHash: actualBodyHash}, nil
}

func decodeBatch(raw []byte) (Batch, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var batch Batch
	if err := decoder.Decode(&batch); err != nil {
		return Batch{}, err
	}
	if err := ensureDecodeEOF(decoder); err != nil {
		return Batch{}, err
	}
	if batch.SchemaVersion != SchemaVersionV1 && batch.SchemaVersion != SchemaVersionV2 && batch.SchemaVersion != SchemaVersionV3 {
		return Batch{}, fmt.Errorf("unsupported schema version %q", batch.SchemaVersion)
	}
	if !sourceIDPattern.MatchString(batch.SourceInstanceID) || !uuidPattern.MatchString(batch.BatchID) || batch.Sequence == 0 || batch.AgentVersion == "" || len(batch.AgentVersion) > 64 || batch.SourceRuntimeVersion == "" || len(batch.SourceRuntimeVersion) > 64 {
		return Batch{}, errors.New("required batch metadata is missing")
	}
	if batch.SchemaVersion == SchemaVersionV1 && len(batch.SourceInstanceID) < 2 {
		return Batch{}, errors.New("schema 1.0 source id must contain at least two characters")
	}
	if batch.SchemaVersion == SchemaVersionV2 && (!uuidPattern.MatchString(batch.SourceInstanceID) ||
		(batch.StreamID != "payments" && batch.StreamID != "identities") ||
		(batch.SourceType != SourceSub2API && batch.SourceType != SourceNewAPI) ||
		batch.Mode != "db_projection" || (batch.ProjectionStatus != "healthy" && batch.ProjectionStatus != "blocked")) {
		return Batch{}, errors.New("schema 2.0 production metadata is invalid")
	}
	if batch.SchemaVersion == SchemaVersionV3 {
		if !uuidPattern.MatchString(batch.SourceInstanceID) ||
			(batch.StreamID != StreamPayments && batch.StreamID != StreamUsage && batch.StreamID != StreamCredits && batch.StreamID != StreamBalances) ||
			(batch.SourceType != SourceSub2API && batch.SourceType != SourceNewAPI) || batch.Mode != "db_projection" ||
			(batch.ProjectionStatus != "healthy" && batch.ProjectionStatus != "blocked") {
			return Batch{}, errors.New("schema 3.0 production metadata is invalid")
		}
		watermark, err := time.Parse(time.RFC3339Nano, batch.StreamWatermarkAt)
		if err != nil || strings.TrimSpace(batch.SourceCursor) == "" || len(batch.SourceCursor) > 160 {
			return Batch{}, errors.New("schema 3.0 source watermark is invalid")
		}
		ceiling, err := time.Parse(time.RFC3339Nano, batch.ScanCeilingAt)
		if err != nil || strings.TrimSpace(batch.ScanCeilingCursor) == "" || len(batch.ScanCeilingCursor) > 256 || watermark.After(ceiling) || !uuidPattern.MatchString(batch.ScanCycleID) {
			return Batch{}, errors.New("schema 3.0 scan ceiling is invalid")
		}
		if batch.StreamID == StreamBalances {
			if !hexHashPattern.MatchString(batch.ScanSnapshotID) || batch.ScanSnapshotRowCount == nil || *batch.ScanSnapshotRowCount < 0 || *batch.ScanSnapshotRowCount > balanceSnapshotMaxRows {
				return Batch{}, errors.New("schema 3.0 balances snapshot metadata is invalid")
			}
		} else if batch.ScanSnapshotID != "" || batch.ScanSnapshotRowCount != nil {
			return Batch{}, errors.New("schema 3.0 non-balance stream carried snapshot metadata")
		}
	}
	if batch.SchemaVersion == SchemaVersionV1 && batch.StreamID != "" {
		return Batch{}, errors.New("schema 1.0 does not contain a stream id")
	}
	if batch.SourceType != SourceSub2API && batch.SourceType != SourceNewAPI && batch.SourceType != SourceDesktop {
		return Batch{}, fmt.Errorf("unsupported source type %q", batch.SourceType)
	}
	if batch.Mode != "mock" && batch.Mode != "db_projection" && batch.Mode != "admin_api" {
		return Batch{}, fmt.Errorf("unsupported source mode %q", batch.Mode)
	}
	if _, err := time.Parse(time.RFC3339, batch.CapturedAt); err != nil {
		return Batch{}, fmt.Errorf("invalid captured_at: %w", err)
	}
	if len(batch.Records) > 500 {
		return Batch{}, errors.New("record count exceeds 500")
	}
	return batch, nil
}

func validateReceivedV3SnapshotBinding(record Record, batch Batch) error {
	if batch.StreamID != StreamBalances {
		return nil
	}
	if batch.ScanSnapshotRowCount == nil {
		return errors.New("balance batch is missing signed scan snapshot metadata")
	}
	expectedCount := fmt.Sprint(*batch.ScanSnapshotRowCount)
	switch record.EntityType {
	case EntityCutoverManifest:
		var payload CutoverManifestPayload
		if err := decodeStrict(record.Payload, &payload); err != nil || payload.BaselineSnapshotHash != batch.ScanSnapshotID || payload.BaselineRowCount != expectedCount {
			return errors.New("cutover manifest does not match signed scan snapshot metadata")
		}
	case EntityBalanceCheckpoint:
		var payload BalanceCheckpointPayload
		if err := decodeStrict(record.Payload, &payload); err != nil || payload.SourceSnapshotID != batch.ScanSnapshotID || payload.SnapshotRowCount != expectedCount {
			return errors.New("balance checkpoint does not match signed scan snapshot metadata")
		}
	}
	return nil
}

func validateRecord(schemaVersion, sourceType, streamID, sourceID, sourceRuntime string, record *Record) error {
	if !uuidPattern.MatchString(record.EventID) || (record.Operation != "upsert" && record.Operation != "tombstone") || record.EntityType == "" {
		return errors.Join(ErrInvalidBatch, errors.New("invalid event metadata"))
	}
	if len(record.Payload) == 0 || len(record.Payload) > MaxRecordPayloadBytes {
		return errors.Join(ErrInvalidBatch, errors.New("record payload exceeds size limit"))
	}
	if _, err := time.Parse(time.RFC3339, record.ObservedAt); err != nil {
		return fmt.Errorf("invalid observed_at: %w", err)
	}
	payloadHash, err := HashCanonicalPayload(record.Payload)
	if err != nil {
		return errors.Join(ErrInvalidBatch, err)
	}
	if record.PayloadSHA256 != payloadHash {
		return ErrPayloadHashMismatch
	}
	if record.Operation == "tombstone" {
		if schemaVersion == SchemaVersionV3 {
			return errors.New("schema 3.0 economic facts cannot be tombstoned")
		}
		if record.EntityType != EntityPaymentOrder && record.EntityType != EntityIdentityBinding &&
			record.EntityType != EntityUsageEvent && record.EntityType != EntityCreditEvent && record.EntityType != EntityBalanceCheckpoint && record.EntityType != EntitySubscriptionPurchase {
			return errors.New("tombstone entity type is not supported")
		}
		var payload TombstonePayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalID) || (payload.Reason != "source_deleted" && payload.Reason != "source_disabled" && payload.Reason != "reconciliation_missing") {
			return errors.New("invalid tombstone fields")
		}
		return validateRequiredTimes(payload.ConfirmedAt)
	}
	if schemaVersion == SchemaVersionV3 && !entityAllowedForV3Stream(streamID, record.EntityType, record.Operation) {
		return errors.New("schema 3.0 entity/stream mismatch")
	}
	if schemaVersion == SchemaVersionV2 {
		if streamID == StreamIdentities && record.EntityType != EntityIdentityBinding {
			return errors.New("schema 2.0 identity stream entity mismatch")
		}
		if streamID == StreamPayments && record.EntityType != EntityPaymentOrder && record.EntityType != EntityPaymentCandidate && record.EntityType != EntityPaymentAdjustment && record.EntityType != EntityUsageDaily {
			return errors.New("schema 2.0 payments stream entity mismatch")
		}
	}

	switch record.EntityType {
	case EntityPaymentOrder:
		if sourceType == SourceNewAPI {
			return errors.New("newapi cannot emit verified payment_order records")
		}
		var payload PaymentOrderPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalOrderID) || !externalReferencePattern.MatchString(payload.ExternalUserID) {
			return errors.New("invalid external payment reference")
		}
		if payload.Status == "" || len(payload.Status) > 64 || payload.OrderType == "" || len(payload.OrderType) > 32 || len(payload.PaymentType) > 32 || len(payload.ProviderKey) > 32 || !decimalPattern.MatchString(payload.Amount) || !decimalPattern.MatchString(payload.PayAmount) || !decimalPattern.MatchString(payload.RefundAmount) || !decimalPattern.MatchString(payload.GatewayRefundAmount) || !currencyPattern.MatchString(payload.Currency) || (payload.ExternalTradeRefHMAC != "" && !hexHashPattern.MatchString(payload.ExternalTradeRefHMAC)) {
			return errors.New("invalid payment financial fields")
		}
		if _, ok := sub2APIStatuses[payload.Status]; !ok {
			return errors.New("invalid payment status")
		}
		if err := validateRequiredTimes(payload.CreatedAt, payload.UpdatedAt); err != nil {
			return err
		}
		if err := validateOptionalTimes(payload.CompletedAt, payload.RefundAt); err != nil {
			return err
		}
		if schemaVersion == SchemaVersionV3 {
			if payload.OrderType != "balance" || payload.CompletedAt == nil || payload.WalletCashServiceUnits == nil || payload.WalletUnitCode == nil ||
				(payload.WalletCashServiceUnits == nil) != (payload.WalletUnitCode == nil) ||
				(payload.WalletCashServiceUnits != nil && (!serviceUnitsPattern.MatchString(*payload.WalletCashServiceUnits) || *payload.WalletUnitCode != unitCodeForSource(sourceType))) {
				return errors.New("invalid wallet cash service units")
			}
			if err := validateFactMetadata(payload.FactMetadata); err != nil {
				return err
			}
		}
	case EntityPaymentCandidate:
		if schemaVersion != SchemaVersionV2 && schemaVersion != SchemaVersionV3 {
			return errors.New("payment candidates require a server schema")
		}
		if sourceType != SourceNewAPI {
			return errors.New("payment candidates are reserved for unverified New API top-ups")
		}
		var payload PaymentCandidatePayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalOrderID) || !externalReferencePattern.MatchString(payload.ExternalUserID) || payload.SourceStatus == "" || len(payload.SourceStatus) > 64 || payload.OrderType != "topup" || payload.VerificationState != VerificationPendingManual || payload.VerificationReason == "" || len(payload.VerificationReason) > 512 || len(payload.PaymentType) > 50 || len(payload.ProviderKey) > 50 || !decimalPattern.MatchString(payload.QuotedAmount) || !decimalPattern.MatchString(payload.ObservedPayAmount) {
			return errors.New("invalid payment candidate fields")
		}
		if payload.Currency != nil && !currencyPattern.MatchString(*payload.Currency) {
			return errors.New("invalid candidate currency")
		}
		if err := validateRequiredTimes(payload.CreatedAt, payload.ObservedAt); err != nil {
			return err
		}
		if err := validateOptionalTimes(payload.CompletedAt); err != nil {
			return err
		}
		if schemaVersion == SchemaVersionV3 {
			if payload.CompletedAt == nil || payload.Currency == nil || *payload.Currency != "CNY" ||
				(payload.WalletCashServiceUnits == nil) != (payload.WalletUnitCode == nil) ||
				(payload.WalletCashServiceUnits != nil && (!serviceUnitsPattern.MatchString(*payload.WalletCashServiceUnits) || *payload.WalletUnitCode != unitCodeForSource(sourceType))) {
				return errors.New("invalid wallet candidate service units")
			}
			if err := validateFactMetadata(payload.FactMetadata); err != nil {
				return err
			}
		}
	case EntityPaymentAdjustment:
		if schemaVersion != SchemaVersionV2 && schemaVersion != SchemaVersionV3 {
			return errors.New("payment adjustments require a server schema")
		}
		if sourceType != SourceSub2API {
			return errors.New("payment adjustments require a refund-capable source")
		}
		var payload PaymentAdjustmentPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalOrderID) || !externalReferencePattern.MatchString(payload.ExternalUserID) || payload.AdjustmentType != AdjustmentRefund || !decimalPattern.MatchString(payload.Amount) || payload.Amount == "0" || !currencyPattern.MatchString(payload.Currency) || payload.SourceStatus == "" || len(payload.SourceStatus) > 64 || payload.Basis != "absolute_cumulative_gateway_refund" {
			return errors.New("invalid payment adjustment fields")
		}
		if err := validateRequiredTimes(payload.SourceUpdatedAt); err != nil {
			return err
		}
		if err := validateOptionalTimes(payload.EffectiveAt); err != nil {
			return err
		}
		if schemaVersion == SchemaVersionV3 {
			if err := validateFactMetadata(payload.FactMetadata); err != nil {
				return err
			}
		}
	case EntityIdentityBinding:
		var payload IdentityBindingPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalUserID) || payload.ProviderType == "" || len(payload.ProviderType) > 32 || payload.ProviderKey == "" || len(payload.ProviderKey) > 255 || payload.ProviderSubject == "" || len(payload.ProviderSubject) > 255 || payload.UserStatus == "" || len(payload.Issuer) > 2048 {
			return errors.New("invalid identity fields")
		}
		if schemaVersion == SchemaVersionV2 && payload.ProviderType != "oidc" {
			return errors.New("schema 2.0 identity bindings require the configured OIDC provider")
		}
		if schemaVersion == SchemaVersionV1 && payload.UserStatus != "active" && payload.UserStatus != "inactive" && payload.UserStatus != "deleted" && payload.UserStatus != "suspended" {
			return errors.New("invalid schema 1.0 identity user status")
		}
		if schemaVersion == SchemaVersionV2 && payload.UserStatus != IdentityUserStatusUnknown && payload.UserStatus != "active" && payload.UserStatus != "inactive" && payload.UserStatus != "deleted" && payload.UserStatus != "suspended" {
			return errors.New("invalid schema 2.0 identity user status")
		}
		if payload.Issuer != "" {
			issuer, err := url.Parse(payload.Issuer)
			if err != nil || issuer.Scheme != "https" || issuer.Hostname() == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
				return errors.New("invalid identity issuer")
			}
		}
		if err := validateRequiredTimes(payload.UpdatedAt); err != nil {
			return err
		}
		if err := validateOptionalTimes(payload.VerifiedAt, payload.DeletedAt); err != nil {
			return err
		}
	case EntityUsageDaily:
		var payload UsageDailyPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalUserID) || !decimalPattern.MatchString(payload.TotalCost) || !decimalPattern.MatchString(payload.TotalActualCost) || !currencyPattern.MatchString(payload.CostCurrency) {
			return errors.New("invalid usage aggregate fields")
		}
		if _, err := time.Parse("2006-01-02", payload.UsageDate); err != nil {
			return fmt.Errorf("invalid usage_date: %w", err)
		}
	case EntityUsageEvent:
		if schemaVersion != SchemaVersionV3 {
			return errors.New("usage events require schema 3.0")
		}
		var payload UsageEventPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalUserID) || !stableCursorPattern.MatchString(payload.ExternalUsageID) ||
			!serviceUnitsPattern.MatchString(payload.ServiceUnits) || payload.ServiceUnits == "0" || payload.UnitCode != unitCodeForSource(sourceType) || payload.BillingScope != "wallet" ||
			validateFactMetadata(payload.FactMetadata) != nil {
			return errors.New("invalid usage event")
		}
		return validateRequiredTimes(payload.OccurredAt)
	case EntityCreditEvent:
		if schemaVersion != SchemaVersionV3 {
			return errors.New("credit events require schema 3.0")
		}
		var payload CreditEventPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalUserID) || !stableCursorPattern.MatchString(payload.ExternalCreditID) ||
			!serviceUnitsPattern.MatchString(payload.ServiceUnits) || payload.ServiceUnits == "0" || payload.UnitCode != unitCodeForSource(sourceType) ||
			(payload.CreditKind != "bonus" && payload.CreditKind != "rebate" && payload.CreditKind != "admin" && payload.CreditKind != "unknown_positive") ||
			validateFactMetadata(payload.FactMetadata) != nil {
			return errors.New("invalid credit event")
		}
		return validateRequiredTimes(payload.OccurredAt)
	case EntityBalanceCheckpoint:
		if schemaVersion != SchemaVersionV3 {
			return errors.New("balance checkpoints require schema 3.0")
		}
		var payload BalanceCheckpointPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalUserID) || !checkpointIDPattern.MatchString(payload.CheckpointID) ||
			!serviceUnitsPattern.MatchString(payload.BalanceServiceUnits) ||
			(payload.CheckpointKind != "cutover" && payload.CheckpointKind != "reconciliation") ||
			(payload.UnitCode != "SUB2_BALANCE_1E8" && payload.UnitCode != "NEWAPI_QUOTA") || payload.UnitCode != unitCodeForSource(sourceType) ||
			!hexHashPattern.MatchString(payload.SourceSnapshotID) || !serviceUnitsPattern.MatchString(payload.SnapshotRowCount) ||
			!strings.HasPrefix(payload.CheckpointID, payload.SourceSnapshotID+":") || validateFactMetadata(payload.FactMetadata) != nil {
			return errors.New("invalid balance checkpoint")
		}
		if (payload.DeficitServiceUnits != "" && !serviceUnitsPattern.MatchString(payload.DeficitServiceUnits)) ||
			(payload.DeficitServiceUnits != "" && (payload.DeficitServiceUnits != "0") != payload.BalanceNegative) ||
			(payload.BalanceNegative && payload.BalanceServiceUnits != "0") {
			return errors.New("invalid balance checkpoint deficit")
		}
		if payload.CheckpointKind == "cutover" && !payload.BaselineMember {
			return errors.New("cutover balance checkpoint must prove baseline membership")
		}
		return validateRequiredTimes(payload.AsOf)
	case EntitySubscriptionPurchase:
		if schemaVersion != SchemaVersionV3 {
			return errors.New("subscription purchases require schema 3.0")
		}
		var payload SubscriptionPurchasePayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !externalReferencePattern.MatchString(payload.ExternalUserID) || !externalReferencePattern.MatchString(payload.ExternalOrderID) ||
			!serviceUnitsPattern.MatchString(payload.PaidMinor) || payload.PaidMinor == "0" || payload.Currency != "CNY" ||
			(payload.VerificationState != "verified" && payload.VerificationState != "pending" && payload.VerificationState != "frozen") ||
			validateFactMetadata(payload.FactMetadata) != nil {
			return errors.New("invalid subscription purchase")
		}
		return validateRequiredTimes(payload.CompletedAt)
	case EntityCutoverManifest:
		if schemaVersion != SchemaVersionV3 || streamID != StreamBalances {
			return errors.New("cutover manifest requires the schema 3.0 balances stream")
		}
		var payload CutoverManifestPayload
		if err := decodeStrict(record.Payload, &payload); err != nil {
			return err
		}
		if !uuidPattern.MatchString(payload.SourceInstanceID) || payload.SourceInstanceID != sourceID || payload.SourceRuntimeVersion != sourceRuntime || !hexHashPattern.MatchString(payload.ManifestHash) ||
			!hexHashPattern.MatchString(payload.ConfigurationHash) || !hexHashPattern.MatchString(payload.BaselineSnapshotHash) ||
			payload.UnitCode != unitCodeForSource(sourceType) || ValidateSigningKeyID(payload.SigningKeyID) != nil ||
			payload.SourceRuntimeVersion == "" || payload.ProjectionContract == "" || !serviceUnitsPattern.MatchString(payload.BaselineRowCount) ||
			payload.PaymentsCeiling == "" || payload.UsageCeiling == "" || payload.CreditsCeiling == "" || payload.BalancesCeiling == "" {
			return errors.New("invalid cutover manifest payload")
		}
		expectedHash, err := cutoverManifestPayloadHash(payload)
		if err != nil || expectedHash != payload.ManifestHash {
			return errors.New("cutover manifest canonical hash mismatch")
		}
		return validateRequiredTimes(payload.CutoverAt, payload.DatabaseClock)
	default:
		return fmt.Errorf("unsupported entity type %q for source %q", record.EntityType, sourceType)
	}
	return nil
}

func cutoverManifestPayloadHash(payload CutoverManifestPayload) (string, error) {
	payload.ManifestHash = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return SHA256Hex(raw), nil
}

func validateFactMetadata(metadata FactMetadata) error {
	if !stableCursorPattern.MatchString(metadata.SourceCursor) ||
		!hexHashPattern.MatchString(metadata.CutoverManifestHash) || !hexHashPattern.MatchString(metadata.ConfigurationHash) {
		return errors.New("invalid fact metadata")
	}
	if (metadata.CausalDomain == "") != (metadata.CausalOrder == nil) {
		return errors.New("causal domain and order must be supplied together")
	}
	if metadata.CausalOrder != nil && (!causalDomainPattern.MatchString(metadata.CausalDomain) || !serviceUnitsPattern.MatchString(*metadata.CausalOrder)) {
		return errors.New("invalid causal order")
	}
	return nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureDecodeEOF(decoder)
}

func ensureDecodeEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values are not allowed")
	}
	return err
}

func validateRequiredTimes(values ...string) error {
	for _, value := range values {
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("invalid timestamp %q: %w", value, err)
		}
	}
	return nil
}

func validateOptionalTimes(values ...*string) error {
	for _, value := range values {
		if value == nil {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, *value); err != nil {
			return fmt.Errorf("invalid timestamp %q: %w", *value, err)
		}
	}
	return nil
}

func validSourceType(sourceType string) bool {
	return sourceType == SourceSub2API || sourceType == SourceNewAPI || sourceType == SourceDesktop
}
