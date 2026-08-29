package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func fixtureEvents(t *testing.T) []audit.Event {
	t.Helper()
	return []audit.Event{
		{
			ID:                       uuid.MustParse("11111111-1111-4111-8111-111111111111"),
			Sequence:                 1,
			OccurredAt:               time.Date(2026, 8, 28, 10, 0, 0, 123456000, time.UTC),
			RecordedAt:               time.Date(2026, 8, 28, 10, 0, 1, 123456000, time.UTC),
			PrincipalID:              "operator-1",
			PrincipalType:            principal.TypeHuman,
			ActionID:                 "registry.service.create",
			ActionVersion:            "1",
			ActionRunID:              uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
			ResourceType:             "core.service",
			ResourceID:               "service-1",
			Environment:              "staging",
			Reason:                   "baseline",
			RequestID:                "req-1",
			TraceID:                  "trace-1",
			SourceIP:                 "127.0.0.1",
			BeforeSummary:            map[string]any{},
			AfterSummary:             map[string]any{"name": "alpha"},
			ConnectorRequestSummary:  map[string]any{},
			ConnectorResponseSummary: map[string]any{},
			Result:                   audit.ResultSucceeded,
			PrevHash:                 audit.GenesisHash,
			EventHash:                "6ee17c5c4f481e877504a86a5c3d8521a731193ebc526041026de6b7bba590b8",
			CanonicalVersion:         audit.CanonicalV1,
		},
		{
			ID:                       uuid.MustParse("22222222-2222-4222-8222-222222222222"),
			Sequence:                 2,
			OccurredAt:               time.Date(2026, 8, 28, 10, 0, 2, 223456000, time.UTC),
			RecordedAt:               time.Date(2026, 8, 28, 10, 0, 3, 223456000, time.UTC),
			PrincipalID:              "worker-1",
			PrincipalType:            principal.TypeService,
			ActionID:                 "finance.upstream_account.set",
			ActionVersion:            "1",
			ActionRunID:              uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
			ResourceType:             "finance.upstream_account",
			ResourceID:               "upstream-1",
			Environment:              "production",
			Reason:                   "sync",
			RequestID:                "req-2",
			TraceID:                  "trace-2",
			SourceIP:                 "10.0.0.2",
			BeforeSummary:            map[string]any{"enabled": false},
			AfterSummary:             map[string]any{"enabled": true},
			ConnectorRequestSummary:  map[string]any{"method": "GET"},
			ConnectorResponseSummary: map[string]any{"status": float64(200)},
			Result:                   audit.ResultSucceeded,
			PrevHash:                 "6ee17c5c4f481e877504a86a5c3d8521a731193ebc526041026de6b7bba590b8",
			EventHash:                "aeaa30852897113afdec0bd8266320b65f5c68582efdbf9e2271c0839cd764a7",
			CanonicalVersion:         audit.CanonicalV2,
		},
	}
}

func TestEncodePayloadIsByteDeterministic(t *testing.T) {
	want, err := os.ReadFile("testdata/mixed-v1-v2.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	const wantSHA256 = "d0189713bc55aac401ecc8134f153c439bdfbce2a3294b2ad7d7a3f7ed932b3d"
	for run := 0; run < 2; run++ {
		var got bytes.Buffer
		object, err := EncodePayload(&got, fixtureEvents(t))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("run %d payload bytes changed\nwant=%q\n got=%q", run, want, got.Bytes())
		}
		if object.SHA256 != wantSHA256 || object.SizeBytes != 1739 || object.RowCount != 2 ||
			object.ContentType != PayloadContentType {
			t.Fatalf("run %d object evidence = %+v", run, object)
		}
	}
}

func TestPayloadPreservesAllPersistedFieldsAndCanonicalVersion(t *testing.T) {
	raw, err := os.ReadFile("testdata/mixed-v1-v2.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePayload(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, fixtureEvents(t)) {
		wantJSON, _ := json.Marshal(fixtureEvents(t))
		gotJSON, _ := json.Marshal(got)
		t.Fatalf("round trip changed persisted fields\nwant=%s\n got=%s", wantJSON, gotJSON)
	}
}

func TestProjectionContainsOnlyHTTPAllowlistedFields(t *testing.T) {
	var output bytes.Buffer
	object, err := EncodeProjection(&output, "staging", fixtureEvents(t))
	if err != nil {
		t.Fatal(err)
	}
	if object.RowCount != 1 {
		t.Fatalf("staging projection rows = %d, want 1", object.RowCount)
	}
	line := bytes.TrimSuffix(output.Bytes(), []byte("\n"))
	var got map[string]json.RawMessage
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{
		"action_id", "action_run_id", "action_version", "after_summary", "before_summary",
		"environment", "error_code", "event_hash", "occurred_at", "prev_hash", "principal_id",
		"principal_type", "request_id", "resource_id", "resource_type", "result", "sequence",
	}
	gotKeys := make([]string, 0, len(got))
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("projection keys = %v, want %v", gotKeys, wantKeys)
	}
	for _, forbidden := range []string{
		"reason", "approval_id", "trace_id", "source_ip", "connector_request_summary",
		"connector_response_summary", "recorded_at", "canonical_version",
	} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("projection leaked %q", forbidden)
		}
	}
}

func TestProjectionSeparatesEnvironments(t *testing.T) {
	for environment, wantSequence := range map[string]int64{"staging": 1, "production": 2} {
		var output bytes.Buffer
		object, err := EncodeProjection(&output, environment, fixtureEvents(t))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := DecodeProjection(bytes.NewReader(output.Bytes()), environment)
		if err != nil {
			t.Fatal(err)
		}
		if object.RowCount != 1 || len(rows) != 1 || rows[0].Sequence != wantSequence ||
			rows[0].Environment != environment {
			t.Fatalf("%s projection = %+v rows=%+v", environment, object, rows)
		}
	}
}

func TestEncodeProjectionRejectsUnknownEnvironment(t *testing.T) {
	for _, environment := range []string{"unknown", "../staging", "staging/other"} {
		var output bytes.Buffer
		if _, err := EncodeProjection(&output, environment, fixtureEvents(t)); err == nil {
			t.Fatalf("environment %q was accepted", environment)
		}
		if output.Len() != 0 {
			t.Fatalf("environment %q wrote bytes before rejection", environment)
		}
	}
}

func TestEncodePayloadRejectsInvalidPrincipalEnvironmentAndResult(t *testing.T) {
	for name, mutate := range map[string]func(*audit.Event){
		"principal":   func(event *audit.Event) { event.PrincipalType = principal.Type("OTHER") },
		"environment": func(event *audit.Event) { event.Environment = "unknown" },
		"result":      func(event *audit.Event) { event.Result = audit.Result("unknown") },
	} {
		t.Run(name, func(t *testing.T) {
			event := fixtureEvents(t)[0]
			mutate(&event)
			hash, err := event.ComputeHash()
			if err != nil {
				t.Fatal(err)
			}
			event.EventHash = hash
			var output bytes.Buffer
			if _, err := EncodePayload(&output, []audit.Event{event}); err == nil {
				t.Fatal("domain-invalid event entered frozen archive wire")
			}
			if output.Len() != 0 {
				t.Fatal("invalid event wrote bytes before rejection")
			}
		})
	}
}

func TestSegmentCutsAtRowOrByteBoundaryWithoutPartialRecord(t *testing.T) {
	events := fixtureEvents(t)
	segments, err := SegmentPayloads(events, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || len(segments[0].Events) != 1 || len(segments[1].Events) != 1 {
		t.Fatalf("row boundary segments = %+v", segments)
	}

	firstLine, err := json.Marshal(archiveEventFromAudit(events[0]))
	if err != nil {
		t.Fatal(err)
	}
	segments, err = SegmentPayloads(events, 25_000, int64(len(firstLine)+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || segments[0].Encoded.SizeBytes != int64(len(firstLine)+1) {
		t.Fatalf("byte boundary segments = %+v", segments)
	}
	for index, segment := range segments {
		if len(segment.Bytes) == 0 || segment.Bytes[len(segment.Bytes)-1] != '\n' {
			t.Fatalf("segment %d ended with a partial record: %q", index, segment.Bytes)
		}
		sum := sha256.Sum256(segment.Bytes)
		if hex.EncodeToString(sum[:]) != segment.Encoded.SHA256 {
			t.Fatalf("segment %d hash does not cover exact bytes", index)
		}
	}
}
