package service

import (
	"context"
	"fmt"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"os"
	"strings"
	"time"
)

func loadAdminPolicy() (auth.AdminPolicy, error) {
	policy := auth.AdminPolicy{Role: os.Getenv("ADMIN_ROLE"), RequiredACR: "mfa", RequiredAMR: []string{"pwd", "otp"}, StepUpMaxAge: 10 * time.Minute}
	return policy, policy.Validate()
}

func loadProductionCSRF() (string, auth.CSRFPolicy, error) {
	publicOrigin, err := exactHTTPSOrigin(os.Getenv("PUBLIC_ORIGIN"))
	if err != nil {
		return "", auth.CSRFPolicy{}, err
	}
	staffOrigin, err := exactHTTPSOrigin(os.Getenv("INVOICE_STAFF_ORIGIN"))
	if err != nil {
		return "", auth.CSRFPolicy{}, fmt.Errorf("INVOICE_STAFF_ORIGIN: %w", err)
	}
	policy, err := auth.NewCSRFPolicy([]string{publicOrigin, staffOrigin})
	return publicOrigin, policy, err
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
