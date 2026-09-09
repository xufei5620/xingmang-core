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

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

type sourceConfig struct {
	Sources []struct {
		ID                             string   `json:"id"`
		Type                           string   `json:"type"`
		Name                           string   `json:"name"`
		RuntimeVersion                 string   `json:"runtime_version"`
		ExpectedPreviousRuntimeVersion *string  `json:"expected_previous_runtime_version,omitempty"`
		Streams                        []string `json:"streams"`
	} `json:"sources"`
}

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute owner database URL secret file")
	configFile := flag.String("config-file", "", "source instances JSON")
	flag.Parse()
	if err := run(context.Background(), *databaseURLFile, *configFile); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap-sources: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURLFile, configFile string) error {
	databaseURL, err := readSecret(databaseURLFile)
	if err != nil {
		return err
	}
	config, err := readConfig(configFile)
	if err != nil {
		return err
	}
	store, err := postgresstore.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	for _, source := range config.Sources {
		sourceType := domain.SourceType(source.Type)
		if source.ID == "" || !sourceType.Valid() || strings.TrimSpace(source.Name) == "" || len(source.Streams) == 0 {
			return errors.New("source identity, type, name and streams are required")
		}
		requestID := fmt.Sprintf("source-bootstrap-%d", time.Now().UTC().UnixNano())
		actor := postgresstore.AuditActor{Type: "system", ID: "deployment-bootstrap", RequestID: requestID,
			Reason: "source instance and runtime version explicitly approved"}
		saved, saveErr := store.ApproveSourceInstance(ctx, postgresstore.SourceInstanceRecord{
			ID: source.ID, SourceType: sourceType, Name: source.Name,
			RuntimeVersion: source.RuntimeVersion, Enabled: true,
		}, source.ExpectedPreviousRuntimeVersion, actor)
		if saveErr != nil {
			return saveErr
		}
		seenStreams := map[string]bool{}
		for _, stream := range source.Streams {
			if stream != "payments" && stream != "identities" && stream != "usage" && stream != "credits" && stream != "balances" {
				return fmt.Errorf("unsupported stream %q", stream)
			}
			if seenStreams[stream] {
				return fmt.Errorf("duplicate stream %q", stream)
			}
			seenStreams[stream] = true
			if err = store.ProvisionSourceStream(ctx, saved.ID, stream, postgresstore.AuditActor{
				Type: "system", ID: "deployment-bootstrap", RequestID: requestID,
				Reason: "source stream explicitly provisioned",
			}); err != nil {
				return err
			}
			fmt.Printf("provisioned source=%s type=%s stream=%s\n", saved.ID, saved.SourceType, stream)
		}
		for _, required := range []string{"payments", "identities", "usage", "credits", "balances"} {
			if !seenStreams[required] {
				return fmt.Errorf("source %s is missing required stream %q", saved.ID, required)
			}
		}
	}
	return nil
}

func readConfig(path string) (sourceConfig, error) {
	body, err := readFile(path)
	if err != nil {
		return sourceConfig{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config sourceConfig
	if err = decoder.Decode(&config); err != nil {
		return sourceConfig{}, err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return sourceConfig{}, errors.New("source config must contain exactly one JSON object")
	}
	if len(config.Sources) == 0 || len(config.Sources) > 16 {
		return sourceConfig{}, errors.New("source config must contain 1 to 16 sources")
	}
	return config, nil
}

func readSecret(path string) (string, error) {
	body, err := readFile(path)
	if err != nil {
		return "", err
	}
	body = bytes.TrimSuffix(body, []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", errors.New("database URL secret must contain one line")
	}
	return string(body), nil
}

func readFile(path string) ([]byte, error) {
	if !filepath.IsAbs(strings.TrimSpace(path)) {
		return nil, errors.New("file path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > 64<<10 {
		return nil, errors.New("file has invalid size")
	}
	return body, nil
}
