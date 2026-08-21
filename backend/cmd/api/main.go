package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := buildRuntime(ctx)
	if err != nil {
		slog.Error("invoice service startup rejected", "error", err)
		os.Exit(1)
	}
	defer runtime.Close()

	var workers sync.WaitGroup
	for _, worker := range runtime.Workers {
		workers.Add(1)
		go func(spec workerSpec) {
			defer workers.Done()
			runWorker(ctx, spec)
		}(worker)
	}
	server := &http.Server{Addr: env("HTTP_ADDR", ":8088"), Handler: runtime.API.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("invoice API listening", "addr", server.Addr, "auth_mode", runtime.AuthMode, "source_mode", runtime.SourceMode)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("invoice API stopped", "error", err)
		os.Exit(1)
	}
	stop()
	workers.Wait()
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func csvEnv(key, fallback string) []string {
	value := env(key, fallback)
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func loadBreakGlassCIDRs(authMode string) ([]string, error) {
	if path := strings.TrimSpace(os.Getenv("ADMIN_BREAK_GLASS_CIDRS_FILE")); path != "" {
		body, err := readBoundedConfigFile(path, 64<<10)
		if err != nil {
			return nil, fmt.Errorf("read ADMIN_BREAK_GLASS_CIDRS_FILE: %w", err)
		}
		values := splitCIDRs(string(body))
		if len(values) == 0 {
			return nil, fmt.Errorf("ADMIN_BREAK_GLASS_CIDRS_FILE is empty")
		}
		return values, nil
	}
	if authMode == "mock" {
		return csvEnv("ADMIN_BOOTSTRAP_IP_ALLOWLIST", "127.0.0.1/32,::1/128"), nil
	}
	return nil, fmt.Errorf("ADMIN_BREAK_GLASS_CIDRS_FILE is required outside mock mode")
}

func splitCIDRs(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' ' })
}

func seedMock(service *ledger.Service) {
	now := time.Now()
	lots := []domain.FundingLot{
		{ID: "sub2-order-1001", PrincipalID: "demo-user", SourceInstanceID: "sub2-main", SourceType: domain.SourceSub2API, ExternalOrderID: "1001", TradeNo: "sub2_demo_1001", Currency: domain.CurrencyCNY, OriginalMinor: 50_000, CurrentCapMinor: 50_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED", CompletedAt: now.AddDate(0, 0, -8), SourceRevision: "mock-v1"},
		{ID: "sub2-order-1002", PrincipalID: "demo-user", SourceInstanceID: "sub2-main", SourceType: domain.SourceSub2API, ExternalOrderID: "1002", TradeNo: "sub2_demo_1002", Currency: domain.CurrencyCNY, OriginalMinor: 30_000, CurrentCapMinor: 30_000, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED", CompletedAt: now.AddDate(0, 0, -2), SourceRevision: "mock-v1"},
		{ID: "newapi-topup-21", PrincipalID: "demo-user", SourceInstanceID: "newapi-main", SourceType: domain.SourceNewAPI, ExternalOrderID: "21", TradeNo: "newapi_demo_21", Currency: domain.CurrencyCNY, OriginalMinor: 40_000, CurrentCapMinor: 40_000, Verification: domain.VerificationPending, SourceStatus: "success", CompletedAt: now.AddDate(0, 0, -1), SourceRevision: "mock-v1"},
	}
	for _, lot := range lots {
		_ = service.AddFundingLot(context.Background(), lot)
	}
}
