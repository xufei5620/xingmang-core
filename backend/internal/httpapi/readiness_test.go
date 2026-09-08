package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/ledger"
)

// readinessServer builds a server whose /readyz verdict is exactly what
// readiness returns, with its log captured.
func readinessServer(t *testing.T, readiness func(context.Context) error) (*Server, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	now := time.Now().UTC()
	repo := adminsettings.NewMemoryRepository(adminsettings.Settings{
		IssuerName: "某某科技有限公司", ServiceItem: adminsettings.FixedServiceItem,
		MinimumRequestMinor: adminsettings.DefaultMinimumRequestMinor, SMTPHost: "smtp.qq.com", SMTPPort: 587,
		SMTPFrom: "invoice@qq.com", SMTPFromName: "发票中心", SMTPStartTLS: true,
		AdminCIDRs: []string{"203.0.113.8/32"}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	server, err := NewWithConfig(ledger.NewService(), Config{
		AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.8/32"},
		AdminSettings: adminsettings.NewService(repo, httpSettingsBox{}),
		Readiness:     readiness,
	}, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return server, logs
}

func getReadyz(t *testing.T, server *Server) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return recorder.Code, recorder.Body.String()
}

// TestReadyzPublishesTheCheckNameAndLogsTheRealCause is the endpoint half of
// XM-INV-READYZ-DETAIL. The two halves of the split are asserted together
// because either one alone would be misleading: the body must gain the stable
// name, and the underlying error -- which is what an operator actually needs
// and what must never be published -- must appear in the log and only there.
func TestReadyzPublishesTheCheckNameAndLogsTheRealCause(t *testing.T) {
	cause := errors.New("dial tcp 10.0.0.7:5432: connect: connection refused")
	server, logs := readinessServer(t, func(context.Context) error {
		return NotReady("database", "the invoice database is unreachable", cause)
	})

	status, body := getReadyz(t, server)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var decoded struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Check   string `json:"check"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body is not the documented error envelope: %v (%s)", err, body)
	}
	if decoded.Error.Code != "NOT_READY" {
		t.Fatalf("code=%q want NOT_READY -- the envelope is a published contract", decoded.Error.Code)
	}
	if decoded.Error.Check != "database" {
		t.Fatalf("check=%q want %q", decoded.Error.Check, "database")
	}
	if decoded.Error.Message != "the invoice database is unreachable" {
		t.Fatalf("message=%q want the check's own sentence verbatim", decoded.Error.Message)
	}
	// Absence assertion, mutation-verified: see the handoff doc. The address
	// in the cause is the exact class of detail this endpoint must not hand
	// to an unauthenticated caller.
	for _, leak := range []string{"10.0.0.7", "5432", "connection refused"} {
		if strings.Contains(body, leak) {
			t.Fatalf("unauthenticated /readyz leaked %q from the underlying error: %s", leak, body)
		}
	}

	logged := logs.String()
	if !strings.Contains(logged, "readiness check failed") || !strings.Contains(logged, "check=database") {
		t.Fatalf("log does not name the failing check: %s", logged)
	}
	if !strings.Contains(logged, "10.0.0.7:5432") {
		t.Fatalf("log dropped the real cause, which is the only place it is available: %s", logged)
	}
}

// TestReadyzRefusesToPublishStringsOutsideTheAllowedShape is the fail-closed
// half of the security design. The guard lives at the response boundary
// rather than at the call sites so that it holds for call sites that do not
// exist yet -- the mistake being defended against is a future edit passing
// the underlying error straight through as the summary.
func TestReadyzRefusesToPublishStringsOutsideTheAllowedShape(t *testing.T) {
	for name, fixture := range map[string]struct {
		err      error
		wantLog  string
		mustHide []string
	}{
		"summary carrying a database url": {
			err: NotReady("database",
				"postgres://invoice_app@10.0.0.7:5432/invoice is unreachable",
				errors.New("boom")),
			wantLog:  "check=rejected_check_name",
			mustHide: []string{"postgres", "10.0.0.7", "invoice_app"},
		},
		"check name carrying a host": {
			err:      NotReady("api.invoice.example", "the invoice database is unreachable", errors.New("boom")),
			wantLog:  "check=rejected_check_name",
			mustHide: []string{"api.invoice.example"},
		},
		"empty check name": {
			err:      NotReady("", "the invoice database is unreachable", errors.New("boom")),
			wantLog:  "check=rejected_check_name",
			mustHide: []string{"\"check\""},
		},
		"unclassified readiness error": {
			err:      errors.New("issuer not configured"),
			wantLog:  "check=unclassified",
			mustHide: []string{"\"check\"", "issuer not configured"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, logs := readinessServer(t, func(context.Context) error { return fixture.err })
			status, body := getReadyz(t, server)
			if status != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", status, body)
			}
			// Byte-identical to what this endpoint returned before the
			// slice: a rejected or unclassified failure must degrade to the
			// old behaviour, never to a partial disclosure.
			const generic = `{"error":{"code":"NOT_READY","message":"required dependencies are unavailable"}}`
			if strings.TrimSpace(body) != generic {
				t.Fatalf("body=%s want the generic envelope %s", strings.TrimSpace(body), generic)
			}
			for _, leak := range fixture.mustHide {
				if strings.Contains(body, leak) {
					t.Fatalf("body leaked %q: %s", leak, body)
				}
			}
			if !strings.Contains(logs.String(), fixture.wantLog) {
				t.Fatalf("log missing %q: %s", fixture.wantLog, logs.String())
			}
		})
	}
}

// TestReadinessFailureFieldsAlwaysNamesSomethingForTheLog closes the one edge
// the tests above found by mutation: an empty logCheck would be treated by
// readinessOutcomeLog.record as a recovery, and the 503 would then be logged
// as nothing at all. Today that cannot happen -- both fallbacks are non-empty
// constants and the pattern requires at least three characters -- and this
// keeps it that way.
func TestReadinessFailureFieldsAlwaysNamesSomethingForTheLog(t *testing.T) {
	for name, err := range map[string]error{
		"well formed":  NotReady("database", "the invoice database is unreachable", errors.New("boom")),
		"empty name":   NotReady("", "the invoice database is unreachable", errors.New("boom")),
		"empty both":   NotReady("", "", nil),
		"bad name":     NotReady("api.invoice.example", "the invoice database is unreachable", errors.New("boom")),
		"bad summary":  NotReady("database", "postgres://x@10.0.0.7/y", errors.New("boom")),
		"unclassified": errors.New("issuer not configured"),
	} {
		t.Run(name, func(t *testing.T) {
			logCheck, _, _ := readinessFailureFields(err)
			if logCheck == "" {
				t.Fatal("an empty log check name would silently suppress the 503 log line entirely")
			}
		})
	}
}

// TestReadyzReadyResponseIsUnchanged is the control for the tests above: the
// success path must keep its exact shape and stay silent, so that a failure
// in one of them means the failure path changed rather than the endpoint
// being broken outright.
func TestReadyzReadyResponseIsUnchanged(t *testing.T) {
	server, logs := readinessServer(t, func(context.Context) error { return nil })
	status, body := getReadyz(t, server)
	if status != http.StatusOK || strings.TrimSpace(body) != `{"status":"ready"}` {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if logs.Len() != 0 {
		t.Fatalf("a healthy probe must not log; it runs every 10s in production: %s", logs.String())
	}
}

// TestReadinessOutcomeLogRateLimitsPerCheck covers the state machine directly,
// with explicit clock values, because the behaviour that matters is a
// six-hour one: the production healthcheck probes /readyz every 10 seconds,
// so a line per evaluation would have written thousands of identical entries
// during the incident and buried the rest of the log.
func TestReadinessOutcomeLogRateLimitsPerCheck(t *testing.T) {
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	var log readinessOutcomeLog

	if !log.record("source_ingest_dead_events", start) {
		t.Fatal("the first failure of a check must always be reported")
	}
	if log.record("source_ingest_dead_events", start.Add(10*time.Second)) {
		t.Fatal("the same check repeating 10s later must be suppressed")
	}
	if log.record("source_ingest_dead_events", start.Add(readinessFailureLogInterval-time.Nanosecond)) {
		t.Fatal("suppression must hold right up to the interval")
	}
	if !log.record("source_ingest_dead_events", start.Add(readinessFailureLogInterval)) {
		t.Fatal("the interval must let the check report again, so a stuck check stays visible")
	}
	// A different check is news regardless of how recently anything reported:
	// the failing check changing is exactly what an operator needs to see.
	if !log.record("database", start.Add(readinessFailureLogInterval+time.Second)) {
		t.Fatal("a different check must report immediately")
	}
	if !log.record("", start.Add(readinessFailureLogInterval+2*time.Second)) {
		t.Fatal("recovery after a reported failure must be reported, so an episode has both ends")
	}
	if log.record("", start.Add(readinessFailureLogInterval+3*time.Second)) {
		t.Fatal("a steady-state ready service must stay silent")
	}
	if !log.record("database", start.Add(readinessFailureLogInterval+4*time.Second)) {
		t.Fatal("a new episode after a recovery must report immediately, not wait out the interval")
	}
}

// TestReadyzLogsOncePerEpisodeNotOncePerProbe wires the rate limit through
// the real handler, since that is where the 10-second probe actually lands.
func TestReadyzLogsOncePerEpisodeNotOncePerProbe(t *testing.T) {
	server, logs := readinessServer(t, func(context.Context) error {
		return NotReady("source_ingest_dead_events",
			"source ingestion has dead events requiring operator repair",
			errors.New("source ingestion contains dead events"))
	})
	for i := 0; i < 30; i++ {
		if status, body := getReadyz(t, server); status != http.StatusServiceUnavailable {
			t.Fatalf("probe %d status=%d body=%s", i, status, body)
		}
	}
	if got := strings.Count(logs.String(), "readiness check failed"); got != 1 {
		t.Fatalf("30 probes inside the interval wrote %d log lines, want 1", got)
	}
}
