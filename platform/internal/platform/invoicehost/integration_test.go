package invoicehost

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	invoiceservice "invoice-system/backend/service"
)

type testPinger struct{}

func (testPinger) Ping(context.Context) error { return nil }

// The real module and platform router share one in-process HTTP handler. No
// second server or transport is supplied to the invoice module.
func TestCombinedRealModuleAndPlatformRouter(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("AUTH_MODE", "mock")
	t.Setenv("DOCUMENT_ROOT", "")
	t.Setenv("ADMIN_IP_ALLOWLIST", "127.0.0.1/32")
	t.Setenv("ADMIN_BREAK_GLASS_CIDRS", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	module, err := invoiceservice.New(ctx, invoiceservice.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = module.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if err := module.Shutdown(closeCtx); err != nil {
			t.Error(err)
		}
	})
	resolver, err := httpapi.NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	platform := httpapi.NewRouter(httpapi.Deps{Service: "unified-test", Environment: "development", DB: testPinger{}, Resolver: resolver, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h, err := NewHandler(platform, module, testPinger{}.Ping)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/healthz", 200}, {"/readyz", 200},
		{"/invoice-api/v1/user/funding-lots", 200},
		{"/invoice-api/v1/auth/login", 404},
		{"/invoice-api/v1/auth/callback", 404},
		{"/invoice-api/v1/auth/console-assertion", 404},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.RemoteAddr = "127.0.0.1:54321"
			r.Header.Set("X-Mock-User-ID", "user-1")
			r.Header.Set("X-Mock-Role", "user")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}
