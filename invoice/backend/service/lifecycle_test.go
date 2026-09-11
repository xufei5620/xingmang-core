package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"invoice-system/backend/internal/httpapi"
)

func TestRunWorkerDoesNotStartCanceledWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	runWorker(ctx, workerSpec{Name: "fixture", Worker: workerFunc(func(context.Context) (int, error) {
		calls++
		return 0, nil
	})})
	if calls != 0 {
		t.Fatalf("canceled module started %d worker calls", calls)
	}
}

func TestModuleStartsWorkersOnceAndDrainsBeforeClosingResources(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var closed atomic.Int32
	module := newRuntime(appRuntime{Workers: []workerSpec{{Name: "fixture", Worker: workerFunc(func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return 0, ctx.Err()
	})}}, close: func() { closed.Add(1) }})
	if err := module.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := module.Start(context.Background()); err == nil {
		t.Fatal("module started duplicate workers")
	}
	deadline, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := module.Shutdown(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown did not report worker still running: %v", err)
	}
	<-canceled
	if closed.Load() != 0 {
		t.Fatal("database closed while a worker was still running")
	}
	close(release)
	drain, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := module.Shutdown(drain); err != nil {
		t.Fatal(err)
	}
	if err := module.Shutdown(drain); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("resources closed %d times", closed.Load())
	}
	if err := module.Start(context.Background()); err == nil {
		t.Fatal("closed runtime restarted")
	}
}

func TestModuleShutdownBeforeStartIsSafeAndStartRejectsCanceledContext(t *testing.T) {
	var closed atomic.Int32
	module := newRuntime(appRuntime{close: func() { closed.Add(1) }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := module.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start accepted: %v", err)
	}
	if err := module.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := module.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("resources closed %d times", closed.Load())
	}
	if err := module.Start(context.Background()); err == nil {
		t.Fatal("shutdown runtime accepted new start")
	}
}

func TestModuleHandlerIsMountedDirectlyWithoutAListener(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("AUTH_MODE", "mock")
	t.Setenv("ADMIN_BREAK_GLASS_CIDRS_FILE", "")
	t.Setenv("DOCUMENT_ROOT", "")
	t.Setenv("ADMIN_IP_ALLOWLIST", "127.0.0.1/32")
	t.Setenv("ADMIN_BOOTSTRAP_IP_ALLOWLIST", "127.0.0.1/32")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	module, err := New(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer module.Shutdown(context.Background())
	if module.AuthMode() != "mock" || module.SourceMode() != "mock" {
		t.Fatal("runtime modes not preserved")
	}
	if err := module.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/invoice/", http.StripPrefix("/invoice", module.Handler()))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/invoice/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("direct invoice handler unavailable: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestModuleReadyInvokesTheOriginalProbeAndFailsClosed(t *testing.T) {
	probe := healthyReadinessProbe(time.Now().UTC())
	module := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent", readiness: func(ctx context.Context) (httpapi.ReadinessOutcome, error) { return probe.evaluate(ctx) }})
	if err := module.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	databaseFailure := errors.New("fixture database unavailable")
	probe.pingDatabase = func(context.Context) error { return databaseFailure }
	if err := module.Ready(context.Background()); !errors.Is(err, databaseFailure) {
		t.Fatalf("original readiness failure hidden: %v", err)
	}
	if err := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent"}).Ready(context.Background()); err == nil {
		t.Fatal("production ready without a probe")
	}
	if err := module.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := module.Ready(context.Background()); err == nil {
		t.Fatal("shutdown module reported ready")
	}
}

func TestProductionModuleRequiresHostStaffResolverBeforeOpeningResources(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_MODE", "session")
	t.Setenv("SOURCE_MODE", "agent")
	t.Setenv("DATABASE_URL_FILE", "")
	_, err := New(context.Background(), Options{})
	if err == nil || err.Error() != "production requires a trusted in-process staff resolver" {
		t.Fatalf("missing host resolver was not rejected before resources: %v", err)
	}
}
