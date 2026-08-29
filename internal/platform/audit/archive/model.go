package archive

import (
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

const (
	FormatVersion = 1

	PayloadContentType    = "application/x-ndjson; charset=utf-8"
	ProjectionContentType = "application/x-ndjson; charset=utf-8"

	CompatibilityVerifierOutdated = "verifier_outdated"
)

const wireTimeLayout = "2006-01-02T15:04:05.000000Z"

// WireTime is the frozen UTC, fixed-microsecond representation used by archive v1.
type WireTime string

func NewWireTime(value time.Time) WireTime {
	return WireTime(value.UTC().Truncate(time.Microsecond).Format(wireTimeLayout))
}

func ParseWireTime(value WireTime) (time.Time, error) {
	parsed, err := time.Parse(wireTimeLayout, string(value))
	if err != nil || string(NewWireTime(parsed)) != string(value) {
		return time.Time{}, fmt.Errorf("archive_format_invalid: non-canonical wire time")
	}
	return parsed, nil
}

type EncodedObjectV1 struct {
	SHA256      string
	SizeBytes   int64
	ContentType string
	RowCount    int64
}

type SegmentBoundary struct {
	FromSequence   int64
	ToSequence     int64
	RowCount       int64
	FirstPrevHash  string
	FirstEventHash string
	LastEventHash  string
}

type EncodedSegmentV1 struct {
	Events               []audit.Event
	Bytes                []byte
	Encoded              EncodedObjectV1
	Boundary             SegmentBoundary
	OversizedRecordCount int64
}

type ArchiveEventV1 struct {
	ID                       string         `json:"id"`
	Sequence                 int64          `json:"sequence"`
	OccurredAt               WireTime       `json:"occurred_at"`
	RecordedAt               WireTime       `json:"recorded_at"`
	PrincipalID              string         `json:"principal_id"`
	PrincipalType            string         `json:"principal_type"`
	ActionID                 string         `json:"action_id"`
	ActionVersion            string         `json:"action_version"`
	ActionRunID              string         `json:"action_run_id"`
	ResourceType             string         `json:"resource_type"`
	ResourceID               string         `json:"resource_id"`
	Environment              string         `json:"environment"`
	Reason                   string         `json:"reason"`
	ApprovalID               string         `json:"approval_id"`
	RequestID                string         `json:"request_id"`
	TraceID                  string         `json:"trace_id"`
	SourceIP                 string         `json:"source_ip"`
	BeforeSummary            map[string]any `json:"before_summary"`
	AfterSummary             map[string]any `json:"after_summary"`
	ConnectorRequestSummary  map[string]any `json:"connector_request_summary"`
	ConnectorResponseSummary map[string]any `json:"connector_response_summary"`
	Result                   string         `json:"result"`
	CompensationResult       string         `json:"compensation_result"`
	PrevHash                 string         `json:"prev_hash"`
	EventHash                string         `json:"event_hash"`
	CanonicalVersion         int16          `json:"canonical_version"`
}

type ProjectionEventV1 struct {
	Sequence      int64          `json:"sequence"`
	OccurredAt    string         `json:"occurred_at"`
	PrincipalID   string         `json:"principal_id"`
	PrincipalType string         `json:"principal_type"`
	ActionID      string         `json:"action_id"`
	ActionVersion string         `json:"action_version"`
	ActionRunID   string         `json:"action_run_id"`
	ResourceType  string         `json:"resource_type"`
	ResourceID    string         `json:"resource_id"`
	Environment   string         `json:"environment"`
	RequestID     string         `json:"request_id"`
	Result        string         `json:"result"`
	ErrorCode     string         `json:"error_code"`
	BeforeSummary map[string]any `json:"before_summary"`
	AfterSummary  map[string]any `json:"after_summary"`
	EventHash     string         `json:"event_hash"`
	PrevHash      string         `json:"prev_hash"`
}

type FormatError struct {
	Code  string
	Line  int64
	Cause error
}

func (e *FormatError) Error() string {
	if e == nil {
		return "archive_format_invalid"
	}
	if e.Line > 0 {
		return fmt.Sprintf("%s at line %d", e.Code, e.Line)
	}
	return e.Code
}

func (e *FormatError) Unwrap() error { return e.Cause }

type CompatibilityError struct {
	Code             string
	FormatVersion    int
	CanonicalVersion int
}

func validEnvironment(value string) bool {
	switch value {
	case "development", "staging", "production":
		return true
	default:
		return false
	}
}

func validPrincipalType(value string) bool {
	switch value {
	case "HUMAN", "SERVICE", "AI", "SERVER_AGENT":
		return true
	default:
		return false
	}
}

func (e *CompatibilityError) Error() string {
	return fmt.Sprintf("%s: format=%d canonical=%d", e.Code, e.FormatVersion, e.CanonicalVersion)
}
