package archive

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func decodeCanonicalLine[T any](line []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, line) {
		return fmt.Errorf("non-canonical JSON bytes")
	}
	return nil
}

func archiveEventToAudit(wire ArchiveEventV1) (audit.Event, error) {
	if wire.CanonicalVersion == 0 {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid"}
	}
	if wire.CanonicalVersion != audit.CanonicalV1 && wire.CanonicalVersion != audit.CanonicalV2 {
		return audit.Event{}, &CompatibilityError{
			Code: CompatibilityVerifierOutdated, FormatVersion: FormatVersion,
			CanonicalVersion: int(wire.CanonicalVersion),
		}
	}
	if wire.BeforeSummary == nil || wire.AfterSummary == nil || wire.ConnectorRequestSummary == nil ||
		wire.ConnectorResponseSummary == nil {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid"}
	}
	id, err := uuid.Parse(wire.ID)
	if err != nil || id.String() != wire.ID {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid"}
	}
	actionRunID, err := uuid.Parse(wire.ActionRunID)
	if err != nil || actionRunID.String() != wire.ActionRunID {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid"}
	}
	occurredAt, err := ParseWireTime(wire.OccurredAt)
	if err != nil {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	recordedAt, err := ParseWireTime(wire.RecordedAt)
	if err != nil {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	if wire.Sequence < 1 || !lowerHex64.MatchString(wire.PrevHash) ||
		!lowerHex64.MatchString(wire.EventHash) || !validEnvironment(wire.Environment) ||
		!validPrincipalType(wire.PrincipalType) {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid"}
	}
	if wire.Result != string(audit.ResultSucceeded) && wire.Result != string(audit.ResultFailed) {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid"}
	}
	event := audit.Event{
		ID: id, Sequence: wire.Sequence, OccurredAt: occurredAt, RecordedAt: recordedAt,
		PrincipalID: wire.PrincipalID, PrincipalType: principal.Type(wire.PrincipalType),
		ActionID: wire.ActionID, ActionVersion: wire.ActionVersion, ActionRunID: actionRunID,
		ResourceType: wire.ResourceType, ResourceID: wire.ResourceID, Environment: wire.Environment,
		Reason: wire.Reason, ApprovalID: wire.ApprovalID, RequestID: wire.RequestID,
		TraceID: wire.TraceID, SourceIP: wire.SourceIP,
		BeforeSummary: wire.BeforeSummary, AfterSummary: wire.AfterSummary,
		ConnectorRequestSummary:  wire.ConnectorRequestSummary,
		ConnectorResponseSummary: wire.ConnectorResponseSummary,
		Result:                   audit.Result(wire.Result), CompensationResult: wire.CompensationResult,
		PrevHash: wire.PrevHash, EventHash: wire.EventHash, CanonicalVersion: wire.CanonicalVersion,
	}
	wantHash, err := event.ComputeHash()
	if err != nil {
		return audit.Event{}, &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	if wantHash != event.EventHash {
		return audit.Event{}, &FormatError{Code: "event_hash_mismatch"}
	}
	return event, nil
}

func DecodePayload(reader io.Reader) ([]audit.Event, error) {
	buffered := bufio.NewReader(reader)
	events := make([]audit.Event, 0)
	for lineNumber := int64(1); ; lineNumber++ {
		line, err := buffered.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				break
			}
			return nil, &FormatError{Code: "truncated_object", Line: lineNumber}
		}
		if err != nil {
			return nil, err
		}
		if len(line) == 1 {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber}
		}
		line = line[:len(line)-1]
		var wire ArchiveEventV1
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&wire); err != nil {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber, Cause: err}
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber, Cause: err}
		}
		event, err := archiveEventToAudit(wire)
		if err != nil {
			var compatibility *CompatibilityError
			if errors.As(err, &compatibility) {
				return nil, compatibility
			}
			var format *FormatError
			if errors.As(err, &format) {
				return nil, &FormatError{Code: format.Code, Line: lineNumber, Cause: err}
			}
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber, Cause: err}
		}
		canonical, err := json.Marshal(wire)
		if err != nil || !bytes.Equal(canonical, line) {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber, Cause: err}
		}
		events = append(events, event)
	}
	return events, nil
}

func DecodeProjection(reader io.Reader, environment string) ([]ProjectionEventV1, error) {
	buffered := bufio.NewReader(reader)
	rows := make([]ProjectionEventV1, 0)
	for lineNumber := int64(1); ; lineNumber++ {
		line, err := buffered.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				break
			}
			return nil, &FormatError{Code: "truncated_object", Line: lineNumber}
		}
		if err != nil {
			return nil, err
		}
		if len(line) == 1 {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber}
		}
		line = line[:len(line)-1]
		var row ProjectionEventV1
		if err := decodeCanonicalLine(line, &row); err != nil {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber, Cause: err}
		}
		if row.Sequence < 1 || !validEnvironment(row.Environment) || row.Environment != environment || !lowerHex64.MatchString(row.EventHash) ||
			!lowerHex64.MatchString(row.PrevHash) {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber}
		}
		if _, err := time.Parse(time.RFC3339Nano, row.OccurredAt); err != nil {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber, Cause: err}
		}
		if _, err := hex.DecodeString(row.EventHash); err != nil {
			return nil, &FormatError{Code: "archive_format_invalid", Line: lineNumber, Cause: err}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
