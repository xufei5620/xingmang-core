package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

func nonNilSummary(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func archiveEventFromAudit(event audit.Event) ArchiveEventV1 {
	return ArchiveEventV1{
		ID: event.ID.String(), Sequence: event.Sequence,
		OccurredAt: NewWireTime(event.OccurredAt), RecordedAt: NewWireTime(event.RecordedAt),
		PrincipalID: event.PrincipalID, PrincipalType: string(event.PrincipalType),
		ActionID: event.ActionID, ActionVersion: event.ActionVersion,
		ActionRunID: event.ActionRunID.String(), ResourceType: event.ResourceType,
		ResourceID: event.ResourceID, Environment: event.Environment, Reason: event.Reason,
		ApprovalID: event.ApprovalID, RequestID: event.RequestID, TraceID: event.TraceID,
		SourceIP: event.SourceIP, BeforeSummary: nonNilSummary(event.BeforeSummary),
		AfterSummary:             nonNilSummary(event.AfterSummary),
		ConnectorRequestSummary:  nonNilSummary(event.ConnectorRequestSummary),
		ConnectorResponseSummary: nonNilSummary(event.ConnectorResponseSummary),
		Result:                   string(event.Result), CompensationResult: event.CompensationResult,
		PrevHash: event.PrevHash, EventHash: event.EventHash,
		CanonicalVersion: event.CanonicalVersion,
	}
}

func validateEventForEncode(event audit.Event) error {
	if event.CanonicalVersion != audit.CanonicalV1 && event.CanonicalVersion != audit.CanonicalV2 {
		if event.CanonicalVersion > audit.CanonicalV2 {
			return &CompatibilityError{
				Code: CompatibilityVerifierOutdated, FormatVersion: FormatVersion,
				CanonicalVersion: int(event.CanonicalVersion),
			}
		}
		return &FormatError{Code: "archive_format_invalid"}
	}
	if event.Sequence < 1 || event.OccurredAt.IsZero() || event.RecordedAt.IsZero() {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if !validPrincipalType(string(event.PrincipalType)) || !validEnvironment(event.Environment) ||
		(event.Result != audit.ResultSucceeded && event.Result != audit.ResultFailed) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	wantHash, err := event.ComputeHash()
	if err != nil {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	if wantHash != event.EventHash {
		return &FormatError{Code: "event_hash_mismatch"}
	}
	return nil
}

func EncodePayload(writer io.Writer, events []audit.Event) (EncodedObjectV1, error) {
	hasher := sha256.New()
	counting := &countingWriter{writer: io.MultiWriter(writer, hasher)}
	for index, event := range events {
		if err := validateEventForEncode(event); err != nil {
			return EncodedObjectV1{}, fmt.Errorf("payload row %d: %w", index+1, err)
		}
		encoded, err := json.Marshal(archiveEventFromAudit(event))
		if err != nil {
			return EncodedObjectV1{}, &FormatError{Code: "archive_format_invalid", Cause: err}
		}
		encoded = append(encoded, '\n')
		if _, err := counting.Write(encoded); err != nil {
			return EncodedObjectV1{}, err
		}
	}
	return EncodedObjectV1{
		SHA256: hex.EncodeToString(hasher.Sum(nil)), SizeBytes: counting.count,
		ContentType: PayloadContentType, RowCount: int64(len(events)),
	}, nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(value []byte) (int, error) {
	written, err := w.writer.Write(value)
	w.count += int64(written)
	return written, err
}

func summaryOrNull(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func projectionFromAudit(event audit.Event) ProjectionEventV1 {
	return ProjectionEventV1{
		Sequence: event.Sequence, OccurredAt: event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		PrincipalID: event.PrincipalID, PrincipalType: string(event.PrincipalType),
		ActionID: event.ActionID, ActionVersion: event.ActionVersion,
		ActionRunID: event.ActionRunID.String(), ResourceType: event.ResourceType,
		ResourceID: event.ResourceID, Environment: event.Environment, RequestID: event.RequestID,
		Result: string(event.Result), ErrorCode: event.CompensationResult,
		BeforeSummary: summaryOrNull(event.BeforeSummary), AfterSummary: summaryOrNull(event.AfterSummary),
		EventHash: event.EventHash, PrevHash: event.PrevHash,
	}
}

func EncodeProjection(writer io.Writer, environment string, events []audit.Event) (EncodedObjectV1, error) {
	if !validEnvironment(environment) {
		return EncodedObjectV1{}, &FormatError{Code: "archive_format_invalid"}
	}
	hasher := sha256.New()
	counting := &countingWriter{writer: io.MultiWriter(writer, hasher)}
	var rows int64
	for _, event := range events {
		if event.Environment != environment {
			continue
		}
		if err := validateEventForEncode(event); err != nil {
			return EncodedObjectV1{}, err
		}
		encoded, err := json.Marshal(projectionFromAudit(event))
		if err != nil {
			return EncodedObjectV1{}, err
		}
		encoded = append(encoded, '\n')
		if _, err := counting.Write(encoded); err != nil {
			return EncodedObjectV1{}, err
		}
		rows++
	}
	return EncodedObjectV1{
		SHA256: hex.EncodeToString(hasher.Sum(nil)), SizeBytes: counting.count,
		ContentType: ProjectionContentType, RowCount: rows,
	}, nil
}

func SegmentPayloads(events []audit.Event, maxRows, maxBytes int64) ([]EncodedSegmentV1, error) {
	if maxRows <= 0 || maxBytes <= 0 {
		return nil, fmt.Errorf("segment limits must be positive")
	}
	segments := make([]EncodedSegmentV1, 0)
	current := make([]audit.Event, 0)
	var currentBytes int64
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		var output bytes.Buffer
		encoded, err := EncodePayload(&output, current)
		if err != nil {
			return err
		}
		copiedEvents := append([]audit.Event(nil), current...)
		copiedBytes := append([]byte(nil), output.Bytes()...)
		oversized := int64(0)
		if len(current) == 1 && encoded.SizeBytes > maxBytes {
			oversized = 1
		}
		segments = append(segments, EncodedSegmentV1{
			Events: copiedEvents, Bytes: copiedBytes, Encoded: encoded,
			Boundary: SegmentBoundary{
				FromSequence: current[0].Sequence, ToSequence: current[len(current)-1].Sequence,
				RowCount: int64(len(current)), FirstPrevHash: current[0].PrevHash,
				FirstEventHash: current[0].EventHash, LastEventHash: current[len(current)-1].EventHash,
			},
			OversizedRecordCount: oversized,
		})
		current = current[:0]
		currentBytes = 0
		return nil
	}
	for _, event := range events {
		if err := validateEventForEncode(event); err != nil {
			return nil, err
		}
		line, err := json.Marshal(archiveEventFromAudit(event))
		if err != nil {
			return nil, err
		}
		lineBytes := int64(len(line) + 1)
		if len(current) > 0 && (int64(len(current)) >= maxRows || currentBytes+lineBytes > maxBytes) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		current = append(current, event)
		currentBytes += lineBytes
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return segments, nil
}
