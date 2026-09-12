package invoicehost

import (
	"context"
	"encoding/json"
	"errors"
	invoiceservice "invoice-system/backend/service"
	"net/http"
	"strings"
	"time"
)

type Module interface {
	Handler() http.Handler
	Readiness(context.Context) invoiceservice.ReadinessReport
}

func NewHandler(platform http.Handler, invoice Module, platformReady func(context.Context) error) (http.Handler, error) {
	if platform == nil || invoice == nil || platformReady == nil {
		return nil, errors.New("unified service dependencies are missing")
	}
	invoiceHandler := invoice.Handler()
	if invoiceHandler == nil {
		return nil, errors.New("invoice handler is missing")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == "/readyz" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			platformErr := platformReady(ctx)
			invoiceReport := invoice.Readiness(ctx)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			platformState := "ready"
			code := http.StatusOK
			if platformErr != nil {
				platformState = "not_ready"
				code = http.StatusServiceUnavailable
			}
			if !invoiceReport.Ready {
				code = http.StatusServiceUnavailable
			}
			sources, projection := invoiceModuleStates(invoiceReport)
			status := "ready"
			if code != http.StatusOK {
				status = "unavailable"
			}
			body := map[string]any{
				"status": status, "platform_ready": platformErr == nil,
				"invoice_ready": invoiceReport.Ready, "invoice": invoiceReport,
				"modules": map[string]moduleState{
					"platform":        {Ready: platformErr == nil, Status: platformState},
					"invoice_sources": sources, "invoice_projection": projection,
				},
			}
			query := r.URL.Query()
			if len(query) == 1 && len(query["report"]) == 1 && query.Get("report") == "full" {
				// HTTP success means the typed evaluation was sampled. Its original
				// readiness/body states remain unchanged and must be checked by the
				// operator; ordinary health checks retain their strict 200/503.
				body["report_schema"] = "xingmang.readiness-evaluation/v1"
				code = http.StatusOK
			}
			w.WriteHeader(code)
			if r.Method != http.MethodHead {
				_ = json.NewEncoder(w).Encode(body)
			}
			return
		}
		if r.URL.Path == "/invoice-api/readyz" {
			cloned := r.Clone(r.Context())
			cloned.URL.Path = "/readyz"
			cloned.URL.RawPath = ""
			cloned.RequestURI = cloned.URL.RequestURI()
			invoiceHandler.ServeHTTP(w, cloned)
			return
		}
		if r.URL.Path == "/internal/v1/source-batches" {
			// This handler still verifies the trusted mTLS ingress and source
			// credential. Public web ingress must not expose this internal path.
			invoiceHandler.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/invoice-api/v1" || strings.HasPrefix(r.URL.Path, "/invoice-api/v1/") {
			cloned := r.Clone(r.Context())
			cloned.URL.Path = "/api/v1" + strings.TrimPrefix(r.URL.Path, "/invoice-api/v1")
			cloned.URL.RawPath = ""
			cloned.RequestURI = cloned.URL.RequestURI()
			invoiceHandler.ServeHTTP(w, cloned)
			return
		}
		platform.ServeHTTP(w, r)
	}), nil
}

// A failed short-circuit check does not prove that a later check passed. Keep
// the invoice's original eleven readiness gates and expose only what they ran.
type moduleState struct {
	Ready  bool   `json:"ready"`
	Status string `json:"status"`
}

func invoiceModuleStates(report invoiceservice.ReadinessReport) (sources, projection moduleState) {
	sources, projection = moduleState{Status: "not_evaluated"}, moduleState{Status: "not_evaluated"}
	if report.Ready {
		return moduleState{Ready: true, Status: "ready"}, moduleState{Ready: true, Status: "ready"}
	}
	switch report.Check {
	case "database", "admin_settings", "invoice_issuer", "clamav_daemon", "clamav_signatures", "pdf_scanner":
		// Shared invoice prerequisites failed. Both invoice capabilities are
		// unavailable; this does not claim their later checks were executed.
		sources.Status, projection.Status = "not_ready", "not_ready"
	case "source_health_query", "source_ingest", "source_ingest_dead_events":
		sources.Status = "not_ready"
	case "eligibility_health_query", "eligibility_projection", "eligibility_projection_dead_jobs", "eligibility_projection_stuck":
		projection.Status = "not_ready"
	case "source_streams", "source_stream_dead_events":
		sources.Status = "not_ready"
		projection = moduleState{Ready: true, Status: "ready"}
	default:
		// Unconfigured/stopping runtime and unclassified failures must remain
		// visibly unavailable, never a platform-only healthy response.
		sources.Status, projection.Status = "not_ready", "not_ready"
	}
	return sources, projection
}
