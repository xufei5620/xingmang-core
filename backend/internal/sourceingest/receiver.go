package sourceingest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/postgresstore"
)

const (
	SourceBatchPath = "/internal/v1/source-batches"
	maxBatchBytes   = 4 << 20
)

type Acceptor interface {
	AcceptSourceBatch(context.Context, string, application.VerifiedSourceBatch) (application.SourceBatchAck, error)
}

type Receiver struct {
	Trust       *TrustStore
	Acceptor    Acceptor
	ProxyCIDRs  []*net.IPNet
	MaximumSkew time.Duration
	Now         func() time.Time
}

type batchV2 struct {
	SchemaVersion        string     `json:"schema_version"`
	SourceInstanceID     string     `json:"source_instance_id"`
	StreamID             string     `json:"stream_id"`
	SourceType           string     `json:"source_type"`
	SourceRuntimeVersion string     `json:"source_runtime_version"`
	AgentVersion         string     `json:"agent_version"`
	BatchID              string     `json:"batch_id"`
	Sequence             int64      `json:"sequence"`
	PreviousBatchHash    *string    `json:"previous_batch_hash"`
	CapturedAt           string     `json:"captured_at"`
	Mode                 string     `json:"mode"`
	ProjectionStatus     string     `json:"projection_status"`
	StreamWatermarkAt    string     `json:"stream_watermark_at,omitempty"`
	SourceCursor         string     `json:"source_cursor,omitempty"`
	ScanCeilingAt        string     `json:"scan_ceiling_at,omitempty"`
	ScanCeilingCursor    string     `json:"scan_ceiling_cursor,omitempty"`
	ScanCycleID          string     `json:"scan_cycle_id,omitempty"`
	ScanComplete         bool       `json:"scan_complete,omitempty"`
	ScanSnapshotID       string     `json:"scan_snapshot_id,omitempty"`
	ScanSnapshotRowCount *int64     `json:"scan_snapshot_row_count,omitempty"`
	Records              []recordV2 `json:"records"`
}

type recordV2 struct {
	EventID       string          `json:"event_id"`
	Operation     string          `json:"operation"`
	ObservedAt    string          `json:"observed_at"`
	PayloadSHA256 string          `json:"payload_sha256"`
	EntityType    string          `json:"entity_type"`
	Payload       json.RawMessage `json:"payload"`
}

func ParseProxyCIDRs(values []string) ([]*net.IPNet, error) {
	networks := make([]*net.IPNet, 0, len(values))
	for _, raw := range values {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		ones, bits := 0, 0
		if network != nil {
			ones, bits = network.Mask.Size()
		}
		if err != nil || network.IP.IsUnspecified() || network.IP.IsMulticast() || network.IP.IsLinkLocalUnicast() || (bits == 32 && ones < 24) || (bits == 128 && ones < 64) {
			return nil, fmt.Errorf("unsafe ingestion proxy CIDR %q", raw)
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return nil, errors.New("ingestion proxy CIDR allowlist is empty")
	}
	return networks, nil
}

func (receiver *Receiver) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if receiver == nil || receiver.Trust == nil || receiver.Acceptor == nil || request.Method != http.MethodPost || request.URL.Path != SourceBatchPath || !containsRemote(receiver.ProxyCIDRs, request.RemoteAddr) {
		writeReceiverError(writer, http.StatusNotFound, "SOURCE_INGEST_NOT_FOUND")
		return
	}
	if singleHeader(request, "X-Invoice-mTLS-Verified") != "SUCCESS" {
		writeReceiverError(writer, http.StatusForbidden, "SOURCE_MTLS_REQUIRED")
		return
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeReceiverError(writer, http.StatusUnsupportedMediaType, "SOURCE_CONTENT_TYPE_REJECTED")
		return
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentTypes[0], ";")[0])); mediaType != "application/json" {
		writeReceiverError(writer, http.StatusUnsupportedMediaType, "SOURCE_CONTENT_TYPE_REJECTED")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBatchBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBatchBytes {
		writeReceiverError(writer, http.StatusRequestEntityTooLarge, "SOURCE_BATCH_SIZE_REJECTED")
		return
	}
	metadata, err := receiver.verifyEnvelope(request, body)
	if err != nil {
		writeReceiverError(writer, http.StatusForbidden, "SOURCE_SIGNATURE_REJECTED")
		return
	}
	batch, err := decodeBatch(body)
	if err != nil || batch.SourceInstanceID != metadata.sourceID || batch.StreamID != metadata.streamID || batch.BatchID != metadata.batchID || batch.Sequence != metadata.sequence {
		writeReceiverError(writer, http.StatusUnprocessableEntity, "SOURCE_BATCH_REJECTED")
		return
	}
	if _, ok := receiver.Trust.resolve(batch.SourceInstanceID, batch.StreamID, batch.SourceType, metadata.serial, metadata.keyID); !ok {
		writeReceiverError(writer, http.StatusForbidden, "SOURCE_TRUST_REJECTED")
		return
	}
	events := make([]application.VerifiedSourceBatchEvent, 0, len(batch.Records))
	receiverNow := time.Now().UTC()
	if receiver.Now != nil {
		receiverNow = receiver.Now().UTC()
	}
	maximumSkew := receiver.MaximumSkew
	if maximumSkew <= 0 {
		maximumSkew = 5 * time.Minute
	}
	for _, record := range batch.Records {
		observedAt, parseErr := time.Parse(time.RFC3339Nano, record.ObservedAt)
		if parseErr != nil || observedAt.After(receiverNow.Add(maximumSkew)) {
			writeReceiverError(writer, http.StatusUnprocessableEntity, "SOURCE_BATCH_REJECTED")
			return
		}
		events = append(events, application.VerifiedSourceBatchEvent{
			EventID: record.EventID, EntityType: record.EntityType, Operation: record.Operation,
			PayloadHash: record.PayloadSHA256, Payload: append([]byte(nil), record.Payload...), ObservedAt: observedAt,
		})
	}
	previous := ""
	if batch.PreviousBatchHash != nil {
		previous = *batch.PreviousBatchHash
	}
	capturedAt, err := time.Parse(time.RFC3339Nano, batch.CapturedAt)
	if err != nil || capturedAt.After(receiverNow.Add(maximumSkew)) {
		writeReceiverError(writer, http.StatusUnprocessableEntity, "SOURCE_BATCH_REJECTED")
		return
	}
	var streamWatermarkAt, scanCeilingAt time.Time
	if batch.SchemaVersion == "3.0" {
		streamWatermarkAt, err = time.Parse(time.RFC3339Nano, batch.StreamWatermarkAt)
		if err != nil {
			writeReceiverError(writer, http.StatusUnprocessableEntity, "SOURCE_BATCH_REJECTED")
			return
		}
		scanCeilingAt, err = time.Parse(time.RFC3339Nano, batch.ScanCeilingAt)
		if err != nil || streamWatermarkAt.After(scanCeilingAt) || scanCeilingAt.After(receiverNow.Add(maximumSkew)) {
			writeReceiverError(writer, http.StatusUnprocessableEntity, "SOURCE_BATCH_REJECTED")
			return
		}
	}
	auditContext := application.WithAuditActor(request.Context(), postgresstore.AuditActor{
		Type: "source_connector", ID: batch.SourceInstanceID,
		RequestID: singleHeader(request, "X-Request-ID"), Reason: "mTLS and Ed25519 verified source batch",
	})
	ack, err := receiver.Acceptor.AcceptSourceBatch(auditContext, batch.SourceInstanceID, application.VerifiedSourceBatch{
		SchemaVersion:    batch.SchemaVersion,
		SourceInstanceID: batch.SourceInstanceID, StreamID: batch.StreamID, BatchID: batch.BatchID,
		Sequence: batch.Sequence, BodyHash: metadata.bodyHash, PreviousBatchHash: previous,
		SigningKeyID: metadata.keyID, SourceRuntimeVersion: batch.SourceRuntimeVersion,
		SourceAgentVersion: batch.AgentVersion, SourceCapturedAt: capturedAt,
		ProjectionStatus: batch.ProjectionStatus, Events: events,
		StreamWatermarkAt: streamWatermarkAt, SourceCursor: batch.SourceCursor,
		ScanCeilingAt: scanCeilingAt, ScanCeilingCursor: batch.ScanCeilingCursor,
		ScanCycleID: batch.ScanCycleID, ScanComplete: batch.ScanComplete,
		ScanSnapshotID: batch.ScanSnapshotID, ScanSnapshotRowCount: snapshotRowCount(batch.ScanSnapshotRowCount),
	})
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusServiceUnavailable
		}
		writeReceiverError(writer, status, "SOURCE_BATCH_COMMIT_REJECTED")
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(ack)
}

type verifiedMetadata struct {
	sourceID, streamID, batchID, bodyHash, keyID, serial string
	sequence                                             int64
}

func (receiver *Receiver) verifyEnvelope(request *http.Request, body []byte) (verifiedMetadata, error) {
	metadata := verifiedMetadata{
		sourceID: singleHeader(request, "X-Source-ID"), streamID: singleHeader(request, "X-Stream-ID"),
		batchID: singleHeader(request, "X-Batch-ID"), bodyHash: singleHeader(request, "X-Content-SHA256"),
		keyID: singleHeader(request, "X-Signature-Key-ID"), serial: singleHeader(request, "X-Invoice-Client-Cert-Serial"),
	}
	sequence, err := strconv.ParseInt(singleHeader(request, "X-Sequence"), 10, 64)
	if err != nil || sequence <= 0 {
		return metadata, errors.New("invalid sequence")
	}
	metadata.sequence = sequence
	if !uuidPattern.MatchString(metadata.sourceID) || !streamPattern.MatchString(metadata.streamID) || !uuidPattern.MatchString(metadata.batchID) || !hexPattern.MatchString(metadata.bodyHash) || !keyIDPattern.MatchString(metadata.keyID) || !serialPattern.MatchString(strings.ToUpper(metadata.serial)) {
		return metadata, errors.New("invalid signature metadata")
	}
	sum := sha256.Sum256(body)
	expected, _ := hex.DecodeString(metadata.bodyHash)
	if len(expected) != len(sum) || subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return metadata, errors.New("body hash mismatch")
	}
	sentAtRaw := singleHeader(request, "X-Sent-At")
	sentAt, err := time.Parse(time.RFC3339Nano, sentAtRaw)
	if err != nil {
		return metadata, errors.New("invalid sent time")
	}
	now := time.Now().UTC()
	if receiver.Now != nil {
		now = receiver.Now().UTC()
	}
	skew := receiver.MaximumSkew
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	delta := now.Sub(sentAt.UTC())
	if delta < -skew || delta > skew {
		return metadata, errors.New("signature timestamp outside allowed skew")
	}
	var batchHeader struct {
		SourceType string `json:"source_type"`
	}
	if err = json.Unmarshal(body, &batchHeader); err != nil {
		return metadata, err
	}
	publicKey, ok := receiver.Trust.resolve(metadata.sourceID, metadata.streamID, batchHeader.SourceType, metadata.serial, metadata.keyID)
	if !ok {
		return metadata, errors.New("unknown source signing key")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(singleHeader(request, "X-Signature"))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return metadata, errors.New("invalid signature encoding")
	}
	canonical := strings.Join([]string{http.MethodPost, SourceBatchPath, metadata.sourceID, metadata.streamID, metadata.batchID, strconv.FormatInt(sequence, 10), sentAtRaw, metadata.bodyHash}, "\n")
	if !ed25519.Verify(publicKey, []byte(canonical), signature) {
		return metadata, errors.New("invalid source signature")
	}
	return metadata, nil
}

func decodeBatch(body []byte) (batchV2, error) {
	parsed, err := parseUniqueJSON(body)
	if err != nil {
		return batchV2{}, err
	}
	object, ok := parsed.(map[string]any)
	if !ok || !hasKeys(object, "schema_version", "source_instance_id", "stream_id", "source_type", "source_runtime_version", "agent_version", "batch_id", "sequence", "previous_batch_hash", "captured_at", "mode", "projection_status", "records") {
		return batchV2{}, errors.New("required batch fields are missing")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var batch batchV2
	if err := decoder.Decode(&batch); err != nil {
		return batchV2{}, err
	}
	if (batch.SchemaVersion != "2.0" && batch.SchemaVersion != "3.0") || !uuidPattern.MatchString(batch.SourceInstanceID) ||
		(batch.SourceType != "sub2api" && batch.SourceType != "newapi") || !uuidPattern.MatchString(batch.BatchID) ||
		batch.Sequence <= 0 || len(batch.Records) > 500 || strings.TrimSpace(batch.SourceRuntimeVersion) == "" ||
		len(batch.SourceRuntimeVersion) > 128 || strings.TrimSpace(batch.AgentVersion) == "" || len(batch.AgentVersion) > 128 ||
		batch.Mode != "db_projection" || (batch.ProjectionStatus != "healthy" && batch.ProjectionStatus != "blocked") {
		return batchV2{}, errors.New("invalid batch metadata")
	}
	if batch.SchemaVersion == "2.0" {
		if batch.StreamID != "payments" && batch.StreamID != "identities" || batch.StreamWatermarkAt != "" ||
			batch.SourceCursor != "" || batch.ScanCeilingAt != "" || batch.ScanCeilingCursor != "" ||
			batch.ScanCycleID != "" || batch.ScanComplete || batch.ScanSnapshotID != "" || batch.ScanSnapshotRowCount != nil {
			return batchV2{}, errors.New("v3 metadata is forbidden in v2 batch")
		}
	} else {
		if batch.StreamID != "payments" && batch.StreamID != "usage" && batch.StreamID != "credits" && batch.StreamID != "balances" ||
			!uuidPattern.MatchString(batch.ScanCycleID) || strings.TrimSpace(batch.SourceCursor) == "" || len(batch.SourceCursor) > 256 ||
			strings.TrimSpace(batch.ScanCeilingCursor) == "" || len(batch.ScanCeilingCursor) > 256 ||
			!hasKeys(object, "stream_watermark_at", "source_cursor", "scan_ceiling_at", "scan_ceiling_cursor", "scan_cycle_id", "scan_complete") {
			return batchV2{}, errors.New("invalid v3 scan metadata")
		}
		if batch.StreamID == "balances" {
			if !hexPattern.MatchString(batch.ScanSnapshotID) || batch.ScanSnapshotRowCount == nil ||
				*batch.ScanSnapshotRowCount < 0 || *batch.ScanSnapshotRowCount > 2_000_000 ||
				!hasKeys(object, "scan_snapshot_id", "scan_snapshot_row_count") {
				return batchV2{}, errors.New("invalid balances snapshot metadata")
			}
		} else if batch.ScanSnapshotID != "" || batch.ScanSnapshotRowCount != nil {
			return batchV2{}, errors.New("snapshot metadata is restricted to balances stream")
		}
		watermark, watermarkErr := time.Parse(time.RFC3339Nano, batch.StreamWatermarkAt)
		ceiling, ceilingErr := time.Parse(time.RFC3339Nano, batch.ScanCeilingAt)
		if watermarkErr != nil || ceilingErr != nil || watermark.After(ceiling) {
			return batchV2{}, errors.New("invalid v3 scan times")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, batch.CapturedAt); err != nil {
		return batchV2{}, err
	}
	if batch.Sequence == 1 && batch.PreviousBatchHash != nil || batch.Sequence > 1 && (batch.PreviousBatchHash == nil || !hexPattern.MatchString(*batch.PreviousBatchHash)) {
		return batchV2{}, errors.New("invalid previous batch hash")
	}
	seen := map[string]struct{}{}
	recordValues, _ := object["records"].([]any)
	if len(recordValues) != len(batch.Records) {
		return batchV2{}, errors.New("invalid records array")
	}
	for index, record := range batch.Records {
		recordObject, ok := recordValues[index].(map[string]any)
		if !ok || !hasKeys(recordObject, "event_id", "operation", "observed_at", "payload_sha256", "entity_type", "payload") {
			return batchV2{}, errors.New("required source record fields are missing")
		}
		if !uuidPattern.MatchString(record.EventID) || (record.Operation != "upsert" && record.Operation != "tombstone") || !hexPattern.MatchString(record.PayloadSHA256) || len(record.Payload) == 0 || len(record.Payload) > 1<<20 {
			return batchV2{}, errors.New("invalid source record")
		}
		if _, duplicate := seen[record.EventID]; duplicate {
			return batchV2{}, errors.New("duplicate source event")
		}
		seen[record.EventID] = struct{}{}
		if _, err := time.Parse(time.RFC3339Nano, record.ObservedAt); err != nil {
			return batchV2{}, err
		}
		if !validEntityForStream(batch.SchemaVersion, batch.StreamID, record.EntityType, record.Operation) {
			return batchV2{}, errors.New("entity type is invalid for stream")
		}
		if batch.SchemaVersion == "3.0" && batch.StreamID == "balances" && record.EntityType == "balance_checkpoint" {
			var snapshot struct {
				SourceSnapshotID string `json:"source_snapshot_id"`
				SnapshotRowCount string `json:"snapshot_row_count"`
				BaselineMember   *bool  `json:"baseline_member"`
			}
			if err := json.Unmarshal(record.Payload, &snapshot); err != nil || snapshot.BaselineMember == nil ||
				snapshot.SourceSnapshotID != batch.ScanSnapshotID {
				return batchV2{}, errors.New("balance fact does not match signed scan snapshot")
			}
			rowCount, parseErr := strconv.ParseInt(snapshot.SnapshotRowCount, 10, 64)
			if parseErr != nil || batch.ScanSnapshotRowCount == nil || rowCount != *batch.ScanSnapshotRowCount {
				return batchV2{}, errors.New("balance fact row count does not match signed scan snapshot")
			}
		}
		if batch.SchemaVersion == "3.0" && batch.StreamID == "balances" && record.EntityType == "cutover_manifest" {
			var manifest struct {
				BaselineSnapshotHash string `json:"baseline_snapshot_hash"`
				BaselineRowCount     string `json:"baseline_row_count"`
			}
			if err := json.Unmarshal(record.Payload, &manifest); err != nil ||
				manifest.BaselineSnapshotHash != batch.ScanSnapshotID {
				return batchV2{}, errors.New("cutover manifest does not match signed scan snapshot")
			}
			rowCount, parseErr := strconv.ParseInt(manifest.BaselineRowCount, 10, 64)
			if parseErr != nil || batch.ScanSnapshotRowCount == nil || rowCount != *batch.ScanSnapshotRowCount {
				return batchV2{}, errors.New("cutover manifest row count does not match signed scan snapshot")
			}
		}
		canonical, err := canonicalPayload(record.Payload)
		if err != nil {
			return batchV2{}, err
		}
		hash := sha256.Sum256(canonical)
		if hex.EncodeToString(hash[:]) != record.PayloadSHA256 {
			return batchV2{}, errors.New("payload hash mismatch")
		}
	}
	return batch, nil
}

func hasKeys(object map[string]any, names ...string) bool {
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}

func snapshotRowCount(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func validEntityForStream(schemaVersion, streamID, entityType, operation string) bool {
	if schemaVersion == "3.0" {
		if operation != "upsert" {
			return false
		}
		switch streamID {
		case "usage":
			return entityType == "usage_event"
		case "credits":
			return entityType == "credit_event"
		case "balances":
			return entityType == "balance_checkpoint" || entityType == "cutover_manifest"
		case "payments":
			return entityType == "payment_order" || entityType == "payment_candidate" ||
				entityType == "payment_adjustment" || entityType == "subscription_purchase"
		}
		return false
	}
	if operation == "tombstone" {
		return entityType == "payment_order" || entityType == "identity_binding"
	}
	if streamID == "identities" {
		return entityType == "identity_binding"
	}
	return entityType == "payment_order" || entityType == "payment_candidate" || entityType == "payment_adjustment"
}

func singleHeader(request *http.Request, name string) string {
	values := request.Header.Values(name)
	if len(values) != 1 || strings.ContainsAny(values[0], "\r\n\x00") {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func containsRemote(networks []*net.IPNet, remoteAddress string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		host = strings.TrimSpace(remoteAddress)
	}
	ip := net.ParseIP(host)
	for _, network := range networks {
		if ip != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func writeReceiverError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": code, "message": "source batch rejected"}})
}
