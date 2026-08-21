package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"invoice-system/agents/sourceagent"
)

func validEnvironment(t *testing.T, sourceType, stream string) map[string]string {
	t.Helper()
	directory := t.TempDir()
	abs := func(name string) string { return filepath.Join(directory, name) }
	sourceID := "10000000-0000-4000-8000-000000000001"
	if sourceType == "newapi" {
		sourceID = "10000000-0000-4000-8000-000000000002"
	}
	values := map[string]string{
		"SOURCE_MODE":                      "db_projection",
		"SOURCE_TYPE":                      sourceType,
		"SOURCE_ID":                        sourceID,
		"SOURCE_RUNTIME_VERSION":           "test-runtime",
		"SOURCE_MAX_CONSECUTIVE_FAILURES":  "10",
		"SOURCE_DB_DSN_FILE":               abs("dsn"),
		"SOURCE_DB_DIALECT":                "postgres",
		"SOURCE_STATE_FILE":                abs("state.json"),
		"SOURCE_RECONCILE_FILE":            abs("reconcile.json"),
		"SOURCE_RECONCILE_MISS_THRESHOLD":  "3",
		"SOURCE_STATE_STREAM":              stream,
		"SOURCE_SPOOL_FILE":                abs("pending.enc"),
		"SOURCE_SPOOL_KEY_FILE":            abs("spool.key"),
		"INGESTION_ORIGIN":                 "https://invoice.example.invalid",
		"INGESTION_ALLOWED_HOSTS":          "invoice.example.invalid",
		"INGESTION_ALLOWED_PORTS":          "443",
		"INGESTION_ALLOWED_CIDRS":          "203.0.113.10/32",
		"INGESTION_ALLOWED_METHODS":        "POST",
		"SOURCE_MTLS_CERT_FILE":            abs("client.crt"),
		"SOURCE_MTLS_KEY_FILE":             abs("client.key"),
		"SOURCE_MTLS_CA_FILE":              abs("ca.crt"),
		"SOURCE_MTLS_RELOAD_ON_HANDSHAKE":  "true",
		"SOURCE_SIGNING_KEY_FILE":          abs("signing.key"),
		"SOURCE_SIGNING_KEY_ID":            "key-1",
		"SOURCE_TRUSTED_OIDC_PROVIDER_KEY": "solov-sso",
		"SOURCE_TRUSTED_OIDC_ISSUER":       "https://id.example.invalid/realms/central",
	}
	return values
}

func TestBuildDBConnectorMapsEveryProductionStream(t *testing.T) {
	database := new(sql.DB)
	tests := []struct {
		source string
		stream string
		assert func(sourceagent.Connector) bool
	}{
		{"sub2api", "payments", func(value sourceagent.Connector) bool { _, ok := value.(*sourceagent.Sub2APIDBConnector); return ok }},
		{"sub2api", "identities", func(value sourceagent.Connector) bool {
			_, ok := value.(*sourceagent.Sub2APIIdentityDBConnector)
			return ok
		}},
		{"newapi", "payments", func(value sourceagent.Connector) bool { _, ok := value.(*sourceagent.NewAPIDBConnector); return ok }},
		{"newapi", "identities", func(value sourceagent.Connector) bool {
			_, ok := value.(*sourceagent.NewAPIIdentityDBConnector)
			return ok
		}},
	}
	for _, test := range tests {
		config := runConfig{
			SourceType: test.source, StreamID: test.stream,
			OIDCProviderKey: "solov-sso", OIDCIssuer: "https://id.example.invalid/realms/central",
		}
		connector, err := buildDBConnector(config, database)
		if err != nil || !test.assert(connector) {
			t.Fatalf("%s/%s connector=%T err=%v", test.source, test.stream, connector, err)
		}
	}
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadRunConfigAcceptsIndependentProductionStreams(t *testing.T) {
	for _, test := range []struct {
		source string
		stream string
	}{
		{source: "sub2api", stream: "payments"},
		{source: "sub2api", stream: "identities"},
		{source: "newapi", stream: "payments"},
		{source: "newapi", stream: "identities"},
	} {
		t.Run(test.source+"-"+test.stream, func(t *testing.T) {
			config, err := loadRunConfig(mapEnvironment(validEnvironment(t, test.source, test.stream)))
			if err != nil {
				t.Fatal(err)
			}
			if config.SourceType != test.source || config.StreamID != test.stream || config.FullScanInterval != time.Hour {
				t.Fatalf("unexpected config: %#v", config)
			}
		})
	}
}

func TestLoadRunConfigAcceptsV3EconomicStreamsWithIsolatedState(t *testing.T) {
	for _, stream := range []string{sourceagent.StreamPayments, sourceagent.StreamUsage, sourceagent.StreamCredits, sourceagent.StreamBalances} {
		values := validEnvironment(t, sourceagent.SourceSub2API, stream)
		directory := filepath.Dir(values["SOURCE_STATE_FILE"])
		values["SOURCE_SCHEMA_VERSION"] = sourceagent.SchemaVersionV3
		values["SOURCE_CUTOVER_MANIFEST_FILE"] = filepath.Join(directory, "cutover.enc")
		values["SOURCE_CUTOVER_KEY_FILE"] = filepath.Join(directory, "cutover.key")
		if stream == sourceagent.StreamBalances {
			values["SOURCE_BALANCE_BASELINE_FILE"] = filepath.Join(directory, "baseline.enc")
			values["SOURCE_BALANCE_SNAPSHOT_FILE"] = filepath.Join(directory, "snapshot.enc")
			values["SOURCE_BALANCE_SNAPSHOT_KEY_FILE"] = filepath.Join(directory, "snapshot.key")
		}
		config, err := loadRunConfig(mapEnvironment(values))
		if err != nil {
			t.Fatalf("stream %s: %v", stream, err)
		}
		if config.ProtocolVersion != sourceagent.SchemaVersionV3 || config.StreamID != stream {
			t.Fatalf("unexpected V3 config: %#v", config)
		}
	}
}

func TestLoadRunConfigRejectsMockAndDirectSecrets(t *testing.T) {
	values := validEnvironment(t, "newapi", "payments")
	values["SOURCE_MODE"] = "mock"
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("production launcher accepted mock mode")
	}
	values["SOURCE_MODE"] = "db_projection"
	values["SOURCE_ID"] = "newapi-primary"
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("production launcher accepted a display slug instead of registered source UUID")
	}
	values["SOURCE_ID"] = "10000000-0000-4000-8000-000000000002"
	values["SOURCE_DB_DSN"] = "postgres://must-not-be-in-env"
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("production launcher accepted a direct DSN environment secret")
	}
	delete(values, "SOURCE_DB_DSN")
	values["PGPASSWORD"] = "ambient-secret"
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("production launcher accepted ambient pgx credential configuration")
	}
}

func TestLoadRunConfigRejectsMutableStateSecretPathAlias(t *testing.T) {
	values := validEnvironment(t, "sub2api", "payments")
	values["SOURCE_SPOOL_FILE"] = values["SOURCE_SPOOL_KEY_FILE"]
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("mutable spool path was allowed to overwrite its encryption key")
	}
	values = validEnvironment(t, "sub2api", "payments")
	values["SOURCE_SPOOL_FILE"] = values["SOURCE_STATE_FILE"]
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("state and pending spool were allowed to alias")
	}
}

func TestLoadRunConfigRejectsInvalidReconcileAndIdentityTrustMetadata(t *testing.T) {
	values := validEnvironment(t, "sub2api", "payments")
	values["SOURCE_RECONCILE_MISS_THRESHOLD"] = "1"
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("single-scan tombstone threshold was accepted")
	}
	values = validEnvironment(t, "newapi", "identities")
	values["SOURCE_TRUSTED_OIDC_ISSUER"] = "https://id.example.invalid/realms/central/"
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("non-canonical OIDC issuer was accepted")
	}
	values = validEnvironment(t, "sub2api", "payments")
	values["SOURCE_SCAN_LIMIT"] = "167"
	if _, err := loadRunConfig(mapEnvironment(values)); err == nil {
		t.Fatal("Sub2API page capable of exceeding the 500-record batch cap was accepted")
	}
}

func TestReadSecretFileRequiresPrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(path, []byte("postgres://source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := readSecretFile(path, 1024)
	if err != nil || string(raw) != "postgres://source" {
		t.Fatalf("read private secret: raw=%q err=%v", raw, err)
	}
	if err := os.WriteFile(path, []byte("line1\nline2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(path, 1024); err == nil {
		t.Fatal("multi-line secret was accepted")
	}
}

func TestExpectedProjectionColumnsNeverIncludeSensitiveFields(t *testing.T) {
	for _, config := range []runConfig{
		{SourceType: sourceagent.SourceSub2API, StreamID: "payments"},
		{SourceType: sourceagent.SourceSub2API, StreamID: "identities"},
		{SourceType: sourceagent.SourceNewAPI, StreamID: "payments"},
		{SourceType: sourceagent.SourceNewAPI, StreamID: "identities"},
		{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamPayments, ProtocolVersion: sourceagent.SchemaVersionV3},
		{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamPayments, ProtocolVersion: sourceagent.SchemaVersionV3},
		{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamUsage, ProtocolVersion: sourceagent.SchemaVersionV3},
		{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamCredits, ProtocolVersion: sourceagent.SchemaVersionV3},
		{SourceType: sourceagent.SourceSub2API, StreamID: sourceagent.StreamBalances, ProtocolVersion: sourceagent.SchemaVersionV3},
		{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamUsage, ProtocolVersion: sourceagent.SchemaVersionV3},
		{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamCredits, ProtocolVersion: sourceagent.SchemaVersionV3},
		{SourceType: sourceagent.SourceNewAPI, StreamID: sourceagent.StreamBalances, ProtocolVersion: sourceagent.SchemaVersionV3},
	} {
		fields := expectedProjectionColumns(config)
		if len(fields) == 0 {
			t.Fatalf("%s/%s has no projection contract", config.SourceType, config.StreamID)
		}
		for field := range fields {
			for _, forbidden := range []string{
				"password", "secret", "email", "trade_no", "provider_snapshot", "metadata",
			} {
				if strings.Contains(field, forbidden) {
					t.Fatalf("%s/%s projection exposes forbidden field %q", config.SourceType, config.StreamID, field)
				}
			}
			if strings.Contains(field, "token") && field != "public.invoice_newapi_oidc_provider_contract_v1.token_endpoint" {
				t.Fatalf("%s/%s projection exposes forbidden token field %q", config.SourceType, config.StreamID, field)
			}
			for _, forbidden := range []string{"public.custom_oauth_providers", "client_id", "client_secret", "scopes", "access_policy", "mapping"} {
				if strings.Contains(field, forbidden) {
					t.Fatalf("%s/%s projection exposes raw provider field %q", config.SourceType, config.StreamID, field)
				}
			}
		}
	}
}
