package sourceagent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type BatchBuilder struct {
	SchemaVersion        string
	SourceInstanceID     string
	StreamID             string
	SourceType           string
	SourceRuntimeVersion string
	AgentVersion         string
	Mode                 string
	ProjectionStatus     string
	Now                  func() time.Time
}

func (b BatchBuilder) Build(sequence uint64, previousHash string, projections []Projection) (ValidatedBatch, error) {
	return b.build(sequence, previousHash, projections, ScanCursor{})
}

// BuildPage binds V3 transport watermarks to the exact cursor transition that
// is encrypted before network I/O.  V1/V2 callers retain their established
// wire representation through Build.
func (b BatchBuilder) BuildPage(sequence uint64, previousHash string, projections []Projection, cursor ScanCursor) (ValidatedBatch, error) {
	return b.build(sequence, previousHash, projections, cursor)
}

func (b BatchBuilder) build(sequence uint64, previousHash string, projections []Projection, cursor ScanCursor) (ValidatedBatch, error) {
	if sequence == 0 || len(projections) > 500 {
		return ValidatedBatch{}, ErrInvalidBatch
	}
	schemaVersion := strings.TrimSpace(b.SchemaVersion)
	if schemaVersion == "" {
		schemaVersion = SchemaVersionV2
	}
	if schemaVersion != SchemaVersionV1 && schemaVersion != SchemaVersionV2 && schemaVersion != SchemaVersionV3 {
		return ValidatedBatch{}, errors.New("invalid batch schema version")
	}
	if schemaVersion == SchemaVersionV1 && len(b.SourceInstanceID) < 2 {
		return ValidatedBatch{}, errors.New("schema 1.0 source id must contain at least two characters")
	}
	if !sourceIDPattern.MatchString(b.SourceInstanceID) || !validSourceType(b.SourceType) || strings.TrimSpace(b.SourceRuntimeVersion) == "" || len(b.SourceRuntimeVersion) > 64 || strings.TrimSpace(b.AgentVersion) == "" || len(b.AgentVersion) > 64 {
		return ValidatedBatch{}, errors.New("invalid batch builder metadata")
	}
	if (schemaVersion == SchemaVersionV2 || schemaVersion == SchemaVersionV3) && !streamIDPattern.MatchString(b.StreamID) {
		return ValidatedBatch{}, errors.New("server schema requires a valid stream id")
	}
	if (schemaVersion == SchemaVersionV2 || schemaVersion == SchemaVersionV3) && (b.SourceType != SourceSub2API && b.SourceType != SourceNewAPI) {
		return ValidatedBatch{}, errors.New("server schema supports only registered server sources")
	}
	if (schemaVersion == SchemaVersionV2 || schemaVersion == SchemaVersionV3) && !uuidPattern.MatchString(b.SourceInstanceID) {
		return ValidatedBatch{}, errors.New("server schema source id must be a registered UUID")
	}
	if schemaVersion == SchemaVersionV2 && b.StreamID != "payments" && b.StreamID != "identities" {
		return ValidatedBatch{}, errors.New("schema 2.0 stream must be payments or identities")
	}
	if schemaVersion == SchemaVersionV1 && b.StreamID != "" {
		return ValidatedBatch{}, errors.New("schema 1.0 does not contain a stream id")
	}
	if schemaVersion == SchemaVersionV3 && b.StreamID != StreamPayments && b.StreamID != StreamUsage && b.StreamID != StreamCredits && b.StreamID != StreamBalances {
		return ValidatedBatch{}, errors.New("schema 3.0 stream must be payments, usage, credits or balances")
	}
	if schemaVersion == SchemaVersionV3 {
		if err := validateV3CursorMetadata(b.StreamID, cursor); err != nil {
			return ValidatedBatch{}, err
		}
	}
	if b.Mode != "mock" && b.Mode != "db_projection" && b.Mode != "admin_api" {
		return ValidatedBatch{}, errors.New("invalid batch mode")
	}
	projectionStatus := strings.TrimSpace(b.ProjectionStatus)
	if projectionStatus == "" {
		projectionStatus = "healthy"
	}
	if projectionStatus != "healthy" && projectionStatus != "blocked" {
		return ValidatedBatch{}, errors.New("invalid projection health status")
	}
	if schemaVersion == SchemaVersionV3 && cursor.ProjectionBlocked {
		projectionStatus = "blocked"
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	batchID, err := randomUUID()
	if err != nil {
		return ValidatedBatch{}, fmt.Errorf("create batch id: %w", err)
	}
	batch := Batch{
		SchemaVersion:        schemaVersion,
		SourceInstanceID:     b.SourceInstanceID,
		StreamID:             b.StreamID,
		SourceType:           b.SourceType,
		SourceRuntimeVersion: b.SourceRuntimeVersion,
		AgentVersion:         b.AgentVersion,
		BatchID:              batchID,
		Sequence:             sequence,
		CapturedAt:           now().UTC().Format(time.RFC3339Nano),
		Mode:                 b.Mode,
		ProjectionStatus:     projectionStatus,
		Records:              make([]Record, 0, len(projections)),
	}
	if schemaVersion == SchemaVersionV3 {
		batch.StreamWatermarkAt = cursor.WatermarkAt
		batch.SourceCursor = cursor.WatermarkCursor
		batch.ScanCeilingAt = cursor.CeilingAt
		batch.ScanCeilingCursor = cursor.CeilingCursor
		batch.ScanCycleID = cursor.ScanCycleID
		batch.ScanComplete = cursor.Completed && !cursor.ProjectionBlocked
		batch.ScanSnapshotID = cursor.SnapshotID
		if cursor.HasSnapshotMetadata {
			count := cursor.SnapshotRowCount
			batch.ScanSnapshotRowCount = &count
		}
	}
	if sequence > 1 {
		if !hexHashPattern.MatchString(previousHash) {
			return ValidatedBatch{}, ErrPreviousHashMismatch
		}
		batch.PreviousBatchHash = &previousHash
	} else if previousHash != "" {
		return ValidatedBatch{}, ErrPreviousHashMismatch
	}
	for _, projection := range projections {
		if schemaVersion == SchemaVersionV1 && (projection.EntityType == EntityPaymentCandidate || projection.EntityType == EntityPaymentAdjustment) {
			return ValidatedBatch{}, errors.New("schema 1.0 does not support candidate or adjustment records")
		}
		if schemaVersion == SchemaVersionV3 && !entityAllowedForV3Stream(b.StreamID, projection.EntityType, projection.Operation) {
			return ValidatedBatch{}, errors.New("schema 3.0 entity is not allowed on this stream")
		}
		record, err := projectionRecord(b.SourceInstanceID, projection)
		if err != nil {
			return ValidatedBatch{}, err
		}
		if schemaVersion == SchemaVersionV3 {
			if err = validateRecord(schemaVersion, b.SourceType, b.StreamID, b.SourceInstanceID, b.SourceRuntimeVersion, &record); err != nil {
				return ValidatedBatch{}, err
			}
		}
		batch.Records = append(batch.Records, record)
	}
	if schemaVersion == SchemaVersionV3 {
		for _, record := range batch.Records {
			if err := validateV3RecordMetadata(record, cursor); err != nil {
				return ValidatedBatch{}, err
			}
		}
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return ValidatedBatch{}, fmt.Errorf("marshal batch: %w", err)
	}
	if len(raw) > MaxBatchBytes {
		return ValidatedBatch{}, errors.New("batch exceeds size limit")
	}
	return ValidatedBatch{Batch: batch, RawBody: raw, BodyHash: SHA256Hex(raw)}, nil
}

func validateV3CursorMetadata(stream string, cursor ScanCursor) error {
	watermark, err := time.Parse(time.RFC3339Nano, cursor.WatermarkAt)
	if err != nil || strings.TrimSpace(cursor.WatermarkCursor) == "" || len(cursor.WatermarkCursor) > 256 {
		return errors.New("schema 3.0 requires a valid source watermark")
	}
	ceiling, err := time.Parse(time.RFC3339Nano, cursor.CeilingAt)
	if err != nil || strings.TrimSpace(cursor.CeilingCursor) == "" || len(cursor.CeilingCursor) > 256 || !uuidPattern.MatchString(cursor.ScanCycleID) {
		return errors.New("schema 3.0 requires a valid scan ceiling")
	}
	if watermark.After(ceiling) {
		return errors.New("schema 3.0 watermark exceeds scan ceiling")
	}
	if stream == StreamBalances {
		if !cursor.HasSnapshotMetadata || !hexHashPattern.MatchString(cursor.SnapshotID) || cursor.SnapshotRowCount < 0 || cursor.SnapshotRowCount > balanceSnapshotMaxRows {
			return errors.New("schema 3.0 balances stream requires a durable snapshot identity and row count")
		}
	} else if cursor.HasSnapshotMetadata || cursor.SnapshotID != "" || cursor.SnapshotRowCount != 0 {
		return errors.New("only schema 3.0 balances may carry snapshot metadata")
	}
	return nil
}

func validateV3RecordMetadata(record Record, cursor ScanCursor) error {
	if record.EntityType == EntityCutoverManifest {
		var manifest CutoverManifestPayload
		if err := json.Unmarshal(record.Payload, &manifest); err != nil || manifest.BaselineSnapshotHash != cursor.SnapshotID || manifest.BaselineRowCount != fmt.Sprint(cursor.SnapshotRowCount) {
			return errors.New("cutover manifest does not match signed scan snapshot metadata")
		}
		return nil
	}
	if record.EntityType == EntityBalanceCheckpoint {
		var checkpoint BalanceCheckpointPayload
		if err := json.Unmarshal(record.Payload, &checkpoint); err != nil || checkpoint.SourceSnapshotID != cursor.SnapshotID || checkpoint.SnapshotRowCount != fmt.Sprint(cursor.SnapshotRowCount) {
			return errors.New("balance checkpoint does not match signed scan snapshot metadata")
		}
	}
	var metadata struct {
		SourceCursor        string `json:"source_cursor"`
		CausalDomain        string `json:"causal_domain"`
		CutoverManifestHash string `json:"cutover_manifest_hash"`
		ConfigurationHash   string `json:"configuration_hash"`
	}
	if err := json.Unmarshal(record.Payload, &metadata); err != nil {
		return errors.New("schema 3.0 fact metadata is invalid")
	}
	if strings.TrimSpace(metadata.SourceCursor) == "" || len(metadata.SourceCursor) > 256 || len(metadata.CausalDomain) > 128 ||
		!hexHashPattern.MatchString(metadata.CutoverManifestHash) || !hexHashPattern.MatchString(metadata.ConfigurationHash) {
		return errors.New("schema 3.0 fact metadata is missing or inconsistent")
	}
	return nil
}

func entityAllowedForV3Stream(stream, entity, operation string) bool {
	if operation == "tombstone" {
		return false
	}
	switch stream {
	case StreamPayments:
		return entity == EntityPaymentOrder || entity == EntityPaymentCandidate || entity == EntityPaymentAdjustment || entity == EntitySubscriptionPurchase
	case StreamUsage:
		return entity == EntityUsageEvent
	case StreamCredits:
		return entity == EntityCreditEvent
	case StreamBalances:
		return entity == EntityCutoverManifest || entity == EntityBalanceCheckpoint
	default:
		return false
	}
}

func projectionRecord(sourceID string, projection Projection) (Record, error) {
	if projection.EntityType == "" || projection.ExternalID == "" || projection.Payload == nil {
		return Record{}, errors.New("invalid projection")
	}
	operation := projection.Operation
	if operation == "" {
		operation = "upsert"
	}
	if operation != "upsert" && operation != "tombstone" {
		return Record{}, errors.New("invalid projection operation")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, projection.ObservedAt)
	if err != nil {
		return Record{}, errors.New("invalid projection observed_at")
	}
	payload, err := json.Marshal(projection.Payload)
	if err != nil {
		return Record{}, fmt.Errorf("marshal projection payload: %w", err)
	}
	payloadHash, err := HashCanonicalPayload(payload)
	if err != nil {
		return Record{}, err
	}
	eventID := deterministicUUID(strings.Join([]string{sourceID, projection.EntityType, projection.ExternalID, operation, payloadHash}, "\x00"))
	return Record{
		EventID:       eventID,
		Operation:     operation,
		ObservedAt:    observedAt.UTC().Format(time.RFC3339Nano),
		PayloadSHA256: payloadHash,
		EntityType:    projection.EntityType,
		Payload:       payload,
	}, nil
}

func randomUUID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return formatUUID(bytes), nil
}

func deterministicUUID(value string) string {
	digest := sha256Bytes([]byte(value))
	var bytes [16]byte
	copy(bytes[:], digest[:16])
	// RFC 9562 UUIDv8: application-defined bytes with standard variant.
	bytes[6] = (bytes[6] & 0x0f) | 0x80
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return formatUUID(bytes)
}

func formatUUID(value [16]byte) string {
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}

// SequenceState advances only after the receiver acknowledges the exact
// batch. CompareAndSwap lets a durable implementation reject competing agent
// processes without allowing either process to skip a sequence.
type SequenceState struct {
	Revision      uint64 `json:"-"`
	Sequence      uint64 `json:"sequence"`
	LastBatchHash string `json:"last_batch_hash"`
}

type SequenceStore interface {
	Load(context.Context) (SequenceState, error)
	CompareAndSwap(context.Context, SequenceState, SequenceState) (bool, error)
}

type MemorySequenceStore struct {
	mu    sync.Mutex
	state SequenceState
}

func (s *MemorySequenceStore) Load(context.Context) (SequenceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *MemorySequenceStore) CompareAndSwap(_ context.Context, oldState, newState SequenceState) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != oldState {
		return false, nil
	}
	if newState.Sequence != oldState.Sequence+1 || !hexHashPattern.MatchString(newState.LastBatchHash) {
		return false, errors.New("invalid sequence transition")
	}
	newState.Revision = oldState.Revision + 1
	s.state = newState
	return true, nil
}

type Publisher struct {
	Builder BatchBuilder
	Store   SequenceStore
	Client  IngestClient
	Pending PendingBatchStore
}

func (p *Publisher) Publish(ctx context.Context, projections []Projection) (IngestAck, error) {
	if p == nil || p.Store == nil || p.Client == nil {
		return IngestAck{}, errors.New("publisher is not configured")
	}
	if p.Pending != nil {
		return IngestAck{}, errors.New("durable publisher requires page cursor metadata")
	}
	state, err := p.Store.Load(ctx)
	if err != nil {
		return IngestAck{}, err
	}
	batch, err := p.Builder.Build(state.Sequence+1, state.LastBatchHash, projections)
	if err != nil {
		return IngestAck{}, err
	}
	ack, err := p.Client.Send(ctx, batch)
	if err != nil {
		return IngestAck{}, err
	}
	if !ack.Accepted || ack.SourceInstanceID != batch.Batch.SourceInstanceID || ack.StreamID != batch.Batch.StreamID || ack.BatchID != batch.Batch.BatchID || ack.Sequence != batch.Batch.Sequence || ack.AcceptedRecords != len(batch.Batch.Records) {
		return IngestAck{}, errors.New("ingestion acknowledgement mismatch")
	}
	if ack.Duplicate {
		// A duplicate means the receiver already committed this exact batch. It is
		// still safe and necessary to advance the local cursor after validation.
	}
	advanced, err := p.Store.CompareAndSwap(ctx, state, SequenceState{Revision: state.Revision, Sequence: batch.Batch.Sequence, LastBatchHash: batch.BodyHash})
	if err != nil {
		return IngestAck{}, err
	}
	if !advanced {
		return IngestAck{}, errors.New("sequence state changed concurrently")
	}
	return ack, nil
}

type PublishReceipt struct {
	Ack          IngestAck
	CursorBefore ScanCursor
	CursorAfter  ScanCursor
	bodyHash     string
}

// PublishPage persists a complete encrypted pending batch before network I/O.
// It intentionally leaves the spool in place after a validated ACK; the
// coordinator clears it only after committing the matching source cursor.
func (p *Publisher) PublishPage(ctx context.Context, cursorBefore, cursorAfter ScanCursor, projections []Projection) (PublishReceipt, error) {
	if p == nil || p.Store == nil || p.Client == nil || p.Pending == nil {
		return PublishReceipt{}, errors.New("durable page publisher is not configured")
	}
	if err := validateStoredFileCursor(cursorBefore); err != nil {
		return PublishReceipt{}, err
	}
	if err := validateStoredFileCursor(cursorAfter); err != nil || cursorAfter.Revision != cursorBefore.Revision {
		return PublishReceipt{}, errors.New("invalid pending page cursor transition")
	}

	for attempt := 0; attempt < 2; attempt++ {
		state, err := p.Store.Load(ctx)
		if err != nil {
			return PublishReceipt{}, err
		}
		pending, exists, err := p.Pending.Load(ctx)
		if err != nil {
			return PublishReceipt{}, err
		}
		if exists {
			if pending.CursorBefore != cursorBefore {
				if cursorMatchesCommitted(pending.CursorAfter, cursorBefore) && state.Sequence == pending.Batch.Batch.Sequence && state.LastBatchHash == pending.Batch.BodyHash {
					if err := p.Pending.Clear(ctx, pending.Batch.BodyHash); err != nil {
						pending.Destroy()
						return PublishReceipt{}, err
					}
					pending.Destroy()
					continue
				}
				pending.Destroy()
				return PublishReceipt{}, errors.New("pending batch does not match the loaded source cursor")
			}
			receipt, err := p.resumePending(ctx, state, pending)
			pending.Destroy()
			return receipt, err
		}

		batch, err := p.Builder.BuildPage(state.Sequence+1, state.LastBatchHash, projections, cursorAfter)
		if err != nil {
			return PublishReceipt{}, err
		}
		pending = PendingBatch{Batch: batch, CursorBefore: cursorBefore, CursorAfter: cursorAfter}
		stored, err := p.Pending.SaveIfAbsent(ctx, pending)
		if err != nil {
			return PublishReceipt{}, err
		}
		if !stored {
			continue
		}
		receipt, err := p.resumePending(ctx, state, pending)
		pending.Destroy()
		return receipt, err
	}
	return PublishReceipt{}, errors.New("pending batch changed concurrently")
}

func (p *Publisher) resumePending(ctx context.Context, state SequenceState, pending PendingBatch) (PublishReceipt, error) {
	batch := pending.Batch
	expectedSchema := strings.TrimSpace(p.Builder.SchemaVersion)
	if expectedSchema == "" {
		expectedSchema = SchemaVersionV2
	}
	if batch.Batch.SourceInstanceID != p.Builder.SourceInstanceID || batch.Batch.StreamID != p.Builder.StreamID || batch.Batch.SourceType != p.Builder.SourceType ||
		(batch.Batch.SchemaVersion != SchemaVersionV2 && batch.Batch.SchemaVersion != SchemaVersionV3) || batch.Batch.SchemaVersion != expectedSchema {
		return PublishReceipt{}, errors.New("pending batch metadata does not match publisher")
	}
	if state.Sequence == batch.Batch.Sequence {
		if state.LastBatchHash != batch.BodyHash {
			return PublishReceipt{}, errors.New("pending batch conflicts with committed sequence state")
		}
		// Sequence state advances only after a validated ACK, so this is a crash
		// recovery proof and does not require another network request.
		return PublishReceipt{
			Ack: IngestAck{
				Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
				StreamID: batch.Batch.StreamID, BatchID: batch.Batch.BatchID,
				Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records),
				Duplicate: true,
			},
			CursorBefore: pending.CursorBefore, CursorAfter: pending.CursorAfter,
			bodyHash: batch.BodyHash,
		}, nil
	}
	if state.Sequence+1 != batch.Batch.Sequence || state.LastBatchHash != previousHashValue(batch.Batch.PreviousBatchHash) {
		return PublishReceipt{}, errors.New("pending batch sequence does not follow committed state")
	}
	ack, err := p.Client.Send(ctx, batch)
	if err != nil {
		return PublishReceipt{}, err
	}
	if !ack.Accepted || ack.SourceInstanceID != batch.Batch.SourceInstanceID || ack.StreamID != batch.Batch.StreamID || ack.BatchID != batch.Batch.BatchID || ack.Sequence != batch.Batch.Sequence || ack.AcceptedRecords != len(batch.Batch.Records) {
		return PublishReceipt{}, errors.New("ingestion acknowledgement mismatch")
	}
	advanced, err := p.Store.CompareAndSwap(ctx, state, SequenceState{
		Revision: state.Revision, Sequence: batch.Batch.Sequence, LastBatchHash: batch.BodyHash,
	})
	if err != nil {
		return PublishReceipt{}, err
	}
	if !advanced {
		return PublishReceipt{}, errors.New("sequence state changed concurrently")
	}
	return PublishReceipt{
		Ack: ack, CursorBefore: pending.CursorBefore, CursorAfter: pending.CursorAfter,
		bodyHash: batch.BodyHash,
	}, nil
}

func (p *Publisher) FinalizePage(ctx context.Context, receipt PublishReceipt) error {
	if p == nil || p.Pending == nil || !hexHashPattern.MatchString(receipt.bodyHash) {
		return errors.New("invalid durable publish receipt")
	}
	return p.Pending.Clear(ctx, receipt.bodyHash)
}

func previousHashValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cursorMatchesCommitted(pendingAfter, current ScanCursor) bool {
	if current.Revision != pendingAfter.Revision+1 {
		return false
	}
	current.Revision = pendingAfter.Revision
	return current == pendingAfter
}
