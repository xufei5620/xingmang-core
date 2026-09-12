// Package service embeds the invoice HTTP API and background workers in the
// platform process while retaining the invoice database and financial services.
package service

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"invoice-system/backend/internal/httpapi"
	"invoice-system/backend/staffauth"
)

// Options contains host-process integrations. It does not expose the invoice
// repository, database pool, or financial implementation to the host.
type Options struct {
	StaffResolver staffauth.Resolver
}

// ReadinessReport contains only public, filtered readiness diagnostics.
// Dependency errors are recorded in application logs, never in this report.
type ReadinessReport struct {
	Ready                    bool                  `json:"ready"`
	Check                    string                `json:"check,omitempty"`
	Summary                  string                `json:"summary,omitempty"`
	Degraded                 []string              `json:"degraded,omitempty"`
	Checks                   map[string]GateReport `json:"checks"`
	SourceNonFreshnessStatus GateStatus            `json:"source_non_freshness_status"`
	SourceFreshness          SourceFreshnessReport `json:"source_freshness"`
}

func (r *Runtime) Readiness(ctx context.Context) ReadinessReport {
	report, _ := r.readinessReport(ctx)
	return report
}

// Runtime owns one invoice handler, its workers, and its independent resources.
// The host owns the HTTP listener and must drain HTTP requests before Shutdown.
type Runtime struct {
	app           appRuntime
	mu            sync.Mutex
	started       bool
	closing       bool
	cancel        context.CancelFunc
	workerContext context.Context
	workers       sync.WaitGroup
	diagnostics   httpapi.ReadinessDiagnostics
	done          chan struct{}
}

// New validates and constructs the invoice module; it starts no listener or
// worker. Failed construction closes any partially acquired resources.
func New(ctx context.Context, options Options) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app, err := buildRuntime(ctx, options)
	if err != nil {
		return nil, err
	}
	return newRuntime(app), nil
}

func newRuntime(app appRuntime) *Runtime { return &Runtime{app: app, done: make(chan struct{})} }

func (r *Runtime) Handler() http.Handler { return r.app.API.Handler() }
func (r *Runtime) AuthMode() string      { return r.app.AuthMode }
func (r *Runtime) SourceMode() string    { return r.app.SourceMode }

// Ready invokes the same readiness evaluation as the invoice API, including
// its source freshness and document-scanner checks. It uses no HTTP loopback.
func (r *Runtime) Ready(ctx context.Context) error {
	_, err := r.readinessReport(ctx)
	return err
}

func (r *Runtime) readinessReport(ctx context.Context) (ReadinessReport, error) {
	outcome, detailed, err := r.evaluateReadinessWithReport(ctx)
	report := r.diagnostics.Report(outcome, err, nil)
	detailed = detailed.filteredDiagnostics()
	detailed.Ready, detailed.Check, detailed.Summary, detailed.Degraded = report.Ready, report.Check, report.Summary, report.Degraded
	return detailed, err
}

func (r *Runtime) evaluateReadiness(ctx context.Context) (httpapi.ReadinessOutcome, error) {
	outcome, _, err := r.evaluateReadinessWithReport(ctx)
	return outcome, err
}

func (r *Runtime) evaluateReadinessWithReport(ctx context.Context) (httpapi.ReadinessOutcome, ReadinessReport, error) {
	detailed := unevaluatedReadinessReport()
	if err := ctx.Err(); err != nil {
		return httpapi.ReadinessOutcome{}, detailed, err
	}
	r.mu.Lock()
	closing := r.closing
	workerContext := r.workerContext
	r.mu.Unlock()
	if closing {
		return httpapi.ReadinessOutcome{}, detailed, errors.New("invoice runtime is stopping")
	}
	if workerContext != nil && workerContext.Err() != nil {
		return httpapi.ReadinessOutcome{}, detailed, errors.New("invoice runtime workers are stopping")
	}
	if r.app.readinessWithReport != nil {
		return r.app.readinessWithReport(ctx)
	}
	if r.app.readiness == nil {
		if r.app.AuthMode == "mock" && r.app.SourceMode == "mock" {
			return httpapi.ReadinessOutcome{}, detailed, nil
		}
		return httpapi.ReadinessOutcome{}, detailed, errors.New("invoice runtime readiness is not configured")
	}
	outcome, err := r.app.readiness(ctx)
	return outcome, detailed, err
}

// Start launches each worker once, using the host's cancellation context.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil || r.started || r.closing {
		return errors.New("invoice runtime cannot be started in its current state")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	r.cancel, r.started = cancel, true
	r.workerContext = workerCtx
	r.workers.Add(len(r.app.Workers))
	for _, spec := range r.app.Workers {
		go func(spec workerSpec) {
			defer r.workers.Done()
			runWorker(workerCtx, spec)
		}(spec)
	}
	return nil
}

// Shutdown stops workers and closes resources once, after every worker exits.
// A caller deadline reports incomplete drainage without closing a live worker's
// database. The same shutdown can subsequently be awaited again.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.done == nil {
		r.mu.Unlock()
		return errors.New("invoice runtime is not initialized")
	}
	if !r.closing {
		r.closing = true
		if r.cancel != nil {
			r.cancel()
		}
		go func() {
			r.workers.Wait()
			r.app.Close()
			close(r.done)
		}()
	}
	done := r.done
	r.mu.Unlock()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
