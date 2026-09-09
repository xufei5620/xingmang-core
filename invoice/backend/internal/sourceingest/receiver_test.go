package sourceingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/domain"
)

type signatureFixture struct {
	BatchFile          string `json:"batch_file"`
	SourceID           string `json:"source_id"`
	StreamID           string `json:"stream_id"`
	BatchID            string `json:"batch_id"`
	Sequence           int64  `json:"sequence"`
	SentAt             string `json:"sent_at"`
	BodySHA256         string `json:"body_sha256"`
	KeyID              string `json:"key_id"`
	PublicKeyRawBase64 string `json:"public_key_raw_base64"`
	SignatureBase64    string `json:"signature_base64"`
}

type captureAcceptor struct {
	batch application.VerifiedSourceBatch
	// err, when set, makes AcceptSourceBatch fail instead of succeeding --
	// exercises the commit-rejected logging path in ServeHTTP.
	err error
}

func (a *captureAcceptor) AcceptSourceBatch(_ context.Context, sourceID string, batch application.VerifiedSourceBatch) (application.SourceBatchAck, error) {
	a.batch = batch
	if a.err != nil {
		return application.SourceBatchAck{}, a.err
	}
	return application.SourceBatchAck{Accepted: true, SourceInstanceID: sourceID, StreamID: batch.StreamID, BatchID: batch.BatchID, Sequence: batch.Sequence, AcceptedRecords: len(batch.Events)}, nil
}

func loadSignatureFixture(t *testing.T) (signatureFixture, []byte) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "contracts", "examples")
	body, err := os.ReadFile(filepath.Join(root, "source-agent-signature.v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture signatureFixture
	if err = json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	batch, err := os.ReadFile(filepath.Join(root, fixture.BatchFile))
	if err != nil {
		t.Fatal(err)
	}
	return fixture, batch
}

func testReceiver(t *testing.T) (*Receiver, signatureFixture, []byte, *captureAcceptor) {
	t.Helper()
	fixture, batch := loadSignatureFixture(t)
	trustPath := filepath.Join(t.TempDir(), "source-trust.json")
	streamTrust := `{"certificate_serials":["ABC123"],"signing_keys":{"` + fixture.KeyID + `":"` + fixture.PublicKeyRawBase64 + `"}}`
	trustBody := `{"sources":[{"source_id":"` + fixture.SourceID + `","source_type":"newapi","streams":{` +
		`"payments":` + streamTrust + `,"identities":` + streamTrust + `,"usage":` + streamTrust +
		`,"credits":` + streamTrust + `,"balances":` + streamTrust + `}}]}`
	if err := os.WriteFile(trustPath, []byte(trustBody), 0o600); err != nil {
		t.Fatal(err)
	}
	trust, err := LoadTrustFile(trustPath)
	if err != nil {
		t.Fatal(err)
	}
	networks, err := ParseProxyCIDRs([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339Nano, fixture.SentAt)
	acceptor := &captureAcceptor{}
	return &Receiver{Trust: trust, Acceptor: acceptor, ProxyCIDRs: networks, Now: func() time.Time { return now }}, fixture, batch, acceptor
}

func signedRequest(fixture signatureFixture, batch []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://invoice-ingest.internal"+SourceBatchPath, bytes.NewReader(batch))
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Invoice-mTLS-Verified", "SUCCESS")
	request.Header.Set("X-Invoice-Client-Cert-Serial", "ABC123")
	request.Header.Set("X-Source-ID", fixture.SourceID)
	request.Header.Set("X-Stream-ID", fixture.StreamID)
	request.Header.Set("X-Batch-ID", fixture.BatchID)
	request.Header.Set("X-Sequence", "1")
	request.Header.Set("X-Sent-At", fixture.SentAt)
	request.Header.Set("X-Content-SHA256", fixture.BodySHA256)
	request.Header.Set("X-Signature-Key-ID", fixture.KeyID)
	request.Header.Set("X-Signature", fixture.SignatureBase64)
	return request
}

func TestReceiverAcceptsCrossModuleSignatureVector(t *testing.T) {
	receiver, fixture, batch, acceptor := testReceiver(t)
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, signedRequest(fixture, batch))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if acceptor.batch.StreamID != "payments" || acceptor.batch.SourceInstanceID != fixture.SourceID ||
		acceptor.batch.ProjectionStatus != "healthy" || len(acceptor.batch.Events) != 1 {
		t.Fatalf("captured batch=%+v", acceptor.batch)
	}
	if !strings.Contains(recorder.Body.String(), `"stream_id":"payments"`) {
		t.Fatalf("ack does not bind stream: %s", recorder.Body.String())
	}
}

func TestReceiverLogsCommitRejectionWithoutLeakingPayload(t *testing.T) {
	// XM-INV-OBS-BUNDLE: a commit rejection used to be entirely silent
	// (409/503 with no server-side log), so diagnosing it required going
	// straight to the database. AcceptSourceBatch's error must now be logged
	// with enough context to correlate against the batch, without echoing the
	// batch's own payload bytes.
	receiver, fixture, batch, acceptor := testReceiver(t)
	acceptor.err = errors.New("ledger unique constraint violated")
	var logs bytes.Buffer
	receiver.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, signedRequest(fixture, batch))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	logText := logs.String()
	for _, want := range []string{
		"source batch commit rejected",
		"source_instance_id=" + fixture.SourceID,
		"stream_id=" + fixture.StreamID,
		"batch_id=" + fixture.BatchID,
		"sequence=1",
		"status=409",
		"ledger unique constraint violated",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log missing %q: %s", want, logText)
		}
	}
	if strings.Contains(logText, string(batch)) {
		t.Fatalf("log must not leak the raw batch payload: %s", logText)
	}
}

func TestDecodeBatchV2RejectsMissingHealthAndNonProductionIdentity(t *testing.T) {
	_, body := loadSignatureFixture(t)
	missingHealth := bytes.Replace(body, []byte("  \"projection_status\": \"healthy\",\r\n"), nil, 1)
	if bytes.Equal(missingHealth, body) {
		missingHealth = bytes.Replace(body, []byte("  \"projection_status\": \"healthy\",\n"), nil, 1)
	}
	if _, err := decodeBatch(missingHealth); err == nil {
		t.Fatal("v2 batch without projection_status was accepted")
	}
	for name, mutated := range map[string][]byte{
		"desktop":       bytes.Replace(body, []byte(`"source_type": "newapi"`), []byte(`"source_type": "desktop"`), 1),
		"custom stream": bytes.Replace(body, []byte(`"stream_id": "payments"`), []byte(`"stream_id": "custom"`), 1),
		"slug source":   bytes.Replace(body, []byte(`"source_instance_id": "10000000-0000-4000-8000-000000000002"`), []byte(`"source_instance_id": "newapi-main"`), 1),
	} {
		if _, err := decodeBatch(mutated); err == nil {
			t.Fatalf("v2 batch with %s was accepted", name)
		}
	}
}

func TestReceiverRejectsTamperDuplicateJSONAndUntrustedNetwork(t *testing.T) {
	receiver, fixture, batch, _ := testReceiver(t)
	tampered := append([]byte(nil), batch...)
	tampered[len(tampered)-3] ^= 1
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, signedRequest(fixture, tampered))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("tamper status=%d", recorder.Code)
	}

	duplicate := bytes.Replace(batch, []byte(`"schema_version": "2.0",`), []byte(`"schema_version":"2.0","schema_version":"2.0",`), 1)
	recorder = httptest.NewRecorder()
	receiver.ServeHTTP(recorder, signedRequest(fixture, duplicate))
	if recorder.Code == http.StatusOK {
		t.Fatal("duplicate JSON key was accepted")
	}

	request := signedRequest(fixture, batch)
	request.RemoteAddr = "198.51.100.8:443"
	recorder = httptest.NewRecorder()
	receiver.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("untrusted proxy status=%d", recorder.Code)
	}
}

func TestParseProxyCIDRsRejectsBroadNetwork(t *testing.T) {
	if _, err := ParseProxyCIDRs([]string{"0.0.0.0/0"}); err == nil {
		t.Fatal("world-open ingestion proxy network accepted")
	}
}

func TestReceiverMapsScanCycleBusyTo503WithRetryAfter(t *testing.T) {
	receiver, fixture, batch, acceptor := testReceiver(t)
	acceptor.err = domain.ErrScanCycleBusy
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, signedRequest(fixture, batch))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After=%q", got)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"SOURCE_SCAN_CYCLE_BUSY"`) {
		t.Fatalf("body missing busy code: %s", recorder.Body.String())
	}
}

func TestReceiverMapsOtherCommitErrorsTo409WithoutRetryAfter(t *testing.T) {
	receiver, fixture, batch, acceptor := testReceiver(t)
	acceptor.err = errors.New("some other commit conflict")
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, signedRequest(fixture, batch))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("unexpected Retry-After on a real conflict: %q", got)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"SOURCE_BATCH_COMMIT_REJECTED"`) {
		t.Fatalf("body missing commit-rejected code: %s", recorder.Body.String())
	}
}

func TestReceiverMapsContextErrorsTo503WithoutBusyCode(t *testing.T) {
	for name, ctxErr := range map[string]error{"canceled": context.Canceled, "deadline exceeded": context.DeadlineExceeded} {
		t.Run(name, func(t *testing.T) {
			receiver, fixture, batch, acceptor := testReceiver(t)
			acceptor.err = ctxErr
			recorder := httptest.NewRecorder()
			receiver.ServeHTTP(recorder, signedRequest(fixture, batch))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get("Retry-After"); got != "" {
				t.Fatalf("unexpected Retry-After on a context error: %q", got)
			}
			if !strings.Contains(recorder.Body.String(), `"code":"SOURCE_BATCH_COMMIT_REJECTED"`) {
				t.Fatalf("body code changed for context errors: %s", recorder.Body.String())
			}
		})
	}
}
