package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
var requiredEligibilityStartAt = time.Date(2026, time.August, 31, 16, 0, 0, 0, time.UTC)

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
	EligibilityStartAt     time.Time
	ScanLimit              int
	PollInterval           time.Duration
	EconomicSafetyDelay    time.Duration
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
		if err := checkDatabaseFromEnvironment(true); err != nil {
			log.Fatal(err)
		}
	case "check-db-static":
		if err := checkDatabaseFromEnvironment(false); err != nil {
			log.Fatal(err)
		}
	case "check-state":
		if err := checkStateFromEnvironment(); err != nil {
			log.Fatal(err)
		}
	case "inspect-pending":
		if err := inspectPendingFromEnvironment(os.Getenv, os.Stdout); err != nil {
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
	fmt.Fprintln(os.Stderr, "usage: source-agent-prod init-state|init-reconcile|cutover-init|check-cutover|check-db-static|check-db|check-state|inspect-pending|healthcheck|run|version")
	os.Exit(2)
}

func loadEligibilityStart(getenv func(string) string) (time.Time, error) {
	if getenv == nil {
		return time.Time{}, errors.New("environment reader is required")
	}
	raw := strings.TrimSpace(getenv("ELIGIBILITY_START_AT"))
	parsed, err := time.Parse(time.RFC3339, raw)
	if raw == "" || err != nil || !parsed.UTC().Equal(requiredEligibilityStartAt) {
		return time.Time{}, errors.New("ELIGIBILITY_START_AT must equal 2026-09-01T00:00:00+08:00")
	}
	return requiredEligibilityStartAt, nil
}

func checkDatabaseFromEnvironment(requireLiveEconomicContract bool) error {
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
	if requireLiveEconomicContract && config.ProtocolVersion == sourceagent.SchemaVersionV3 {
		manifestStore := sourceagent.EncryptedStateFile{Path: config.CutoverManifestFile, Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: config.CutoverKeyFile}}
		manifest, loadErr := sourceagent.LoadCutoverManifest(ctx, manifestStore, config.SourceID, config.SourceType, config.SourceRuntime)
		if loadErr != nil {
			return fmt.Errorf("load encrypted cutover manifest for live database check: %w", loadErr)
		}
		if loadErr = sourceagent.ValidateCutoverEligibility(manifest, config.EligibilityStartAt); loadErr != nil {
			return loadErr
		}
		if config.StreamID == sourceagent.StreamBalances && manifest.SigningKeyID != config.SigningKeyID {
			return errors.New("cutover manifest signing key id differs from this stream")
		}
		if loadErr = sourceagent.CheckLiveEconomicContract(ctx, database, config.SourceType, config.StreamID, manifest); loadErr != nil {
			return fmt.Errorf("live source economic contract check failed: %w", loadErr)
		}
	}
	checkMode := "static"
	if requireLiveEconomicContract {
		checkMode = "full"
	}
	log.Printf("validated dependency-free read-only bridge source=%q stream=%q check_mode=%q", config.SourceID, config.StreamID, checkMode)
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
		SigningKeyID: config.SigningKeyID, EligibilityStartAt: config.EligibilityStartAt,
		Manifest: manifestStore, Snapshot: snapshotStore})
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
	if err = sourceagent.ValidateCutoverEligibility(manifest, config.EligibilityStartAt); err != nil {
		return err
	}
	log.Printf("cutover valid source=%q at=%q manifest_hash=%q baseline_snapshot=%q rows=%d", config.SourceID, manifest.CutoverAt, manifest.ManifestHash, snapshot.SnapshotID, len(snapshot.Rows))
	return nil
}

func loadCutoverCommandConfig(getenv func(string) string) (runConfig, sourceagent.EncryptedStateFile, sourceagent.EncryptedStateFile, error) {
	config := runConfig{SourceID: strings.TrimSpace(getenv("SOURCE_ID")), SourceType: strings.TrimSpace(getenv("SOURCE_TYPE")), SourceRuntime: strings.TrimSpace(getenv("SOURCE_RUNTIME_VERSION")),
		ProtocolVersion: sourceagent.SchemaVersionV3, StreamID: sourceagent.StreamBalances, DatabaseDSNFile: strings.TrimSpace(getenv("SOURCE_DB_DSN_FILE")),
		CutoverManifestFile: strings.TrimSpace(getenv("SOURCE_CUTOVER_MANIFEST_FILE")), CutoverKeyFile: strings.TrimSpace(getenv("SOURCE_CUTOVER_KEY_FILE")),
		BalanceBaselineFile: strings.TrimSpace(getenv("SOURCE_BALANCE_BASELINE_FILE")), BalanceSnapshotKeyFile: strings.TrimSpace(getenv("SOURCE_BALANCE_SNAPSHOT_KEY_FILE")), SigningKeyID: strings.TrimSpace(getenv("SOURCE_SIGNING_KEY_ID"))}
	eligibilityStart, err := loadEligibilityStart(getenv)
	if err != nil {
		return runConfig{}, sourceagent.EncryptedStateFile{}, sourceagent.EncryptedStateFile{}, err
	}
	config.EligibilityStartAt = eligibilityStart
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

const pendingInspectionSchemaVersion = 1

type pendingInspectionCursor struct {
	Revision uint64 `json:"revision"`
	SHA256   string `json:"sha256"`
}

type pendingInspectionBatch struct {
	SchemaVersion     string  `json:"schema_version"`
	BatchID           string  `json:"batch_id"`
	Sequence          uint64  `json:"sequence"`
	PreviousBatchHash *string `json:"previous_batch_hash"`
	BodyHash          string  `json:"body_hash"`
	RecordCount       int     `json:"record_count"`
	ScanCycleID       string  `json:"scan_cycle_id"`
	ScanComplete      bool    `json:"scan_complete"`
}

type pendingInspectionDurableState struct {
	CursorRevision  uint64 `json:"cursor_revision"`
	CursorSHA256    string `json:"cursor_sha256"`
	PublishRevision uint64 `json:"publish_revision"`
	Sequence        uint64 `json:"sequence"`
	LastBatchHash   string `json:"last_batch_hash"`
}

type pendingInspectionConsistency struct {
	Lifecycle               string `json:"lifecycle"`
	SourceStream            bool   `json:"source_stream"`
	SchemaRuntime           bool   `json:"schema_runtime"`
	BodyHash                bool   `json:"body_hash"`
	HashChain               bool   `json:"hash_chain"`
	Sequence                bool   `json:"sequence"`
	PublishRevision         bool   `json:"publish_revision"`
	Cursor                  bool   `json:"cursor"`
	CursorRevision          bool   `json:"cursor_revision"`
	BatchCursorScanMetadata bool   `json:"batch_cursor_scan_metadata"`
}

type pendingInspectionReport struct {
	InspectionSchemaVersion int                           `json:"inspection_schema_version"`
	Source                  string                        `json:"source"`
	SourceType              string                        `json:"source_type"`
	Stream                  string                        `json:"stream"`
	Pending                 bool                          `json:"pending"`
	CursorHashAlgorithm     string                        `json:"cursor_hash_algorithm"`
	Batch                   *pendingInspectionBatch       `json:"batch,omitempty"`
	CursorBefore            *pendingInspectionCursor      `json:"cursor_before,omitempty"`
	CursorAfter             *pendingInspectionCursor      `json:"cursor_after,omitempty"`
	DurableState            pendingInspectionDurableState `json:"durable_state"`
	Consistency             *pendingInspectionConsistency `json:"consistency,omitempty"`
}

// inspectPendingFromEnvironment emits only a versioned, non-sensitive JSON
// projection. It deliberately uses the production configuration loader and the
// authenticated encrypted-spool reader, but never opens the source database,
// initializes an outbound transport, acquires a state lock or writes a file.
func inspectPendingFromEnvironment(getenv func(string) string, output io.Writer) error {
	if output == nil {
		return errors.New("inspect-pending output is required")
	}
	config, err := loadRunConfig(getenv)
	if err != nil {
		return err
	}
	stateDirectory := filepath.Dir(config.StateFile)
	if filepath.Dir(config.SpoolFile) != stateDirectory {
		return errors.New("inspect-pending requires state and pending spool in one dedicated stream directory")
	}
	locks, err := filepath.Glob(filepath.Join(stateDirectory, "*.lock"))
	if err != nil || len(locks) != 0 {
		return errors.New("inspect-pending requires a stopped agent and a lock-free state directory")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	spoolKeys := sourceagent.FileSpoolKeyProvider{Path: config.SpoolKeyFile}
	keySnapshot, err := spoolKeys.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("validate pending spool key: %w", err)
	}
	keySnapshot.Destroy()

	stateStore := &sourceagent.FileStateStore{Path: config.StateFile, SourceID: config.SourceID, StreamID: config.StreamID}
	durableCursor, durableSequence, err := stateStore.CheckReadOnly()
	if err != nil {
		return fmt.Errorf("inspect durable cursor/sequence: %w", err)
	}
	if durableSequence.Revision != durableSequence.Sequence {
		return errors.New("durable publish revision and sequence are inconsistent")
	}
	durableCursorMetadata, err := pendingCursorMetadata(durableCursor)
	if err != nil {
		return err
	}
	report := pendingInspectionReport{
		InspectionSchemaVersion: pendingInspectionSchemaVersion,
		Source:                  config.SourceID, SourceType: config.SourceType, Stream: config.StreamID,
		CursorHashAlgorithm: "sha256-go-json-scan-cursor-v1",
		DurableState: pendingInspectionDurableState{
			CursorRevision: durableCursorMetadata.Revision, CursorSHA256: durableCursorMetadata.SHA256,
			PublishRevision: durableSequence.Revision, Sequence: durableSequence.Sequence,
			LastBatchHash: durableSequence.LastBatchHash,
		},
	}

	pendingStore := &sourceagent.EncryptedFilePendingStore{
		Path: config.SpoolFile, SourceID: config.SourceID, StreamID: config.StreamID, Keys: spoolKeys,
	}
	pending, exists, err := pendingStore.CheckReadOnly(ctx)
	if err != nil {
		return fmt.Errorf("authenticate and validate encrypted pending spool: %w", err)
	}
	if !exists {
		return writePendingInspection(output, report)
	}
	defer pending.Destroy()

	batch := pending.Batch.Batch
	if batch.SourceInstanceID != config.SourceID || batch.StreamID != config.StreamID ||
		batch.SourceType != config.SourceType || batch.SourceRuntimeVersion != config.SourceRuntime ||
		batch.SchemaVersion != config.ProtocolVersion || batch.Mode != "db_projection" {
		return errors.New("pending spool metadata does not match the production stream configuration")
	}
	if !pendingBatchCursorMetadataConsistent(batch, pending.CursorAfter) {
		return errors.New("pending batch scan metadata does not match its target cursor")
	}
	lifecycle, err := pendingLifecycle(durableCursor, durableSequence, pending)
	if err != nil {
		return err
	}
	beforeMetadata, err := pendingCursorMetadata(pending.CursorBefore)
	if err != nil {
		return err
	}
	afterMetadata, err := pendingCursorMetadata(pending.CursorAfter)
	if err != nil {
		return err
	}
	var previousBatchHash *string
	if batch.PreviousBatchHash != nil {
		value := *batch.PreviousBatchHash
		previousBatchHash = &value
	}
	report.Pending = true
	report.Batch = &pendingInspectionBatch{
		SchemaVersion: batch.SchemaVersion, BatchID: batch.BatchID, Sequence: batch.Sequence,
		PreviousBatchHash: previousBatchHash, BodyHash: pending.Batch.BodyHash,
		RecordCount: len(batch.Records), ScanCycleID: batch.ScanCycleID, ScanComplete: batch.ScanComplete,
	}
	report.CursorBefore = &beforeMetadata
	report.CursorAfter = &afterMetadata
	report.Consistency = &pendingInspectionConsistency{
		Lifecycle: lifecycle, SourceStream: true, SchemaRuntime: true, BodyHash: true,
		HashChain: true, Sequence: true, PublishRevision: true, Cursor: true,
		CursorRevision: true, BatchCursorScanMetadata: true,
	}
	return writePendingInspection(output, report)
}

func pendingCursorMetadata(cursor sourceagent.ScanCursor) (pendingInspectionCursor, error) {
	revision := cursor.Revision
	cursor.Revision = 0
	raw, err := json.Marshal(cursor)
	if err != nil {
		return pendingInspectionCursor{}, errors.New("encode cursor fingerprint failed")
	}
	result := pendingInspectionCursor{Revision: revision, SHA256: sourceagent.SHA256Hex(raw)}
	for index := range raw {
		raw[index] = 0
	}
	return result, nil
}

func pendingLifecycle(cursor sourceagent.ScanCursor, sequence sourceagent.SequenceState, pending sourceagent.PendingBatch) (string, error) {
	if sequence.Revision != sequence.Sequence {
		return "", errors.New("durable publish revision and sequence are inconsistent")
	}
	previousHash := ""
	if pending.Batch.Batch.PreviousBatchHash != nil {
		previousHash = *pending.Batch.Batch.PreviousBatchHash
	}
	queuedSequence := pending.Batch.Batch.Sequence > 0 && sequence.Sequence == pending.Batch.Batch.Sequence-1 && sequence.LastBatchHash == previousHash
	committedSequence := sequence.Sequence == pending.Batch.Batch.Sequence && sequence.LastBatchHash == pending.Batch.BodyHash
	cursorBefore := cursor == pending.CursorBefore
	cursorAfter := pendingCommittedCursorMatches(pending.CursorAfter, cursor)
	switch {
	case queuedSequence && cursorBefore:
		return "sequence_uncommitted", nil
	case committedSequence && cursorBefore:
		return "sequence_committed_cursor_pending", nil
	case committedSequence && cursorAfter:
		return "cursor_committed_pending_clear", nil
	default:
		return "", errors.New("pending spool is inconsistent with durable cursor, sequence or hash chain")
	}
}

func pendingCommittedCursorMatches(pendingAfter, current sourceagent.ScanCursor) bool {
	if current.Revision == 0 || current.Revision-1 != pendingAfter.Revision {
		return false
	}
	current.Revision = pendingAfter.Revision
	return current == pendingAfter
}

func pendingBatchCursorMetadataConsistent(batch sourceagent.Batch, cursor sourceagent.ScanCursor) bool {
	if batch.SchemaVersion == sourceagent.SchemaVersionV3 {
		snapshotMatches := (!cursor.HasSnapshotMetadata && batch.ScanSnapshotRowCount == nil && batch.ScanSnapshotID == "") ||
			(cursor.HasSnapshotMetadata && batch.ScanSnapshotRowCount != nil && *batch.ScanSnapshotRowCount == cursor.SnapshotRowCount && batch.ScanSnapshotID == cursor.SnapshotID)
		return batch.StreamWatermarkAt == cursor.WatermarkAt && batch.SourceCursor == cursor.WatermarkCursor &&
			batch.ScanCeilingAt == cursor.CeilingAt && batch.ScanCeilingCursor == cursor.CeilingCursor &&
			batch.ScanCycleID == cursor.ScanCycleID && batch.ScanComplete == (cursor.Completed && !cursor.ProjectionBlocked) && snapshotMatches
	}
	return batch.StreamWatermarkAt == "" && batch.SourceCursor == "" && batch.ScanCeilingAt == "" &&
		batch.ScanCeilingCursor == "" && batch.ScanCycleID == "" && !batch.ScanComplete &&
		batch.ScanSnapshotID == "" && batch.ScanSnapshotRowCount == nil
}

func writePendingInspection(output io.Writer, report pendingInspectionReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return errors.New("encode pending inspection failed")
	}
	raw = append(raw, '\n')
	written, writeErr := output.Write(raw)
	for index := range raw {
		raw[index] = 0
	}
	if writeErr != nil || written != len(raw) {
		return errors.New("write pending inspection failed")
	}
	return nil
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
		eligibilityStart, policyErr := loadEligibilityStart(os.Getenv)
		if policyErr != nil {
			return policyErr
		}
		if loadErr = sourceagent.ValidateCutoverEligibility(manifest, eligibilityStart); loadErr != nil {
			return loadErr
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
	if config.ProtocolVersion == sourceagent.SchemaVersionV3 {
		config.EligibilityStartAt, err = loadEligibilityStart(getenv)
		if err != nil {
			return runConfig{}, err
		}
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
	if config.EconomicSafetyDelay, err = envDuration(getenv, "SOURCE_ECONOMIC_SAFETY_DELAY", 5*time.Minute); err != nil {
		return runConfig{}, err
	}
	if config.EconomicSafetyDelay < time.Minute || config.EconomicSafetyDelay > 24*time.Hour {
		return runConfig{}, errors.New("SOURCE_ECONOMIC_SAFETY_DELAY must be between 1 minute and 24 hours")
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
		return nil, errors.New("connect to source bridge database failed")
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
	expectedRoutine := expectedBridgeRoutine(config)
	if expectedRoutine == "" {
		return errors.New("no source bridge privilege contract for source stream")
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
	if len(actual) != 0 || len(expected) != 0 {
		return errors.New("source database caller must not have raw column SELECT grants")
	}
	for field := range expected {
		if _, ok := actual[field]; !ok {
			return errors.New("source database is missing a required projection column grant")
		}
	}
	var superuser, createDB, createRole, replication, bypassRLS, canLogin, inherit, canConnect, canTemporary bool
	var connectionLimit int
	var createSchema, useBridgeSchema, unexpectedSchemaUsage, roleMembership bool
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
		       has_schema_privilege(current_user,'invoice_bridge','USAGE'),
		       EXISTS (
		         SELECT 1 FROM pg_namespace n
		         WHERE n.nspname NOT IN ('public','invoice_bridge')
		           AND n.nspname <> 'information_schema'
		           AND n.nspname NOT LIKE 'pg\_%' ESCAPE '\'
		           AND has_schema_privilege(current_user,n.oid,'USAGE')
		       ),
		       EXISTS (
		         SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid OR m.roleid=r.oid
		       )
		FROM pg_roles r WHERE r.rolname=current_user`).Scan(
		&superuser, &createDB, &createRole, &replication, &bypassRLS,
		&canLogin, &inherit, &connectionLimit, &canConnect, &canTemporary,
		&createSchema, &useBridgeSchema, &unexpectedSchemaUsage, &roleMembership); err != nil {
		return errors.New("inspect source role attributes failed")
	}
	if superuser || createDB || createRole || replication || bypassRLS || !canLogin || inherit || !canConnect ||
		connectionLimit != 2 || canTemporary || createSchema || !useBridgeSchema ||
		unexpectedSchemaUsage || roleMembership {
		return errors.New("source database role is over-privileged")
	}
	expectedRoutineHash := expectedBridgeRoutineHash(config)
	if expectedRoutineHash == "" {
		return errors.New("source bridge routine hash contract is missing")
	}
	var routineOID uint32
	var ownerConnectionLimit int
	var routineHash, ownerName string
	var securityDefiner, ownerCanLogin, ownerSuper, ownerCreateDB, ownerCreateRole, ownerReplication, ownerBypassRLS, ownerInherit bool
	var safeRoutineShape, ownerMembership, relationDependency, callerCanExecute bool
	if err = database.QueryRowContext(ctx, `
		SELECT p.oid,owner.rolname,p.prosecdef,owner.rolcanlogin,owner.rolsuper,owner.rolcreatedb,
		       owner.rolcreaterole,owner.rolreplication,owner.rolbypassrls,
		       owner.rolinherit,owner.rolconnlimit,
		       p.prokind='f' AND p.pronargs=2
		         AND oidvectortypes(p.proargtypes)='text, jsonb'
		         AND p.proretset AND p.prorettype='jsonb'::regtype
		         AND language_row.lanname='plpgsql' AND p.provolatile='v'
		         AND (
		           SELECT COALESCE(array_agg(setting ORDER BY setting),ARRAY[]::text[])
		           FROM unnest(COALESCE(p.proconfig,ARRAY[]::text[])) configured(setting)
		         )=ARRAY['row_security=on','search_path=pg_catalog']::text[],
		       encode(sha256(convert_to(p.prosrc,'UTF8')),'hex'),
		       EXISTS (
		         SELECT 1 FROM pg_auth_members m
		         WHERE m.member=owner.oid OR m.roleid=owner.oid
		       ),
		       EXISTS (
		         SELECT 1 FROM pg_depend d
		         WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid
		           AND d.refclassid='pg_class'::regclass
		       ),
		       has_function_privilege(current_user,p.oid,'EXECUTE')
		FROM pg_proc p JOIN pg_roles owner ON owner.oid=p.proowner
		JOIN pg_language language_row ON language_row.oid=p.prolang
		WHERE p.oid=to_regprocedure($1)`, expectedRoutine).Scan(
		&routineOID, &ownerName, &securityDefiner, &ownerCanLogin, &ownerSuper, &ownerCreateDB,
		&ownerCreateRole, &ownerReplication, &ownerBypassRLS, &ownerInherit, &ownerConnectionLimit,
		&safeRoutineShape, &routineHash, &ownerMembership, &relationDependency, &callerCanExecute); err != nil {
		return errors.New("inspect source bridge routine failed")
	}
	ownerRole := "invoice_" + config.SourceType + "_bridge_owner"
	if routineOID == 0 || ownerName != ownerRole || !securityDefiner || ownerCanLogin || ownerSuper || ownerCreateDB || ownerCreateRole ||
		ownerReplication || ownerBypassRLS || ownerInherit || ownerConnectionLimit != 0 || ownerMembership ||
		!safeRoutineShape || routineHash != expectedRoutineHash || relationDependency || !callerCanExecute {
		return errors.New("source bridge routine security contract failed")
	}
	relations := expectedBridgeRelations(config.SourceType)
	if len(relations) == 0 {
		return errors.New("source bridge relation contract is missing")
	}
	var relationCount, unsafeRelations int
	if err = database.QueryRowContext(ctx, `
		SELECT count(*),count(*) FILTER (
		  WHERE c.relrowsecurity OR c.relforcerowsecurity
		     OR c.relowner=(SELECT oid FROM pg_roles WHERE rolname=$2)
		)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind IN ('r','p')
		  AND c.relname=ANY($1::text[])`, relations, ownerRole).Scan(&relationCount, &unsafeRelations); err != nil {
		return errors.New("inspect source bridge relation safety failed")
	}
	if relationCount != len(relations) || unsafeRelations != 0 {
		return errors.New("source bridge relation inventory or row-security contract failed")
	}
	bridgeRoles := expectedBridgeRoles(config.SourceType)
	bridgeFunctions := expectedBridgeFunctions(config.SourceType)
	routineCallers := expectedBridgeRoutineCallers(config)
	if len(bridgeRoles) != 7 || len(bridgeFunctions) != 5 || len(routineCallers) == 0 {
		return errors.New("source bridge global ACL contract is missing")
	}
	var globalBoundarySafe bool
	if err = database.QueryRowContext(ctx, `
		SELECT
		  n.nspowner=d.datdba
		  AND NOT EXISTS (SELECT 1 FROM pg_class relation WHERE relation.relnamespace=n.oid)
		  AND COALESCE((
		    SELECT array_agg(function_row.proname ORDER BY function_row.proname)
		    FROM pg_proc function_row WHERE function_row.pronamespace=n.oid
		  ),ARRAY[]::name[])=$3::name[]
		  AND NOT EXISTS (
		    SELECT 1 FROM aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) privilege
		    LEFT JOIN pg_roles grantee ON grantee.oid=privilege.grantee
		    WHERE privilege.grantee<>n.nspowner AND (
		      privilege.grantee=0 OR grantee.rolname IS NULL OR
		      grantee.rolname<>ALL($2::text[]) OR privilege.privilege_type<>'USAGE'
		    )
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM unnest($2::text[]) expected(role_name)
		    WHERE NOT has_schema_privilege(expected.role_name,n.oid,'USAGE')
		       OR has_schema_privilege(expected.role_name,n.oid,'CREATE')
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM pg_proc function_row
		    CROSS JOIN LATERAL aclexplode(COALESCE(function_row.proacl,acldefault('f',function_row.proowner))) privilege
		    LEFT JOIN pg_roles grantee ON grantee.oid=privilege.grantee
		    WHERE function_row.oid=$1 AND privilege.grantee<>function_row.proowner AND (
		      privilege.grantee=0 OR grantee.rolname IS NULL OR
		      grantee.rolname<>ALL($4::text[]) OR privilege.privilege_type<>'EXECUTE'
		    )
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM unnest($4::text[]) expected(role_name)
		    WHERE NOT has_function_privilege(expected.role_name,$1,'EXECUTE')
		  )
		FROM pg_namespace n JOIN pg_database d ON d.datname=current_database()
		WHERE n.nspname='invoice_bridge'`, routineOID, bridgeRoles, bridgeFunctions, routineCallers).Scan(&globalBoundarySafe); err != nil {
		return errors.New("inspect source bridge global ACL failed")
	}
	if !globalBoundarySafe {
		return errors.New("source bridge schema, owner or function ACL contract failed")
	}
	var unexpectedBridgeExecute bool
	if err = database.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		  WHERE n.nspname='invoice_bridge' AND p.oid<>$1
		    AND has_function_privilege(current_user,p.oid,'EXECUTE')
		)`, routineOID).Scan(&unexpectedBridgeExecute); err != nil || unexpectedBridgeExecute {
		return errors.New("source database caller can execute an unexpected bridge routine")
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
	return map[string]struct{}{}
}

func expectedBridgeRoutine(config runConfig) string {
	if config.SourceType != sourceagent.SourceSub2API && config.SourceType != sourceagent.SourceNewAPI {
		return ""
	}
	suffix := ""
	switch config.StreamID {
	case sourceagent.StreamPayments:
		suffix = "payments_v4"
	case sourceagent.StreamUsage:
		suffix = "usage_v4"
	case sourceagent.StreamCredits:
		suffix = "credits_v4"
	case sourceagent.StreamBalances:
		suffix = "balances_v4"
	case sourceagent.StreamIdentities:
		suffix = "identities_v4"
	default:
		return ""
	}
	return "invoice_bridge." + config.SourceType + "_" + suffix + "(text,jsonb)"
}

func expectedBridgeRelations(source string) []string {
	switch source {
	case sourceagent.SourceSub2API:
		return []string{
			"payment_orders", "settings", "auth_identities", "usage_logs",
			"promo_code_usages", "user_affiliate_ledger", "redeem_codes", "users",
		}
	case sourceagent.SourceNewAPI:
		return []string{
			"top_ups", "subscription_orders", "options", "custom_oauth_providers",
			"user_oauth_bindings", "logs", "checkins", "redemptions", "users",
		}
	default:
		return nil
	}
}

func expectedBridgeRoutineHash(config runConfig) string {
	hashes := map[string]map[string]string{
		sourceagent.SourceNewAPI: {
			sourceagent.StreamPayments:   "d908e1ef57383ad10ccf0c4d2e266577e51a5e37a1684866ad5dd4f347c10dcf",
			sourceagent.StreamUsage:      "ca68cbf1a9ce5eaacde3c52b2778f535bf3510b24c095150ed2bf7bd7fd8843a",
			sourceagent.StreamCredits:    "307183eda0f2e6900ea9c7dd49194e499e9abfbbb07d308b1558769e257bd8ff",
			sourceagent.StreamBalances:   "63ab9a5c45cb06267c59a6b8e105149679249fce94b7f73abefd85e6f856d484",
			sourceagent.StreamIdentities: "dd92d2fe4b37a8b22509d19507ebae1143dde9952a185a0a180336cc3ef4a7a1",
		},
		sourceagent.SourceSub2API: {
			sourceagent.StreamPayments:   "3ce533217c9535cec7ca711ccb2c011540f4976c414d4543bf3bb13f991c3df1",
			sourceagent.StreamUsage:      "9291f757e1e0c5b0daa0c6858020e6f61f1cfd63f66cfbe4677b7c6dff3b1530",
			sourceagent.StreamCredits:    "5683a8b5eec1b50f33740b63d6a361c003686fa6d90eafc92306882a337ab087",
			sourceagent.StreamBalances:   "00697ce59c06a5a5715dfd4ac58df15e83f76418d8cc4f20ec904cf59d0b36d5",
			sourceagent.StreamIdentities: "443fc8aa1ed8232c742bfe2964a2869e279886694dddeda0a9d532ecad8f1f90",
		},
	}
	return hashes[config.SourceType][config.StreamID]
}

func expectedBridgeRoles(source string) []string {
	if source != sourceagent.SourceSub2API && source != sourceagent.SourceNewAPI {
		return nil
	}
	prefix := "invoice_" + source + "_"
	return []string{
		prefix + "balances_reader", prefix + "bridge_owner", prefix + "credits_reader",
		prefix + "identities_reader", prefix + "payments_reader", prefix + "payments_v3_reader",
		prefix + "usage_reader",
	}
}

func expectedBridgeFunctions(source string) []string {
	if source != sourceagent.SourceSub2API && source != sourceagent.SourceNewAPI {
		return nil
	}
	return []string{
		source + "_balances_v4", source + "_credits_v4", source + "_identities_v4",
		source + "_payments_v4", source + "_usage_v4",
	}
}

func expectedBridgeRoutineCallers(config runConfig) []string {
	prefix := "invoice_" + config.SourceType + "_"
	switch config.StreamID {
	case sourceagent.StreamPayments:
		return []string{prefix + "payments_reader", prefix + "payments_v3_reader"}
	case sourceagent.StreamIdentities:
		return []string{prefix + "identities_reader"}
	case sourceagent.StreamUsage:
		return []string{prefix + "usage_reader"}
	case sourceagent.StreamCredits:
		return []string{prefix + "credits_reader"}
	case sourceagent.StreamBalances:
		return []string{prefix + "balances_reader"}
	default:
		return nil
	}
}

func buildDBConnector(config runConfig, database *sql.DB) (sourceagent.Connector, error) {
	if config.ProtocolVersion == sourceagent.SchemaVersionV3 {
		manifestStore := sourceagent.EncryptedStateFile{Path: config.CutoverManifestFile, Purpose: "cutover_manifest", Keys: sourceagent.FileSpoolKeyProvider{Path: config.CutoverKeyFile}}
		manifest, err := sourceagent.LoadCutoverManifest(context.Background(), manifestStore, config.SourceID, config.SourceType, config.SourceRuntime)
		if err != nil {
			return nil, fmt.Errorf("load encrypted cutover manifest: %w", err)
		}
		if err = sourceagent.ValidateCutoverEligibility(manifest, config.EligibilityStartAt); err != nil {
			return nil, err
		}
		if config.StreamID == sourceagent.StreamBalances && manifest.SigningKeyID != config.SigningKeyID {
			return nil, errors.New("cutover manifest signing key id differs from this stream")
		}
		switch config.StreamID {
		case sourceagent.StreamPayments:
			return &sourceagent.PaymentV3DBConnector{DB: database, Source: config.SourceType, Manifest: manifest, SafetyDelay: config.EconomicSafetyDelay}, nil
		case sourceagent.StreamUsage, sourceagent.StreamCredits:
			return &sourceagent.EconomicDBConnector{DB: database, Source: config.SourceType, Stream: config.StreamID, Manifest: manifest, SafetyDelay: config.EconomicSafetyDelay}, nil
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
