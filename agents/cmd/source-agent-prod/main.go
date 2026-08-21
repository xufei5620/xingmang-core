package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"invoice-system/agents/sourceagent"
)

var buildVersion = "0.3.0"

var productionSourceIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

type runConfig struct {
	SourceID               string
	StreamID               string
	SourceType             string
	SourceRuntime          string
	ProtocolVersion        string
	DatabaseDSNFile        string
	StateFile              string
	ReconcileFile          string
	SpoolFile              string
	SpoolKeyFile           string
	CutoverManifestFile    string
	CutoverKeyFile         string
	BalanceBaselineFile    string
	BalanceSnapshotFile    string
	BalanceSnapshotKeyFile string
	OIDCProviderKey        string
	OIDCIssuer             string
	IngestionOrigin        string
	IngestionHosts         []string
	IngestionPorts         []int
	IngestionCIDRs         []string
	MTLSCertificateFile    string
	MTLSPrivateKeyFile     string
	MTLSCAFile             string
	MTLSServerName         string
	SigningKeyFile         string
	SigningKeyID           string
	ScanLimit              int
	PollInterval           time.Duration
	ReconcileInterval      time.Duration
	FullScanInterval       time.Duration
	MaxBackoff             time.Duration
	MaxPages               int
	MaxConsecutiveFailures int
	ReconcileMissThreshold int
	HTTPTimeout            time.Duration
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC | log.Lmsgprefix)
	log.SetPrefix("source-agent-prod: ")
	if len(os.Args) != 2 {
		fatalUsage()
	}
	switch os.Args[1] {
	case "init-state":
		if err := initStateFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "init-reconcile":
		if err := initReconcileFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "cutover-init":
		if err := cutoverInitFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "check-cutover":
		if err := checkCutoverFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "run":
		if err := runFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "check-db":
		if err := checkDatabaseFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "check-state":
		if err := checkStateFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "healthcheck":
		if err := healthcheckFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "version":
		fmt.Println(buildVersion)
	default:
		fatalUsage()
	}
}

func fatalUsage() {
	fmt.Fprintln(os.Stderr, "usage: source-agent-prod init-state|init-reconcile|cutover-init|check-cutover|check-db|check-state|healthcheck|run|version")
	os.Exit(2)
}

func checkDatabaseFromEnvironment() error {
	config, err := loadRunConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := openReadOnlyPostgres(ctx, config)
	if err != nil {
		return err
	}
	defer database.Close()
	log.Printf("validated exact read-only projection source=%q stream=%q", config.SourceID, config.StreamID)
	return nil
}

func initStateFromEnvironment() error {
	state := &sourceagent.FileStateStore{
		Path:     strings.TrimSpace(os.Getenv("SOURCE_STATE_FILE")),
		SourceID: strings.TrimSpace(os.Getenv("SOURCE_ID")),
		StreamID: strings.TrimSpace(os.Getenv("SOURCE_STATE_STREAM")),
	}
	if !productionSourceIDPattern.MatchString(state.SourceID) || !validStream(state.StreamID) {
		return errors.New("init-state requires registered UUID SOURCE_ID and a registered SOURCE_STATE_STREAM")
	}
	if err := sourceagent.InitializeFileState(context.Background(), state); err != nil {
		return fmt.Errorf("initialize source state: %w", err)
	}
	log.Printf("initialized state source=%q stream=%q", state.SourceID, state.StreamID)
	return nil
}

func initReconcileFromEnvironment() error {
	if strings.TrimSpace(os.Getenv("SOURCE_SCHEMA_VERSION")) == sourceagent.SchemaVersionV3 {
		return errors.New("schema 3.0 streams do not use deletion reconciliation or tombstones")
	}
	threshold, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SOURCE_RECONCILE_MISS_THRESHOLD")))
	if err != nil {
		return errors.New("init-reconcile requires SOURCE_RECONCILE_MISS_THRESHOLD")
	}
	reconciler := &sourceagent.FileReconciler{
		Path:          strings.TrimSpace(os.Getenv("SOURCE_RECONCILE_FILE")),
		SourceID:      strings.TrimSpace(os.Getenv("SOURCE_ID")),
		StreamID:      strings.TrimSpace(os.Getenv("SOURCE_STATE_STREAM")),
		SourceType:    strings.TrimSpace(os.Getenv("SOURCE_TYPE")),
		MissThreshold: threshold,
	}
	if err = sourceagent.InitializeFileReconciler(context.Background(), reconciler); err != nil {
		return fmt.Errorf("initialize reconcile state: %w", err)
	}
	log.Printf("initialized reconcile state source=%q stream=%q threshold=%d", reconciler.SourceID, reconciler.StreamID, threshold)
	return nil
}

func cutoverInitFromEnvironment() error {
	config, manifestStore, snapshotStore, err := loadCutoverCommandConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, err := openReadOnlyPostgres(ctx, config)
	if err != nil {
		return err
	}
	defer database.Close()
	manifest, err := sourceagent.CaptureCutover(ctx, database, sourceagent.CutoverCaptureConfig{
		SourceID: config.SourceID, SourceType: config.SourceType, SourceRuntime: config.SourceRuntime,
		SigningKeyID: config.SigningKeyID, Manifest: manifestStore, Snapshot: snapshotStore})
	if err != nil {
		return err
	}
	log.Printf("captured immutable cutover source=%q at=%q manifest_hash=%q baseline_rows=%q", config.SourceID, manifest.CutoverAt, manifest.ManifestHash, manifest.BaselineRowCount)
	return nil
}

func checkCutoverFromEnvironment() error {
	config, manifestStore, snapshotStore, err := loadCutoverCommandConfig(os.Getenv)
	if err != nil {
		return err
	}
	manifest, snapshot, err := sourceagent.LoadAndCheckCutover(context.Background(), manifestStore, snapshotStore, config.SourceID, config.SourceType, config.SourceRuntime)
	if err != nil {
		return err
	}
	if manifest.SigningKeyID != config.SigningKeyID {
		return errors.New("cutover signing key id mismatch")
	}
	log.Printf("cutover valid source=%q at=%q manifest_hash=%q baseline_snapshot=%q rows=%d", config.SourceID, manifest.CutoverAt, manifest.ManifestHash, snapshot.SnapshotID, len(snapshot.Rows))
	return nil
}

func loadCutoverCommandConfig(getenv func(string) string) (runConfig, sourceagent.EncryptedStateFile, sourceagent.EncryptedStateFile, error) {
	config := runConfig{SourceID: strings.TrimSpace(getenv("SOURCE_ID")), SourceType: strings.TrimSpace(getenv("SOURCE_TYPE")), SourceRuntime: strings.TrimSpace(getenv("SOURCE_RUNTIME_VERSION")),
		ProtocolVersion: sourceagent.SchemaVersionV3, StreamID: sourceagent.StreamBalances, DatabaseDSNFile: strings.TrimSpace(getenv("SOURCE_DB_DSN_FILE")),
		CutoverManifestFile: strings.TrimSpace(getenv("SOURCE_CUTOVER_MANIFEST_FILE")), CutoverKeyFile: strings.TrimSpace(getenv("SOURCE_CUTOVER_KEY_FILE")),
		BalanceBaselineFile: strings.TrimSpace(getenv("SOURCE_BALANCE_BASELINE_FILE")), BalanceSnapshotKeyFile: strings.TrimSpace(getenv("SOURCE_BALANCE_SNAPSHOT_KEY_FILE")), SigningKeyID: strings.TrimSpace(getenv("SOURCE_SIGNING_KEY_ID"))}
	if !productionSourceIDPattern.MatchString(config.SourceID) || (config.SourceType != sourceagent.SourceSub2API && config.SourceType != sourceagent.SourceNewAPI) || config.SourceRuntime == "" || sourceagent.ValidateSigningKeyID(config.SigningKeyID) != nil {
		return runConfig{}, sourceagent.EncryptedStateFile{}, sourceagent.EncryptedStateFile{}, errors.New("cutover requires source UUID/type/runtime and signing key id")
	}
	for name, path := range map[string]string{"SOURCE_DB_DSN_FILE": config.DatabaseDSNFile, "SOURCE_CUTOVER_MANIFEST_FILE": config.CutoverManifestFile, "SOURCE_CUTOVER_KEY_FILE": config.CutoverKeyFile, "SOURCE_BALANCE_BASELINE_FILE": config.BalanceBaselineFile, "SOURCE_BALANCE_SNAPSHOT_KEY_FILE": config.BalanceSnapshotKeyFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return runConfig{}, sourceagent.EncryptedStateFile{}, sourceagent.EncryptedStateFile{}, fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	manifest := sourceagent.EncryptedStateFile{Path: config.CutoverManifestFile, Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: config.CutoverKeyFile}}
	snapshot := sourceagent.EncryptedStateFile{Path: config.BalanceBaselineFile, Purpose: "balance_baseline", Keys: sourceagent.FileSpoolKeyProvider{Path: config.BalanceSnapshotKeyFile}}
	return config, manifest, snapshot, nil
}

func checkStateFromEnvironment() error {
	sourceID := strings.TrimSpace(os.Getenv("SOURCE_ID"))
	streamID := strings.TrimSpace(os.Getenv("SOURCE_STATE_STREAM"))
	sourceType := strings.TrimSpace(os.Getenv("SOURCE_TYPE"))
	statePath := strings.TrimSpace(os.Getenv("SOURCE_STATE_FILE"))
	reconcilePath := strings.TrimSpace(os.Getenv("SOURCE_RECONCILE_FILE"))
	v3 := strings.TrimSpace(os.Getenv("SOURCE_SCHEMA_VERSION")) == sourceagent.SchemaVersionV3
	spoolPath := strings.TrimSpace(os.Getenv("SOURCE_SPOOL_FILE"))
	spoolKeyPath := strings.TrimSpace(os.Getenv("SOURCE_SPOOL_KEY_FILE"))
	threshold, err := envInt(os.Getenv, "SOURCE_RECONCILE_MISS_THRESHOLD", 3, 2, 10)
	if err != nil {
		return err
	}
	if !productionSourceIDPattern.MatchString(sourceID) || !validStream(streamID) ||
		(sourceType != sourceagent.SourceSub2API && sourceType != sourceagent.SourceNewAPI) {
		return errors.New("check-state requires source UUID, type and a registered stream")
	}
	statePaths := map[string]string{"SOURCE_STATE_FILE": statePath, "SOURCE_SPOOL_FILE": spoolPath, "SOURCE_SPOOL_KEY_FILE": spoolKeyPath}
	if !v3 {
		statePaths["SOURCE_RECONCILE_FILE"] = reconcilePath
	}
	for name, path := range statePaths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	if sameFilesystemPath(statePath, spoolPath) || sameFilesystemPath(spoolKeyPath, statePath) || sameFilesystemPath(spoolKeyPath, spoolPath) ||
		(!v3 && (sameFilesystemPath(statePath, reconcilePath) || sameFilesystemPath(reconcilePath, spoolPath) || sameFilesystemPath(spoolKeyPath, reconcilePath))) {
		return errors.New("check-state paths must be distinct")
	}
	directory := filepath.Dir(statePath)
	if filepath.Dir(spoolPath) != directory || (!v3 && filepath.Dir(reconcilePath) != directory) {
		return errors.New("state, reconcile and pending spool must share one dedicated stream directory")
	}
	locks, err := filepath.Glob(filepath.Join(directory, "*.lock"))
	if err != nil || len(locks) != 0 {
		return errors.New("source state contains a stale lock; restore is not clean")
	}
	state := &sourceagent.FileStateStore{Path: statePath, SourceID: sourceID, StreamID: streamID}
	cursor, sequence, err := state.CheckReadOnly()
	if err != nil {
		return fmt.Errorf("check durable cursor/sequence: %w", err)
	}
	reconcileCheck := sourceagent.ReconcileCheck{Phase: "not_applicable"}
	if !v3 {
		reconciler := &sourceagent.FileReconciler{Path: reconcilePath, SourceID: sourceID, StreamID: streamID, SourceType: sourceType, MissThreshold: threshold}
		reconcileCheck, err = reconciler.CheckReadOnly()
		if err != nil {
			return fmt.Errorf("check reconcile inventory: %w", err)
		}
	}
	spoolKeys := sourceagent.FileSpoolKeyProvider{Path: spoolKeyPath}
	keySnapshot, err := spoolKeys.Snapshot(context.Background())
	if err != nil {
		return fmt.Errorf("load pending spool key: %w", err)
	}
	keySnapshot.Destroy()
	pendingStore := &sourceagent.EncryptedFilePendingStore{Path: spoolPath, SourceID: sourceID,
		StreamID: streamID, Keys: spoolKeys}
	pending, exists, err := pendingStore.CheckReadOnly(context.Background())
	if err != nil {
		return fmt.Errorf("decrypt pending spool: %w", err)
	}
	if exists {
		defer pending.Destroy()
		if pending.Batch.Batch.SourceInstanceID != sourceID || pending.Batch.Batch.StreamID != streamID {
			return errors.New("pending spool source identity mismatch")
		}
		validSequence := sequence.Sequence+1 == pending.Batch.Batch.Sequence ||
			(sequence.Sequence == pending.Batch.Batch.Sequence && sequence.LastBatchHash == pending.Batch.BodyHash)
		cursorPosition := func(value sourceagent.ScanCursor) sourceagent.ScanCursor { value.Revision = 0; return value }
		validCursor := cursor == pending.CursorBefore || cursorPosition(cursor) == cursorPosition(pending.CursorAfter)
		if !validSequence || !validCursor {
			return errors.New("pending spool is inconsistent with durable cursor or sequence")
		}
	}
	if strings.TrimSpace(os.Getenv("SOURCE_SCHEMA_VERSION")) == sourceagent.SchemaVersionV3 {
		manifestStore := sourceagent.EncryptedStateFile{Path: strings.TrimSpace(os.Getenv("SOURCE_CUTOVER_MANIFEST_FILE")), Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: strings.TrimSpace(os.Getenv("SOURCE_CUTOVER_KEY_FILE"))}}
		manifest, loadErr := sourceagent.LoadCutoverManifest(context.Background(), manifestStore, sourceID, sourceType, strings.TrimSpace(os.Getenv("SOURCE_RUNTIME_VERSION")))
		if loadErr != nil {
			return fmt.Errorf("check cutover manifest: %w", loadErr)
		}
		if streamID == sourceagent.StreamBalances && manifest.SigningKeyID != strings.TrimSpace(os.Getenv("SOURCE_SIGNING_KEY_ID")) {
			return errors.New("cutover manifest signing key id mismatch")
		}
		if streamID == sourceagent.StreamBalances {
			baseline := sourceagent.EncryptedStateFile{Path: strings.TrimSpace(os.Getenv("SOURCE_BALANCE_BASELINE_FILE")), Purpose: "balance_baseline", Keys: sourceagent.FileSpoolKeyProvider{Path: strings.TrimSpace(os.Getenv("SOURCE_BALANCE_SNAPSHOT_KEY_FILE"))}}
			if _, _, loadErr = sourceagent.LoadAndCheckCutover(context.Background(), manifestStore, baseline, sourceID, sourceType, strings.TrimSpace(os.Getenv("SOURCE_RUNTIME_VERSION"))); loadErr != nil {
				return fmt.Errorf("check cutover baseline: %w", loadErr)
			}
		}
	}
	log.Printf("offline state valid source=%q stream=%q sequence=%d cursor_revision=%d reconcile_phase=%q inventory=%d seen=%d tombstones=%d pending=%t",
		sourceID, streamID, sequence.Sequence, cursor.Revision, reconcileCheck.Phase,
		reconcileCheck.InventoryEntries, reconcileCheck.SeenEntries, reconcileCheck.PlannedTombstones, exists)
	return nil
}

func healthcheckFromEnvironment() error {
	if err := checkStateFromEnvironment(); err != nil {
		return err
	}
	maximumAge, err := envDuration(os.Getenv, "SOURCE_LOCAL_HEALTH_MAX_AGE", 5*time.Minute)
	if err != nil || maximumAge < time.Minute || maximumAge > time.Hour {
		return errors.New("SOURCE_LOCAL_HEALTH_MAX_AGE must be between 1 minute and 1 hour")
	}
	info, err := os.Stat(strings.TrimSpace(os.Getenv("SOURCE_STATE_FILE")))
	if err != nil || time.Since(info.ModTime()) > maximumAge || info.ModTime().After(time.Now().Add(time.Minute)) {
		return errors.New("source state heartbeat is stale")
	}
	return nil
}

func runFromEnvironment() error {
	config, err := loadRunConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	state := &sourceagent.FileStateStore{Path: config.StateFile, SourceID: config.SourceID, StreamID: config.StreamID}
	cursors := sourceagent.FileCursorStore{State: state}
	sequences := sourceagent.FileSequenceStore{State: state}
	if _, err := cursors.Load(ctx, config.SourceID); err != nil {
		return fmt.Errorf("load initialized cursor state: %w", err)
	}
	if _, err := sequences.Load(ctx); err != nil {
		return fmt.Errorf("load initialized publish state: %w", err)
	}
	spoolKeys := sourceagent.FileSpoolKeyProvider{Path: config.SpoolKeyFile}
	spoolKey, err := spoolKeys.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("validate encrypted pending spool key: %w", err)
	}
	spoolKey.Destroy()
	signingKeys := sourceagent.FileSigningKeyProvider{KeyID: config.SigningKeyID, PrivateKeyFile: config.SigningKeyFile}
	signingKey, err := signingKeys.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("validate Ed25519 signing key: %w", err)
	}
	signingKey.Destroy()
	pending := &sourceagent.EncryptedFilePendingStore{
		Path: config.SpoolFile, SourceID: config.SourceID, StreamID: config.StreamID,
		Keys: spoolKeys,
	}
	if _, _, err := pending.Load(ctx); err != nil {
		return fmt.Errorf("load encrypted pending spool: %w", err)
	}

	ingestionHTTP, err := sourceagent.NewRestrictedHTTPClient(
		config.IngestionOrigin,
		sourceagent.OriginPolicy{
			AllowedHosts: config.IngestionHosts, AllowedPorts: config.IngestionPorts,
			AllowedCIDRs: config.IngestionCIDRs, AllowedMethods: []string{http.MethodPost},
		},
		sourceagent.TLSFiles{
			CertificateFile: config.MTLSCertificateFile,
			PrivateKeyFile:  config.MTLSPrivateKeyFile,
			CAFile:          config.MTLSCAFile, ServerName: config.MTLSServerName,
			ReloadClientCertificate: true,
		},
		config.HTTPTimeout,
	)
	if err != nil {
		return fmt.Errorf("configure restricted ingestion transport: %w", err)
	}
	client := &sourceagent.HTTPIngestClient{
		HTTP: ingestionHTTP, Keys: signingKeys,
	}
	database, err := openReadOnlyPostgres(ctx, config)
	if err != nil {
		return err
	}
	defer database.Close()
	connector, err := buildDBConnector(config, database)
	if err != nil {
		return err
	}
	publisher := &sourceagent.Publisher{
		Builder: sourceagent.BatchBuilder{
			SchemaVersion:    config.ProtocolVersion,
			SourceInstanceID: config.SourceID, StreamID: config.StreamID,
			SourceType: config.SourceType, SourceRuntimeVersion: config.SourceRuntime,
			AgentVersion: buildVersion, Mode: "db_projection",
		},
		Store: sequences, Client: client, Pending: pending,
	}
	var coordinator sourceagent.PageSyncer
	if config.ProtocolVersion == sourceagent.SchemaVersionV3 {
		coordinator = &sourceagent.SyncCoordinator{SourceID: config.SourceID, Connector: connector, Cursors: cursors, Publisher: publisher, Limit: config.ScanLimit}
	} else {
		coordinator = &sourceagent.DurableSyncCoordinator{SourceID: config.SourceID, Connector: connector, Cursors: cursors, Publisher: publisher,
			Reconcile: &sourceagent.FileReconciler{Path: config.ReconcileFile, SourceID: config.SourceID, StreamID: config.StreamID, SourceType: config.SourceType, MissThreshold: config.ReconcileMissThreshold}, Limit: config.ScanLimit}
	}
	runner := &sourceagent.SyncRunner{
		Coordinator: coordinator, SourceType: config.SourceType,
		PollInterval: config.PollInterval, ReconcileInterval: config.ReconcileInterval,
		FullScanInterval: config.FullScanInterval, MaxBackoff: config.MaxBackoff,
		MaxPagesPerCycle: config.MaxPages, MaxConsecutiveFailures: config.MaxConsecutiveFailures,
		OnCycle: func(result sourceagent.SyncCycleResult) {
			log.Printf("cycle complete source=%q stream=%q mode=%q scan_complete=%t pages=%d records=%d sequence=%d batch=%q warnings=%q",
				config.SourceID, config.StreamID, result.Mode, result.Complete, result.Pages, result.Records,
				result.LastSeq, result.LastBatch, result.Warnings)
		},
		OnFailure: func(failure sourceagent.SyncFailure) {
			if failure.Permanent {
				log.Printf("permanent sync failure source=%q stream=%q mode=%q error=%q",
					config.SourceID, config.StreamID, failure.Mode, failure.Err)
				return
			}
			log.Printf("transient sync failure source=%q stream=%q mode=%q retry_in=%q error=%q",
				config.SourceID, config.StreamID, failure.Mode, failure.RetryIn, failure.Err)
		},
	}
	log.Printf("starting source=%q stream=%q type=%q schema=%q agent_version=%q",
		config.SourceID, config.StreamID, config.SourceType, config.ProtocolVersion, buildVersion)
	if err := runner.Run(ctx); err != nil {
		return fmt.Errorf("source stream stopped fail-closed: %w", err)
	}
	log.Printf("shutdown complete source=%q stream=%q", config.SourceID, config.StreamID)
	return nil
}

func loadRunConfig(getenv func(string) string) (runConfig, error) {
	if getenv == nil {
		return runConfig{}, errors.New("environment reader is required")
	}
	for _, directSecret := range []string{"SOURCE_DB_DSN", "SOURCE_SIGNING_KEY", "SOURCE_MTLS_PRIVATE_KEY", "SOURCE_SPOOL_KEY", "SOURCE_CUTOVER_KEY", "SOURCE_BALANCE_SNAPSHOT_KEY"} {
		if strings.TrimSpace(getenv(directSecret)) != "" {
			return runConfig{}, fmt.Errorf("%s is prohibited; use the corresponding _FILE mount", directSecret)
		}
	}
	for _, ambientPG := range []string{
		"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE",
		"PGSERVICE", "PGSERVICEFILE", "PGSSLMODE", "PGSSLCERT", "PGSSLKEY",
		"PGSSLROOTCERT", "PGSSLPASSWORD", "PGOPTIONS", "PGAPPNAME",
	} {
		if strings.TrimSpace(getenv(ambientPG)) != "" {
			return runConfig{}, fmt.Errorf("%s is prohibited; source PostgreSQL configuration must be self-contained in SOURCE_DB_DSN_FILE", ambientPG)
		}
	}
	if strings.TrimSpace(getenv("SOURCE_MODE")) != "db_projection" {
		return runConfig{}, errors.New("production launcher requires SOURCE_MODE=db_projection")
	}
	if strings.TrimSpace(getenv("INGESTION_ALLOWED_METHODS")) != "POST" {
		return runConfig{}, errors.New("production launcher requires INGESTION_ALLOWED_METHODS=POST")
	}
	if strings.TrimSpace(getenv("SOURCE_MTLS_RELOAD_ON_HANDSHAKE")) != "true" {
		return runConfig{}, errors.New("production launcher requires SOURCE_MTLS_RELOAD_ON_HANDSHAKE=true")
	}
	config := runConfig{
		SourceID:               strings.TrimSpace(getenv("SOURCE_ID")),
		StreamID:               strings.TrimSpace(getenv("SOURCE_STATE_STREAM")),
		SourceType:             strings.TrimSpace(getenv("SOURCE_TYPE")),
		SourceRuntime:          strings.TrimSpace(getenv("SOURCE_RUNTIME_VERSION")),
		ProtocolVersion:        strings.TrimSpace(getenv("SOURCE_SCHEMA_VERSION")),
		DatabaseDSNFile:        strings.TrimSpace(getenv("SOURCE_DB_DSN_FILE")),
		StateFile:              strings.TrimSpace(getenv("SOURCE_STATE_FILE")),
		ReconcileFile:          strings.TrimSpace(getenv("SOURCE_RECONCILE_FILE")),
		SpoolFile:              strings.TrimSpace(getenv("SOURCE_SPOOL_FILE")),
		SpoolKeyFile:           strings.TrimSpace(getenv("SOURCE_SPOOL_KEY_FILE")),
		CutoverManifestFile:    strings.TrimSpace(getenv("SOURCE_CUTOVER_MANIFEST_FILE")),
		CutoverKeyFile:         strings.TrimSpace(getenv("SOURCE_CUTOVER_KEY_FILE")),
		BalanceBaselineFile:    strings.TrimSpace(getenv("SOURCE_BALANCE_BASELINE_FILE")),
		BalanceSnapshotFile:    strings.TrimSpace(getenv("SOURCE_BALANCE_SNAPSHOT_FILE")),
		BalanceSnapshotKeyFile: strings.TrimSpace(getenv("SOURCE_BALANCE_SNAPSHOT_KEY_FILE")),
		OIDCProviderKey:        strings.TrimSpace(getenv("SOURCE_TRUSTED_OIDC_PROVIDER_KEY")),
		OIDCIssuer:             strings.TrimSpace(getenv("SOURCE_TRUSTED_OIDC_ISSUER")),
		IngestionOrigin:        strings.TrimSpace(getenv("INGESTION_ORIGIN")),
		IngestionHosts:         splitList(getenv("INGESTION_ALLOWED_HOSTS")),
		IngestionCIDRs:         splitList(getenv("INGESTION_ALLOWED_CIDRS")),
		MTLSCertificateFile:    strings.TrimSpace(getenv("SOURCE_MTLS_CERT_FILE")),
		MTLSPrivateKeyFile:     strings.TrimSpace(getenv("SOURCE_MTLS_KEY_FILE")),
		MTLSCAFile:             strings.TrimSpace(getenv("SOURCE_MTLS_CA_FILE")),
		MTLSServerName:         strings.TrimSpace(getenv("SOURCE_MTLS_SERVER_NAME")),
		SigningKeyFile:         strings.TrimSpace(getenv("SOURCE_SIGNING_KEY_FILE")),
		SigningKeyID:           strings.TrimSpace(getenv("SOURCE_SIGNING_KEY_ID")),
	}
	var err error
	if config.ProtocolVersion == "" {
		config.ProtocolVersion = sourceagent.SchemaVersionV2
	}
	if config.IngestionPorts, err = parsePorts(getenv("INGESTION_ALLOWED_PORTS")); err != nil {
		return runConfig{}, err
	}
	if config.ScanLimit, err = envInt(getenv, "SOURCE_SCAN_LIMIT", 100, 1, 500); err != nil {
		return runConfig{}, err
	}
	if config.MaxPages, err = envInt(getenv, "SOURCE_MAX_PAGES_PER_CYCLE", 1000, 1, 10000); err != nil {
		return runConfig{}, err
	}
	if config.MaxConsecutiveFailures, err = envInt(getenv, "SOURCE_MAX_CONSECUTIVE_FAILURES", 10, 1, 1000); err != nil {
		return runConfig{}, err
	}
	if config.ReconcileMissThreshold, err = envInt(getenv, "SOURCE_RECONCILE_MISS_THRESHOLD", 3, 2, 10); err != nil {
		return runConfig{}, err
	}
	if config.PollInterval, err = envDuration(getenv, "SOURCE_POLL_INTERVAL", time.Minute); err != nil {
		return runConfig{}, err
	}
	if config.ReconcileInterval, err = envDuration(getenv, "SOURCE_RECONCILE_INTERVAL", 6*time.Hour); err != nil {
		return runConfig{}, err
	}
	if config.FullScanInterval, err = envDuration(getenv, "NEWAPI_FULL_SCAN_INTERVAL", time.Hour); err != nil {
		return runConfig{}, err
	}
	if config.MaxBackoff, err = envDuration(getenv, "SOURCE_MAX_BACKOFF", time.Minute); err != nil {
		return runConfig{}, err
	}
	if config.HTTPTimeout, err = envDuration(getenv, "INGESTION_HTTP_TIMEOUT", 20*time.Second); err != nil {
		return runConfig{}, err
	}
	if config.HTTPTimeout < time.Second || config.HTTPTimeout > 2*time.Minute {
		return runConfig{}, errors.New("INGESTION_HTTP_TIMEOUT must be between 1 second and 2 minutes")
	}
	if !productionSourceIDPattern.MatchString(config.SourceID) || config.SourceRuntime == "" || len(config.SourceRuntime) > 64 ||
		(config.SourceType != sourceagent.SourceSub2API && config.SourceType != sourceagent.SourceNewAPI) {
		return runConfig{}, errors.New("registered UUID SOURCE_ID, supported SOURCE_TYPE and SOURCE_RUNTIME_VERSION are required")
	}
	if !validStream(config.StreamID) {
		return runConfig{}, errors.New("SOURCE_STATE_STREAM is not registered")
	}
	if config.StreamID == sourceagent.StreamIdentities && config.ProtocolVersion != sourceagent.SchemaVersionV2 {
		return runConfig{}, errors.New("identities requires schema 2.0")
	}
	if (config.StreamID == sourceagent.StreamUsage || config.StreamID == sourceagent.StreamCredits || config.StreamID == sourceagent.StreamBalances) && config.ProtocolVersion != sourceagent.SchemaVersionV3 {
		return runConfig{}, errors.New("economic streams require schema 3.0")
	}
	if config.StreamID == sourceagent.StreamPayments && config.ProtocolVersion != sourceagent.SchemaVersionV2 && config.ProtocolVersion != sourceagent.SchemaVersionV3 {
		return runConfig{}, errors.New("payments requires schema 2.0 or 3.0")
	}
	if strings.TrimSpace(getenv("SOURCE_DB_DIALECT")) != "postgres" {
		return runConfig{}, errors.New("production launcher supports only SOURCE_DB_DIALECT=postgres")
	}
	paths := map[string]string{
		"SOURCE_DB_DSN_FILE":      config.DatabaseDSNFile,
		"SOURCE_STATE_FILE":       config.StateFile,
		"SOURCE_SPOOL_FILE":       config.SpoolFile,
		"SOURCE_SPOOL_KEY_FILE":   config.SpoolKeyFile,
		"SOURCE_MTLS_CERT_FILE":   config.MTLSCertificateFile,
		"SOURCE_MTLS_KEY_FILE":    config.MTLSPrivateKeyFile,
		"SOURCE_SIGNING_KEY_FILE": config.SigningKeyFile,
	}
	if config.ProtocolVersion == sourceagent.SchemaVersionV2 {
		paths["SOURCE_RECONCILE_FILE"] = config.ReconcileFile
	}
	for name, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return runConfig{}, fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	if config.ProtocolVersion == sourceagent.SchemaVersionV3 {
		for name, path := range map[string]string{"SOURCE_CUTOVER_MANIFEST_FILE": config.CutoverManifestFile, "SOURCE_CUTOVER_KEY_FILE": config.CutoverKeyFile} {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return runConfig{}, fmt.Errorf("%s must be an absolute clean path", name)
			}
		}
		if config.StreamID == sourceagent.StreamBalances {
			for name, path := range map[string]string{"SOURCE_BALANCE_BASELINE_FILE": config.BalanceBaselineFile, "SOURCE_BALANCE_SNAPSHOT_FILE": config.BalanceSnapshotFile, "SOURCE_BALANCE_SNAPSHOT_KEY_FILE": config.BalanceSnapshotKeyFile} {
				if !filepath.IsAbs(path) || filepath.Clean(path) != path {
					return runConfig{}, fmt.Errorf("%s must be an absolute clean path", name)
				}
			}
		}
	}
	if config.MTLSCAFile != "" && (!filepath.IsAbs(config.MTLSCAFile) || filepath.Clean(config.MTLSCAFile) != config.MTLSCAFile) {
		return runConfig{}, errors.New("SOURCE_MTLS_CA_FILE must be an absolute clean path")
	}
	mutablePaths := map[string]string{"SOURCE_STATE_FILE": config.StateFile, "SOURCE_SPOOL_FILE": config.SpoolFile}
	if config.ProtocolVersion == sourceagent.SchemaVersionV2 {
		mutablePaths["SOURCE_RECONCILE_FILE"] = config.ReconcileFile
	}
	secretPaths := map[string]string{
		"SOURCE_DB_DSN_FILE": config.DatabaseDSNFile, "SOURCE_SPOOL_KEY_FILE": config.SpoolKeyFile,
		"SOURCE_MTLS_CERT_FILE": config.MTLSCertificateFile, "SOURCE_MTLS_KEY_FILE": config.MTLSPrivateKeyFile,
		"SOURCE_SIGNING_KEY_FILE": config.SigningKeyFile,
	}
	if config.ProtocolVersion == sourceagent.SchemaVersionV3 {
		secretPaths["SOURCE_CUTOVER_KEY_FILE"] = config.CutoverKeyFile
	}
	if config.StreamID == sourceagent.StreamBalances {
		mutablePaths["SOURCE_BALANCE_SNAPSHOT_FILE"] = config.BalanceSnapshotFile
		secretPaths["SOURCE_BALANCE_SNAPSHOT_KEY_FILE"] = config.BalanceSnapshotKeyFile
		secretPaths["SOURCE_CUTOVER_MANIFEST_FILE"] = config.CutoverManifestFile
		secretPaths["SOURCE_BALANCE_BASELINE_FILE"] = config.BalanceBaselineFile
	}
	if config.MTLSCAFile != "" {
		secretPaths["SOURCE_MTLS_CA_FILE"] = config.MTLSCAFile
	}
	if sameFilesystemPath(config.StateFile, config.SpoolFile) {
		return runConfig{}, errors.New("SOURCE_STATE_FILE and SOURCE_SPOOL_FILE must be distinct")
	}
	if config.ProtocolVersion == sourceagent.SchemaVersionV2 && (sameFilesystemPath(config.ReconcileFile, config.StateFile) || sameFilesystemPath(config.ReconcileFile, config.SpoolFile)) {
		return runConfig{}, errors.New("SOURCE_RECONCILE_FILE must be distinct from state and spool files")
	}
	for mutableName, mutablePath := range mutablePaths {
		for secretName, secretPath := range secretPaths {
			if sameFilesystemPath(mutablePath, secretPath) {
				return runConfig{}, fmt.Errorf("%s must not alias %s", mutableName, secretName)
			}
		}
	}
	if config.IngestionOrigin == "" || len(config.IngestionHosts) == 0 || len(config.IngestionPorts) == 0 || len(config.IngestionCIDRs) == 0 || config.SigningKeyID == "" {
		return runConfig{}, errors.New("ingestion origin allowlist, mTLS and signing configuration are required")
	}
	if config.StreamID == "payments" && config.SourceType == sourceagent.SourceSub2API && config.ScanLimit > 166 {
		return runConfig{}, errors.New("Sub2API payments SOURCE_SCAN_LIMIT must not exceed 166 because one row can emit three records")
	}
	if config.StreamID == "identities" && (config.OIDCProviderKey == "" || len(config.OIDCProviderKey) > 255 || strings.ContainsAny(config.OIDCProviderKey, "\r\n\x00") || !validHTTPSIssuer(config.OIDCIssuer)) {
		return runConfig{}, errors.New("identity stream requires trusted OIDC provider key/slug and issuer")
	}
	if config.SourceType == sourceagent.SourceNewAPI && config.StreamID == "identities" && config.OIDCProviderKey != "solov-sso" {
		return runConfig{}, errors.New("New API identity projection is approved only for provider slug solov-sso")
	}
	policy := &sourceagent.SyncRunner{
		Coordinator: noOpPageSyncer{}, SourceType: config.SourceType,
		PollInterval: config.PollInterval, ReconcileInterval: config.ReconcileInterval,
		FullScanInterval: config.FullScanInterval, MaxBackoff: config.MaxBackoff,
		MaxPagesPerCycle: config.MaxPages, MaxConsecutiveFailures: config.MaxConsecutiveFailures,
	}
	if err := policy.Validate(); err != nil {
		return runConfig{}, err
	}
	return config, nil
}

type noOpPageSyncer struct{}

func (noOpPageSyncer) SyncPage(context.Context, sourceagent.ScanMode) (sourceagent.ScanPage, sourceagent.IngestAck, error) {
	return sourceagent.ScanPage{}, sourceagent.IngestAck{}, errors.New("not executable")
}

func openReadOnlyPostgres(ctx context.Context, config runConfig) (*sql.DB, error) {
	raw, err := readSecretFile(config.DatabaseDSNFile, 64<<10)
	if err != nil {
		return nil, errors.New("read source database DSN file failed")
	}
	dsn := string(raw)
	for index := range raw {
		raw[index] = 0
	}
	pgxConfig, err := pgx.ParseConfig(dsn)
	dsn = ""
	if err != nil {
		return nil, errors.New("parse source database DSN failed")
	}
	if pgxConfig.RuntimeParams == nil {
		pgxConfig.RuntimeParams = make(map[string]string)
	}
	pgxConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pgxConfig.RuntimeParams["statement_timeout"] = "15000"
	pgxConfig.RuntimeParams["lock_timeout"] = "5000"
	pgxConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	pgxConfig.RuntimeParams["application_name"] = "invoice-src-" + config.SourceID + "-" + config.StreamID
	pgxConfig.RuntimeParams["search_path"] = "public,pg_catalog"
	pgxConfig.RuntimeParams["timezone"] = "UTC"
	pgxConfig.ConnectTimeout = 10 * time.Second
	database := stdlib.OpenDB(*pgxConfig)
	database.SetMaxOpenConns(2)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(5 * time.Minute)
	connectContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := database.PingContext(connectContext); err != nil {
		database.Close()
		return nil, errors.New("connect to source projection database failed")
	}
	var readOnly string
	if err := database.QueryRowContext(connectContext, "SHOW transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		database.Close()
		return nil, errors.New("source database connection did not prove transaction_read_only=on")
	}
	if err := verifyProjectionPrivileges(connectContext, database, config); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func verifyProjectionPrivileges(ctx context.Context, database *sql.DB, config runConfig) error {
	expected := expectedProjectionColumns(config)
	if len(expected) == 0 {
		return errors.New("no projection privilege contract for source stream")
	}
	rows, err := database.QueryContext(ctx, `
		SELECT table_schema,table_name,column_name
		FROM information_schema.columns
		WHERE table_schema <> 'information_schema'
		  AND table_schema NOT LIKE 'pg\_%' ESCAPE '\'
		  AND has_column_privilege(current_user,format('%I.%I',table_schema,table_name),column_name,'SELECT')
		ORDER BY table_schema,table_name,column_name`)
	if err != nil {
		return fmt.Errorf("inspect source projection column privileges failed: %w", err)
	}
	defer rows.Close()
	actual := map[string]struct{}{}
	for rows.Next() {
		var schema, table, column string
		if err = rows.Scan(&schema, &table, &column); err != nil {
			return errors.New("scan source projection privileges failed")
		}
		actual[schema+"."+table+"."+column] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return errors.New("iterate source projection privileges failed")
	}
	if len(actual) != len(expected) {
		return errors.New("source database SELECT grants differ from the exact projection contract")
	}
	for field := range expected {
		if _, ok := actual[field]; !ok {
			return errors.New("source database is missing a required projection column grant")
		}
	}
	var superuser, createDB, createRole, replication, bypassRLS, canLogin, inherit, canConnect, canTemporary bool
	var connectionLimit int
	var createSchema, usePublicSchema, unexpectedSchemaUsage, memberOfRole bool
	if err = database.QueryRowContext(ctx, `
		SELECT r.rolsuper,r.rolcreatedb,r.rolcreaterole,r.rolreplication,r.rolbypassrls,
		       r.rolcanlogin,r.rolinherit,r.rolconnlimit,
		       has_database_privilege(current_user,current_database(),'CONNECT'),
		       has_database_privilege(current_user,current_database(),'TEMP'),
		       EXISTS (
		         SELECT 1 FROM pg_namespace n
		         WHERE n.nspname <> 'information_schema'
		           AND n.nspname NOT LIKE 'pg\_%' ESCAPE '\'
		           AND has_schema_privilege(current_user,n.oid,'CREATE')
		       ),
		       has_schema_privilege(current_user,'public','USAGE'),
		       EXISTS (
		         SELECT 1 FROM pg_namespace n
		         WHERE n.nspname <> 'public'
		           AND n.nspname <> 'information_schema'
		           AND n.nspname NOT LIKE 'pg\_%' ESCAPE '\'
		           AND has_schema_privilege(current_user,n.oid,'USAGE')
		       ),
		       EXISTS (
		         SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid
		       )
		FROM pg_roles r WHERE r.rolname=current_user`).Scan(
		&superuser, &createDB, &createRole, &replication, &bypassRLS,
		&canLogin, &inherit, &connectionLimit, &canConnect, &canTemporary,
		&createSchema, &usePublicSchema, &unexpectedSchemaUsage, &memberOfRole); err != nil {
		return errors.New("inspect source role attributes failed")
	}
	if superuser || createDB || createRole || replication || bypassRLS || !canLogin || inherit || !canConnect ||
		connectionLimit != 2 || canTemporary || createSchema || !usePublicSchema ||
		unexpectedSchemaUsage || memberOfRole {
		return errors.New("source database role is over-privileged")
	}
	tables := map[string]struct{}{}
	for field := range expected {
		parts := strings.Split(field, ".")
		if len(parts) != 3 {
			return errors.New("invalid projection privilege contract")
		}
		tables[parts[0]+"."+parts[1]] = struct{}{}
	}
	for table := range tables {
		var tableInsert, tableUpdate, tableDelete, tableTruncate, tableReferences, tableTrigger bool
		var columnInsert, columnUpdate, columnReferences bool
		if err = database.QueryRowContext(ctx, `
			SELECT has_table_privilege(current_user,$1,'INSERT'),
			       has_table_privilege(current_user,$1,'UPDATE'),
			       has_table_privilege(current_user,$1,'DELETE'),
			       has_table_privilege(current_user,$1,'TRUNCATE'),
			       has_table_privilege(current_user,$1,'REFERENCES'),
			       has_table_privilege(current_user,$1,'TRIGGER'),
			       has_any_column_privilege(current_user,$1,'INSERT'),
			       has_any_column_privilege(current_user,$1,'UPDATE'),
			       has_any_column_privilege(current_user,$1,'REFERENCES')`, table).Scan(
			&tableInsert, &tableUpdate, &tableDelete, &tableTruncate, &tableReferences, &tableTrigger,
			&columnInsert, &columnUpdate, &columnReferences); err != nil {
			return fmt.Errorf("inspect source mutation privileges failed: %w", err)
		}
		if tableInsert || tableUpdate || tableDelete || tableTruncate || tableReferences || tableTrigger || columnInsert || columnUpdate || columnReferences {
			return errors.New("source database role has mutation privileges")
		}
	}
	var unexpectedMutation, sequencePrivilege bool
	if err = database.QueryRowContext(ctx, `
		SELECT EXISTS (
		         SELECT 1
		         FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		         WHERE c.relkind IN ('r','p','v','m','f')
		           AND n.nspname <> 'information_schema'
		           AND n.nspname NOT LIKE 'pg\_%' ESCAPE '\'
		           AND (
		             has_table_privilege(current_user,c.oid,'INSERT') OR
		             has_table_privilege(current_user,c.oid,'UPDATE') OR
		             has_table_privilege(current_user,c.oid,'DELETE') OR
		             has_table_privilege(current_user,c.oid,'TRUNCATE') OR
		             has_table_privilege(current_user,c.oid,'REFERENCES') OR
		             has_table_privilege(current_user,c.oid,'TRIGGER')
		           )
		       ),
		       EXISTS (
		         SELECT 1
		         FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		         WHERE c.relkind='S'
		           AND n.nspname <> 'information_schema'
		           AND n.nspname NOT LIKE 'pg\_%' ESCAPE '\'
		           AND (
		             has_sequence_privilege(current_user,c.oid,'USAGE') OR
		             has_sequence_privilege(current_user,c.oid,'SELECT') OR
		             has_sequence_privilege(current_user,c.oid,'UPDATE')
		           )
		       )`).Scan(&unexpectedMutation, &sequencePrivilege); err != nil {
		return errors.New("inspect source non-projection privileges failed")
	}
	if unexpectedMutation || sequencePrivilege {
		return errors.New("source database role has non-projection privileges")
	}
	return nil
}

func expectedProjectionColumns(config runConfig) map[string]struct{} {
	fields := []string{}
	switch {
	case config.ProtocolVersion == sourceagent.SchemaVersionV3 && config.SourceType == sourceagent.SourceSub2API && config.StreamID == sourceagent.StreamPayments:
		fields = projectionFields("invoice_sub2api_payments_projection_v3", []string{"source_id", "user_id", "event_time", "causal_domain", "source_cursor", "entity_kind", "status", "order_type", "amount", "pay_amount", "currency", "refund_amount", "gateway_refund_amount", "completed_at", "refund_at", "created_at", "updated_at", "payment_type", "provider_key", "wallet_cash_service_units", "paid_minor", "verification_state", "refunded"})
		fields = append(fields, projectionFields("invoice_sub2api_payments_projection_health_v3", []string{"contract_ok", "blocked_reason", "configuration_hash"})...)
	case config.ProtocolVersion == sourceagent.SchemaVersionV3 && config.SourceType == sourceagent.SourceNewAPI && config.StreamID == sourceagent.StreamPayments:
		fields = projectionFields("invoice_newapi_payments_projection_v3", []string{"source_id", "user_id", "event_time", "causal_domain", "source_cursor", "entity_kind", "status", "order_type", "amount", "pay_amount", "currency", "refund_amount", "gateway_refund_amount", "completed_at", "refund_at", "created_at", "updated_at", "payment_type", "provider_key", "wallet_cash_service_units", "paid_minor", "verification_state", "refunded"})
		fields = append(fields, projectionFields("invoice_newapi_payments_projection_health_v3", []string{"contract_ok", "blocked_reason", "configuration_hash"})...)
	case config.ProtocolVersion == sourceagent.SchemaVersionV3 && (config.StreamID == sourceagent.StreamUsage || config.StreamID == sourceagent.StreamCredits):
		base := "invoice_" + config.SourceType + "_" + config.StreamID + "_projection_v3"
		columns := []string{"source_id", "user_id", "event_time", "service_units", "causal_domain", "source_cursor"}
		if config.StreamID == sourceagent.StreamCredits {
			columns = append(columns, "credit_kind")
		}
		fields = projectionFields(base, columns)
		fields = append(fields, projectionFields("invoice_"+config.SourceType+"_"+config.StreamID+"_projection_health_v3", []string{"contract_ok", "blocked_reason", "total_rows", "gap_count", "configuration_hash"})...)
	case config.ProtocolVersion == sourceagent.SchemaVersionV3 && config.StreamID == sourceagent.StreamBalances:
		fields = projectionFields("invoice_"+config.SourceType+"_balance_projection_v3", []string{"user_id", "balance_service_units", "balance_negative"})
		fields = append(fields, projectionFields("invoice_"+config.SourceType+"_cutover_contract_v3", []string{"projection_contract", "contract_ok", "configuration_hash", "payments_event_at", "payments_cursor", "usage_event_at", "usage_cursor", "credits_event_at", "credits_cursor"})...)
	case config.SourceType == sourceagent.SourceSub2API && config.StreamID == "payments":
		fields = []string{"public.invoice_sub2api_payment_projection_v1.id", "public.invoice_sub2api_payment_projection_v1.user_id", "public.invoice_sub2api_payment_projection_v1.status", "public.invoice_sub2api_payment_projection_v1.order_type", "public.invoice_sub2api_payment_projection_v1.amount", "public.invoice_sub2api_payment_projection_v1.pay_amount", "public.invoice_sub2api_payment_projection_v1.refund_amount", "public.invoice_sub2api_payment_projection_v1.currency", "public.invoice_sub2api_payment_projection_v1.completed_at", "public.invoice_sub2api_payment_projection_v1.refund_at", "public.invoice_sub2api_payment_projection_v1.created_at", "public.invoice_sub2api_payment_projection_v1.updated_at", "public.invoice_sub2api_payment_projection_v1.payment_type", "public.invoice_sub2api_payment_projection_v1.provider_key", "public.invoice_sub2api_payment_projection_health_v1.total_rows", "public.invoice_sub2api_payment_projection_health_v1.exposed_cny_rows", "public.invoice_sub2api_payment_projection_health_v1.unsupported_known_non_cny_rows", "public.invoice_sub2api_payment_projection_health_v1.blocked_unknown_currency_rows"}
	case config.SourceType == sourceagent.SourceSub2API && config.StreamID == "identities":
		fields = []string{"public.invoice_sub2api_oidc_binding_projection_v1.id", "public.invoice_sub2api_oidc_binding_projection_v1.user_id", "public.invoice_sub2api_oidc_binding_projection_v1.provider_type", "public.invoice_sub2api_oidc_binding_projection_v1.provider_key", "public.invoice_sub2api_oidc_binding_projection_v1.provider_subject", "public.invoice_sub2api_oidc_binding_projection_v1.verified_at", "public.invoice_sub2api_oidc_binding_projection_v1.issuer", "public.invoice_sub2api_oidc_binding_projection_v1.created_at", "public.invoice_sub2api_oidc_binding_projection_v1.updated_at"}
	case config.SourceType == sourceagent.SourceNewAPI && config.StreamID == "payments":
		fields = []string{"public.top_ups.id", "public.top_ups.user_id", "public.top_ups.amount", "public.top_ups.money", "public.top_ups.payment_method", "public.top_ups.payment_provider", "public.top_ups.create_time", "public.top_ups.complete_time", "public.top_ups.status"}
	case config.SourceType == sourceagent.SourceNewAPI && config.StreamID == "identities":
		fields = []string{"public.invoice_newapi_oidc_provider_contract_v1.id", "public.invoice_newapi_oidc_provider_contract_v1.slug", "public.invoice_newapi_oidc_provider_contract_v1.enabled", "public.invoice_newapi_oidc_provider_contract_v1.well_known", "public.invoice_newapi_oidc_provider_contract_v1.authorization_endpoint", "public.invoice_newapi_oidc_provider_contract_v1.token_endpoint", "public.invoice_newapi_oidc_provider_contract_v1.user_info_endpoint", "public.invoice_newapi_oidc_provider_contract_v1.contract_ok", "public.invoice_newapi_oidc_binding_projection_v1.id", "public.invoice_newapi_oidc_binding_projection_v1.user_id", "public.invoice_newapi_oidc_binding_projection_v1.provider_id", "public.invoice_newapi_oidc_binding_projection_v1.provider_user_id", "public.invoice_newapi_oidc_binding_projection_v1.created_at", "public.invoice_newapi_oidc_binding_projection_v1.provider_slug"}
	}
	result := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		result[field] = struct{}{}
	}
	return result
}

func projectionFields(table string, columns []string) []string {
	result := make([]string, 0, len(columns))
	for _, column := range columns {
		result = append(result, "public."+table+"."+column)
	}
	return result
}

func buildDBConnector(config runConfig, database *sql.DB) (sourceagent.Connector, error) {
	if config.ProtocolVersion == sourceagent.SchemaVersionV3 {
		manifestStore := sourceagent.EncryptedStateFile{Path: config.CutoverManifestFile, Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: config.CutoverKeyFile}}
		manifest, err := sourceagent.LoadCutoverManifest(context.Background(), manifestStore, config.SourceID, config.SourceType, config.SourceRuntime)
		if err != nil {
			return nil, fmt.Errorf("load encrypted cutover manifest: %w", err)
		}
		if config.StreamID == sourceagent.StreamBalances && manifest.SigningKeyID != config.SigningKeyID {
			return nil, errors.New("cutover manifest signing key id differs from this stream")
		}
		switch config.StreamID {
		case sourceagent.StreamPayments:
			return &sourceagent.PaymentV3DBConnector{DB: database, Source: config.SourceType, Manifest: manifest}, nil
		case sourceagent.StreamUsage, sourceagent.StreamCredits:
			return &sourceagent.EconomicDBConnector{DB: database, Source: config.SourceType, Stream: config.StreamID, Manifest: manifest}, nil
		case sourceagent.StreamBalances:
			return &sourceagent.BalanceDBConnector{DB: database, Source: config.SourceType, SourceID: config.SourceID, Manifest: manifest,
				Baseline: sourceagent.EncryptedStateFile{Path: config.BalanceBaselineFile, Purpose: "balance_baseline", Keys: sourceagent.FileSpoolKeyProvider{Path: config.BalanceSnapshotKeyFile}},
				Current:  sourceagent.EncryptedStateFile{Path: config.BalanceSnapshotFile, Purpose: "balance_snapshot", Keys: sourceagent.FileSpoolKeyProvider{Path: config.BalanceSnapshotKeyFile}}}, nil
		}
		return nil, errors.New("unsupported schema 3.0 stream")
	}
	if config.StreamID == "payments" {
		if config.SourceType == sourceagent.SourceSub2API {
			return &sourceagent.Sub2APIDBConnector{DB: database, Dialect: sourceagent.DialectPostgres}, nil
		}
		return &sourceagent.NewAPIDBConnector{DB: database, Dialect: sourceagent.DialectPostgres}, nil
	}
	if config.SourceType == sourceagent.SourceSub2API {
		return &sourceagent.Sub2APIIdentityDBConnector{
			DB: database, Dialect: sourceagent.DialectPostgres,
			TrustedProviderKey: config.OIDCProviderKey, TrustedIssuer: config.OIDCIssuer,
		}, nil
	}
	return &sourceagent.NewAPIIdentityDBConnector{
		DB: database, Dialect: sourceagent.DialectPostgres,
		TrustedProviderSlug: config.OIDCProviderKey, TrustedIssuer: config.OIDCIssuer,
	}, nil
}

func readSecretFile(path string, maximum int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("secret path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("secret path is missing or unsafe")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("secret file permissions are broader than 0600")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("read secret file failed")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || int64(len(trimmed)) > maximum || bytes.ContainsAny(trimmed, "\r\n") {
		for index := range raw {
			raw[index] = 0
		}
		return nil, errors.New("secret file content is invalid")
	}
	result := append([]byte(nil), trimmed...)
	for index := range raw {
		raw[index] = 0
	}
	return result, nil
}

func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(field); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func sameFilesystemPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func validHTTPSIssuer(raw string) bool {
	if len(raw) == 0 || len(raw) > 2048 || strings.HasSuffix(raw, "/") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func parsePorts(raw string) ([]int, error) {
	values := splitList(raw)
	ports := make([]int, 0, len(values))
	for _, value := range values {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("INGESTION_ALLOWED_PORTS contains an invalid port")
		}
		ports = append(ports, port)
	}
	return ports, nil
}

func envDuration(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return value, nil
}

func envInt(getenv func(string) string, name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func validStream(value string) bool {
	return value == sourceagent.StreamPayments || value == sourceagent.StreamIdentities || value == sourceagent.StreamUsage || value == sourceagent.StreamCredits || value == sourceagent.StreamBalances
}
