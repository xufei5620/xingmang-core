package invoicehost

import (
	"context"
	"encoding/json"
	"errors"
	invoiceservice "invoice-system/backend/service"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testModule struct {
	handler    http.Handler
	err        error
	readyCalls int
	degraded   []string
	check      string
}

func (m *testModule) Readiness(context.Context) invoiceservice.ReadinessReport {
	m.readyCalls++
	return invoiceservice.ReadinessReport{Ready: m.err == nil, Check: m.check, Degraded: m.degraded}
}

func (m *testModule) Handler() http.Handler       { return m.handler }
func (m *testModule) Ready(context.Context) error { m.readyCalls++; return m.err }
func TestUnifiedHandlerRoutesWithoutNetworkOrBodyRewrite(t *testing.T) {
	var received *http.Request
	var body string
	m := &testModule{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(201)
	})}
	platform := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) })
	h, err := NewHandler(platform, m, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://console.example.test/invoice-api/v1/admin/invoice-requests/r1/documents/upload?source_instance_id=sub", strings.NewReader("synthetic-upload"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=synthetic")
	r.Header.Set("X-CSRF-Token", "synthetic-csrf")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 || received == nil {
		t.Fatalf("invoice request reached status %d", w.Code)
	}
	if received.URL.Path != "/api/v1/admin/invoice-requests/r1/documents/upload" || received.URL.RawQuery != "source_instance_id=sub" || body != "synthetic-upload" || received.Header.Get("X-CSRF-Token") != "synthetic-csrf" {
		t.Fatal("invoice route changed request content")
	}
	if r.URL.Path != "/invoice-api/v1/admin/invoice-requests/r1/documents/upload" {
		t.Fatal("mutated original request")
	}
	ingest := httptest.NewRecorder()
	h.ServeHTTP(ingest, httptest.NewRequest("POST", "/internal/v1/source-batches", strings.NewReader("synthetic-batch")))
	if ingest.Code != 201 || received.URL.Path != "/internal/v1/source-batches" || body != "synthetic-batch" {
		t.Fatal("source ingress contract was lost")
	}
	for _, path := range []string{"/api/v1/auth/login", "/webhooks/infini/account-1", "/invoice-api-other/v1/admin"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 202 {
			t.Errorf("platform path %s changed: %d", path, rec.Code)
		}
	}
}
func TestUnifiedReadyIsStrictWhilePlatformBusinessRemainsAvailable(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		platformErr, invoiceErr error
		want                    int
	}{
		{"ready", nil, nil, 200}, {"platform-down", errors.New("private database address"), nil, 503}, {"invoice-down", nil, errors.New("private source address"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &testModule{handler: http.NotFoundHandler(), err: tc.invoiceErr}
			h, err := NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), m, func(context.Context) error { return tc.platformErr })
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
			}
			if strings.Contains(rec.Body.String(), "private") {
				t.Fatal("public probe leaked dependency error")
			}
			if m.readyCalls != 1 {
				t.Fatal("invoice readiness was not checked")
			}
			business := httptest.NewRecorder()
			h.ServeHTTP(business, httptest.NewRequest("GET", "/api/v1/ops/summary", nil))
			if business.Code != 200 || m.readyCalls != 1 {
				t.Fatal("invoice readiness blocked or intercepted a platform business route")
			}
		})
	}
}

func TestUnifiedReadyPreservesInvoiceDegradedEvidence(t *testing.T) {
	m := &testModule{handler: http.NotFoundHandler(), degraded: []string{"source_ingest_dead_contained"}}
	h, err := NewHandler(http.NotFoundHandler(), m, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"degraded":["source_ingest_dead_contained"]`) {
		t.Fatalf("contained anomaly hidden: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFullReadinessSamplingKeepsDefaultHTTPFailureAndOriginalBodyState(t *testing.T) {
	m := &testModule{handler: http.NotFoundHandler(), err: errors.New("private source path"), check: "source_streams"}
	h, err := NewHandler(http.NotFoundHandler(), m, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		code   int
		report bool
	}{
		{"/readyz", http.StatusServiceUnavailable, false},
		{"/readyz?report=full", http.StatusOK, true},
		{"/readyz?report=unknown", http.StatusServiceUnavailable, false},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s status %d want %d", tc.path, w.Code, tc.code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["status"] != "unavailable" || body["invoice_ready"] != false {
			t.Fatalf("sampling hid original readiness: %s", w.Body.String())
		}
		if tc.report && body["report_schema"] != "xingmang.readiness-evaluation/v1" {
			t.Fatal("full report has no version binding")
		}
		if !tc.report && body["report_schema"] != nil {
			t.Fatal("ordinary readiness gained a sampling-success claim")
		}
		if strings.Contains(w.Body.String(), "private") {
			t.Fatal("sampling exposed private cause")
		}
	}
	if m.readyCalls != 3 {
		t.Fatal("each request must evaluate invoice exactly once")
	}
}

// The untouched module remains unclassified on these short-circuit paths.
// A runbook must not describe not_evaluated as universally unreachable.
func TestUnifiedShortCircuitLeavesUnclassifiedModuleNotEvaluated(t *testing.T) {
	for check, unclassified := range map[string]string{
		"source_health_query":      "invoice_projection",
		"eligibility_health_query": "invoice_sources",
	} {
		t.Run(check, func(t *testing.T) {
			m := &testModule{handler: http.NotFoundHandler(), check: check, err: errors.New("unavailable")}
			h, err := NewHandler(http.NotFoundHandler(), m, func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			var body struct {
				InvoiceReady bool                   `json:"invoice_ready"`
				Modules      map[string]moduleState `json:"modules"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			state, exists := body.Modules[unclassified]
			if response.Code != http.StatusServiceUnavailable || body.InvoiceReady || !exists || state.Ready || state.Status != "not_evaluated" {
				t.Fatalf("short-circuit classification changed: status=%d response=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestUnifiedHeadReadinessChecksInvoiceWithoutResponseBody(t *testing.T) {
	m := &testModule{handler: http.NotFoundHandler(), err: errors.New("unavailable")}
	h, err := NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(405) }), m, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("HEAD", "/readyz", nil))
	if w.Code != 503 || m.readyCalls != 1 || w.Body.Len() != 0 {
		t.Fatalf("HEAD probe bypassed invoice: status=%d probes=%d body=%s", w.Code, m.readyCalls, w.Body.String())
	}
}

func TestUnifiedReadyReportsEachModuleWithoutInventingUnrunChecks(t *testing.T) {
	for _, tc := range []struct{ check, sources, projection string }{
		{"", "ready", "ready"},
		{"database", "not_ready", "not_ready"},
		{"admin_settings", "not_ready", "not_ready"},
		{"invoice_issuer", "not_ready", "not_ready"},
		{"clamav_daemon", "not_ready", "not_ready"},
		{"clamav_signatures", "not_ready", "not_ready"},
		{"pdf_scanner", "not_ready", "not_ready"},
		{"source_health_query", "not_ready", "not_evaluated"},
		{"source_ingest", "not_ready", "not_evaluated"},
		{"source_ingest_dead_events", "not_ready", "not_evaluated"},
		{"eligibility_health_query", "not_evaluated", "not_ready"},
		{"eligibility_projection", "not_evaluated", "not_ready"},
		{"eligibility_projection_dead_jobs", "not_evaluated", "not_ready"},
		{"eligibility_projection_stuck", "not_evaluated", "not_ready"},
		{"source_streams", "not_ready", "ready"},
		{"source_stream_dead_events", "not_ready", "ready"},
		{"unrecognized_dependency", "not_ready", "not_ready"},
	} {
		t.Run(tc.check, func(t *testing.T) {
			m := &testModule{handler: http.NotFoundHandler(), check: tc.check}
			if tc.check != "" {
				m.err = errors.New("private dependency detail")
			}
			h, err := NewHandler(http.NotFoundHandler(), m, func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
			var body struct {
				Status       string `json:"status"`
				InvoiceReady bool   `json:"invoice_ready"`
				Modules      map[string]struct {
					Ready  bool   `json:"ready"`
					Status string `json:"status"`
				} `json:"modules"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantCode, wantStatus := 503, "unavailable"
			if tc.check == "" {
				wantCode, wantStatus = 200, "ready"
			}
			if w.Code != wantCode || body.Status != wantStatus || body.InvoiceReady != (tc.check == "") {
				t.Fatalf("platform availability or complete invoice verdict lost: status=%d body=%s", w.Code, w.Body.String())
			}
			if len(body.Modules) != 3 || !body.Modules["platform"].Ready || body.Modules["platform"].Status != "ready" {
				t.Fatalf("platform module missing: %s", w.Body.String())
			}
			for key, want := range map[string]string{"invoice_sources": tc.sources, "invoice_projection": tc.projection} {
				got := body.Modules[key]
				if got.Status != want || got.Ready != (want == "ready") {
					t.Errorf("module=%s got=%+v want=%s body=%s", key, got, want, w.Body.String())
				}
			}
			if strings.Contains(w.Body.String(), "private") || w.Header().Get("Cache-Control") != "no-store" || m.readyCalls != 1 {
				t.Fatalf("public readiness diagnostics contract lost: %s", w.Body.String())
			}
		})
	}
}

func TestInvoiceReadinessRetainsItsOwnFailClosedHTTPStatus(t *testing.T) {
	var receivedPath string
	m := &testModule{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusServiceUnavailable)
	})}
	h, err := NewHandler(http.NotFoundHandler(), m, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/invoice-api/readyz", nil)
	h.ServeHTTP(w, r)
	if w.Code != 503 || receivedPath != "/readyz" || r.URL.Path != "/invoice-api/readyz" {
		t.Fatalf("invoice probe was weakened or request mutated: status=%d path=%s original=%s", w.Code, receivedPath, r.URL.Path)
	}
}
