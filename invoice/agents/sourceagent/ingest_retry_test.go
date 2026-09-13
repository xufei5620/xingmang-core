package sourceagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The server verifies the actual signed HTTP request before returning the
// receiver's wire response, so the tests cover transport parsing and Run.
func retryHTTPClient(t *testing.T, respond func(http.ResponseWriter, ValidatedBatch)) (*HTTPIngestClient, BatchBuilder) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := NewAtomicSigningKeyProvider("retry-test-key", private)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	builder := BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection", Now: func() time.Time { return now },
	}
	publicKeys := StaticPublicKeySet{builder.SourceInstanceID: {builder.StreamID: {"retry-test-key": public}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		seq, seqErr := strconv.ParseUint(r.Header.Get("X-Sequence"), 10, 64)
		metadata := SignatureMetadata{
			SourceID: r.Header.Get("X-Source-ID"), StreamID: r.Header.Get("X-Stream-ID"), BatchID: r.Header.Get("X-Batch-ID"),
			Sequence: seq, SentAt: r.Header.Get("X-Sent-At"), BodySHA256: r.Header.Get("X-Content-SHA256"),
			KeyID: r.Header.Get("X-Signature-Key-ID"), Signature: r.Header.Get("X-Signature"),
		}
		if readErr != nil || seqErr != nil || VerifyBatchSignature(body, metadata, publicKeys, now, time.Minute) != nil {
			t.Error("request did not preserve a valid signed batch")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var batch Batch
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		respond(w, ValidatedBatch{Batch: batch, RawBody: body, BodyHash: metadata.BodySHA256})
	}))
	t.Cleanup(server.Close)
	origin, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(origin.Port())
	restricted, err := NewRestrictedHTTPClient(server.URL, OriginPolicy{
		AllowedHosts: []string{origin.Hostname()}, AllowedPorts: []int{port}, AllowedCIDRs: []string{"127.0.0.0/8"},
		AllowedMethods: []string{http.MethodPost}, AllowPlainHTTP: true,
	}, TLSFiles{}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return &HTTPIngestClient{HTTP: restricted, Keys: keys, Now: func() time.Time { return now }, AllowInsecureDevelopment: true}, builder
}

func TestRunnerHTTPTransientRetriesExactPendingBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan ValidatedBatch, 2)
	var attempts int
	client, builder := retryHTTPClient(t, func(w http.ResponseWriter, batch ValidatedBatch) {
		requests <- batch
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"SOURCE_BATCH_COMMIT_RETRYABLE","message":"source batch rejected"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(IngestAck{Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
			StreamID: batch.Batch.StreamID, BatchID: batch.Batch.BatchID, Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records)})
	})
	sequence, cursors, pending := &MemorySequenceStore{}, NewMemoryCursorStore(), &MemoryPendingBatchStore{}
	coordinator := &SyncCoordinator{SourceID: builder.SourceInstanceID, Limit: 100, Cursors: cursors,
		Connector: fixedConnector{page: ScanPage{Projections: []Projection{candidateProjectionForTest()}, NextCursor: ScanCursor{Version: 1, ID: 41, Completed: true}}},
		Publisher: &Publisher{Builder: builder, Client: client, Store: sequence, Pending: pending},
	}
	failures, cycles, sleeps := 0, 0, 0
	runner := &SyncRunner{Coordinator: coordinator, SourceType: SourceNewAPI, PollInterval: 5 * time.Second, FullScanInterval: time.Hour,
		MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 2, Jitter: func(d time.Duration) time.Duration { return d },
		OnFailure: func(failure SyncFailure) {
			failures++
			if failure.Permanent || failure.RetryIn != 5*time.Second {
				t.Fatalf("unexpected transient failure: %+v", failure)
			}
			state, _ := sequence.Load(ctx)
			cursor, _ := cursors.Load(ctx, builder.SourceInstanceID)
			spooled, exists, err := pending.Load(ctx)
			if err != nil || !exists || state.Sequence != 0 || cursor.Revision != 0 || spooled.Batch.Batch.Sequence != 1 {
				t.Fatal("503 did not retain pending batch without advancing sequence/cursor")
			}
			coordinator.Connector = failOnScanConnector{}
		},
		OnCycle: func(result SyncCycleResult) {
			cycles++
			if result.LastSeq != 1 || !result.Complete {
				t.Fatalf("unexpected cycle: %+v", result)
			}
			cancel()
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sleeps++
			if d != 5*time.Second {
				t.Fatalf("retry wait=%s", d)
			}
			return nil
		},
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if failures != 1 || cycles != 1 || sleeps != 1 || len(requests) != 2 {
		t.Fatalf("failures=%d cycles=%d sleeps=%d requests=%d", failures, cycles, sleeps, len(requests))
	}
	first, retried := <-requests, <-requests
	if first.Batch.Sequence != retried.Batch.Sequence || first.Batch.BatchID != retried.Batch.BatchID || first.BodyHash != retried.BodyHash || !bytes.Equal(first.RawBody, retried.RawBody) {
		t.Fatal("retry rebuilt or changed the pending batch")
	}
	state, _ := sequence.Load(ctx)
	cursor, _ := cursors.Load(ctx, builder.SourceInstanceID)
	if state.Sequence != 1 || state.Revision != 1 || cursor.ID != 41 || cursor.Revision != 1 {
		t.Fatalf("ACK did not apply exactly once: state=%+v cursor=%+v", state, cursor)
	}
	if _, exists, _ := pending.Load(ctx); exists {
		t.Fatal("pending batch not cleared after verified ACK")
	}
}

func TestRunnerHTTPRetryAfterOnlyExemptsScanCycleBusy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		busy   bool
	}{
		{"transient commit", 503, `{"error":{"code":"SOURCE_BATCH_COMMIT_RETRYABLE"}}`, false},
		{"missing code", 503, `{}`, false},
		{"unknown code", 503, `{"error":{"code":"PRIVATE_RESPONSE_MARKER"}}`, false},
		{"malformed", 503, `{"error":{"code":"SOURCE_SCAN_CYCLE_BUSY"}`, false},
		{"trailing json", 503, `{"error":{"code":"SOURCE_SCAN_CYCLE_BUSY"}} {}`, false},
		{"oversize", 503, `{"error":{"code":"SOURCE_SCAN_CYCLE_BUSY","message":"` + strings.Repeat("x", 4096) + `"}}`, false},
		{"scan cycle busy", 503, `{"error":{"code":"SOURCE_SCAN_CYCLE_BUSY"}}`, true},
		{"true conflict", 409, `{"error":{"code":"SOURCE_BATCH_COMMIT_REJECTED"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, builder := retryHTTPClient(t, func(w http.ResponseWriter, _ ValidatedBatch) {
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			batch, err := builder.Build(1, "", []Projection{candidateProjectionForTest()})
			if err != nil {
				t.Fatal(err)
			}
			_, transportErr := client.Send(context.Background(), batch)
			var ingestErr *IngestHTTPError
			if !errors.As(transportErr, &ingestErr) || ingestErr.StatusCode != tc.status || ingestErr.RetryAfter != 5*time.Second {
				t.Fatalf("unexpected HTTP error: %v", transportErr)
			}
			if strings.Contains(fmt.Sprintf("%#v", *ingestErr), "PRIVATE_RESPONSE_MARKER") {
				t.Fatal("response payload leaked into transport error")
			}
			syncer := &scriptedPageSyncer{err: transportErr}
			sleeps := 0
			runner := &SyncRunner{Coordinator: syncer, SourceType: SourceNewAPI, PollInterval: 5 * time.Second, FullScanInterval: time.Hour,
				MaxBackoff: time.Minute, MaxPagesPerCycle: 10, MaxConsecutiveFailures: 2, Jitter: func(d time.Duration) time.Duration { return d },
				Sleep: func(context.Context, time.Duration) error {
					sleeps++
					if sleeps >= 3 {
						return context.Canceled
					}
					return nil
				},
			}
			err = runner.Run(context.Background())
			switch {
			case tc.busy:
				if err != nil || sleeps != 3 {
					t.Fatalf("scan busy lost existing exemption: err=%v sleeps=%d", err, sleeps)
				}
			case tc.status == 409:
				if !errors.As(err, &ingestErr) || !ingestErr.Permanent() || sleeps != 0 || len(syncer.modes) != 1 {
					t.Fatalf("409 was retried: err=%v sleeps=%d", err, sleeps)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "circuit opened") || sleeps != 1 || len(syncer.modes) != 2 {
					t.Fatalf("transient escaped failure budget: err=%v sleeps=%d calls=%d", err, sleeps, len(syncer.modes))
				}
			}
		})
	}
}
