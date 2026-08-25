package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/postgresstore"
)

const maxBootstrapFileBytes int64 = 64 << 10

type bootstrapSettings struct {
	IssuerName          string   `json:"issuer_name"`
	MinimumRequestMinor int64    `json:"minimum_request_minor"`
	EligibilityStartAt  string   `json:"eligibility_start_at"`
	SMTPHost            string   `json:"smtp_host"`
	SMTPPort            int      `json:"smtp_port"`
	SMTPFrom            string   `json:"smtp_from"`
	SMTPFromName        string   `json:"smtp_from_name"`
	SMTPStartTLS        bool     `json:"smtp_starttls"`
	AdminCIDRs          []string `json:"admin_cidrs"`
}

func main() {
	databaseURLFile := flag.String("database-url-file", "", "read-only path containing the migration/bootstrap PostgreSQL URL")
	settingsFile := flag.String("settings-file", "", "non-secret initial settings JSON; initialization refuses overwrite")
	actorID := flag.String("actor-id", "deployment-bootstrap", "non-secret audit actor ID")
	flag.Parse()
	if err := run(context.Background(), *databaseURLFile, *settingsFile, *actorID); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap-settings: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURLFile, settingsFile, actorID string) error {
	databaseURL, err := readOneLineSecret(databaseURLFile)
	if err != nil {
		return fmt.Errorf("database URL file: %w", err)
	}
	settings, err := readBootstrapSettings(settingsFile)
	if err != nil {
		return err
	}
	store, err := postgresstore.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	eligibilityStartAt, err := time.Parse(time.RFC3339, strings.TrimSpace(settings.EligibilityStartAt))
	if err != nil {
		return fmt.Errorf("eligibility_start_at: %w", err)
	}
	service := adminsettings.NewService(adminsettings.NewPostgresRepository(store.Pool()), nil)
	updated, err := service.Bootstrap(ctx, adminsettings.UpdateInput{
		IssuerName: settings.IssuerName, MinimumRequestMinor: settings.MinimumRequestMinor,
		EligibilityStartAt: eligibilityStartAt.UTC(),
		SMTPHost:           settings.SMTPHost, SMTPPort: settings.SMTPPort,
		SMTPFrom: settings.SMTPFrom, SMTPFromName: settings.SMTPFromName,
		SMTPStartTLS: settings.SMTPStartTLS, AdminCIDRs: settings.AdminCIDRs,
	}, adminsettings.Actor{
		ID: strings.TrimSpace(actorID), RequestID: fmt.Sprintf("bootstrap-%d", time.Now().UTC().UnixNano()),
		Reason: "initial production settings bootstrap",
	})
	if errors.Is(err, adminsettings.ErrRevisionConflict) {
		return errors.New("settings already exist; use the authenticated administrator UI instead of overwriting bootstrap state")
	}
	if err != nil {
		return err
	}
	fmt.Printf("initialized admin settings revision=%d secret_configured=%t\n", updated.Revision, updated.SMTPSecretConfigured)
	return nil
}

func readBootstrapSettings(path string) (bootstrapSettings, error) {
	body, err := readBoundedFile(path)
	if err != nil {
		return bootstrapSettings{}, fmt.Errorf("settings file: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var settings bootstrapSettings
	if err = decoder.Decode(&settings); err != nil {
		return bootstrapSettings{}, fmt.Errorf("decode settings file: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return bootstrapSettings{}, errors.New("settings file must contain exactly one JSON object")
	}
	return settings, nil
}

func readOneLineSecret(path string) (string, error) {
	if !filepath.IsAbs(strings.TrimSpace(path)) {
		return "", errors.New("secret file path must be absolute")
	}
	body, err := readBoundedFile(path)
	if err != nil {
		return "", err
	}
	if len(body) >= 2 && body[len(body)-2] == '\r' && body[len(body)-1] == '\n' {
		body = body[:len(body)-2]
	} else if len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", errors.New("secret file is empty or contains multiple lines")
	}
	return string(body), nil
}

func readBoundedFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxBootstrapFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBootstrapFileBytes {
		return nil, errors.New("file is too large")
	}
	return body, nil
}
