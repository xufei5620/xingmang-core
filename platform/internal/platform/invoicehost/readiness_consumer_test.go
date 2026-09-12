package invoicehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	invoiceservice "invoice-system/backend/service"
)

// Read the shipped healthcheck, not a second test-owned probe URL. Exercise
// real HTTP responses at that path. Image-local wget is covered by deployment
// qualification; this portable test does not start Docker or simulate a shell.
func TestComposeHealthcheckConsumesStrictUnifiedReadiness(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "deploy", "unified", "compose.json"))
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Healthcheck struct {
				Test []string `json:"test"`
			} `json:"healthcheck"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &compose); err != nil {
		t.Fatal(err)
	}
	command := compose.Services["platform-api"].Healthcheck.Test
	if len(command) != 2 || command[0] != "CMD-SHELL" {
		t.Fatalf("healthcheck changed execution mode: %v", command)
	}
	match := regexp.MustCompile(`^wget -q -O- (http://127\.0\.0\.1:8080/[^ ]+) \|\| exit 1$`).FindStringSubmatch(command[1])
	if len(match) != 2 {
		t.Fatalf("healthcheck must propagate HTTP failure: %v", command)
	}
	endpoint, err := url.Parse(match[1])
	if err != nil {
		t.Fatal(err)
	}
	nginx, err := os.ReadFile(filepath.Join(root, "deploy", "unified", "nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	locations := regexp.MustCompile(`location = `+regexp.QuoteMeta(endpoint.Path)+`\s*\{\s*proxy_pass http://\$unified_api;\s*\}`).FindAll(nginx, -1)
	if len(locations) != 2 {
		t.Fatal("both public web entry points must forward the healthcheck path to the Go handler")
	}
	module := &testModule{handler: http.NotFoundHandler()}
	handler, err := NewHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), module, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	// Eleven original sequential gates, plus the four named sub-failure paths.
	for _, check := range []string{"", "database", "admin_settings", "invoice_issuer", "clamav_daemon", "clamav_signatures", "pdf_scanner", "source_health_query", "source_ingest", "eligibility_health_query", "eligibility_projection", "source_streams", "source_ingest_dead_events", "eligibility_projection_dead_jobs", "eligibility_projection_stuck", "source_stream_dead_events"} {
		t.Run(check, func(t *testing.T) {
			module.check, module.err = check, nil
			if check != "" {
				module.err = errors.New("private dependency detail")
			}
			response, err := server.Client().Get(server.URL + endpoint.RequestURI())
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := http.StatusServiceUnavailable
			if check == "" {
				want = http.StatusOK
			}
			if response.StatusCode != want {
				t.Fatalf("shipped probe incorrectly classifies %q: HTTP %d, want %d", check, response.StatusCode, want)
			}
			if strings.Contains(string(body), "private dependency") {
				t.Fatal("public probe leaked private error")
			}
		})
	}
}

type unconfiguredInvoice struct{ runtime invoiceservice.Runtime }

func (*unconfiguredInvoice) Handler() http.Handler { return http.NotFoundHandler() }
func (m *unconfiguredInvoice) Readiness(ctx context.Context) invoiceservice.ReadinessReport {
	return m.runtime.Readiness(ctx)
}

func TestUnifiedMissingOrUnconfiguredInvoiceCannotBecomeHealthy(t *testing.T) {
	platform := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	if handler, err := NewHandler(platform, nil, func(context.Context) error { return nil }); err == nil || handler != nil {
		t.Fatal("unified service must reject a missing invoice module, not silently enter platform-only mode")
	}
	handler, err := NewHandler(platform, &unconfiguredInvoice{}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"invoice_ready":false`) || strings.Contains(w.Body.String(), `"not_evaluated"`) {
		t.Fatalf("unconfigured invoice readiness must be explicitly unavailable: %d %s", w.Code, w.Body.String())
	}
}
