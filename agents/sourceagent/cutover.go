package sourceagent

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	cutoverSchemaVersion           = 1
	cutoverMaxBytes                = 8 << 20
	balanceSnapshotMaxRows         = 2_000_000
	ProjectionContractSub2APIV4    = "sub2api-economic-v4"
	ProjectionContractNewAPIRC25V4 = "newapi-economic-rc25-v4"
)

func expectedEconomicProjectionContract(sourceType string) (string, error) {
	switch sourceType {
	case SourceSub2API:
		return ProjectionContractSub2APIV4, nil
	case SourceNewAPI:
		return ProjectionContractNewAPIRC25V4, nil
	default:
		return "", errors.New("cutover manifest source type is invalid")
	}
}

// SourceHighWater is captured for audit in the same source-database snapshot
// as the legacy balance baseline. It is not itself a post-cutover watermark.
type SourceHighWater struct {
	EventTime string `json:"event_time"`
	Cursor    string `json:"cursor"`
}

type CutoverManifest struct {
	SchemaVersion      int                        `json:"schema_version"`
	SourceID           string                     `json:"source_id"`
	SourceType         string                     `json:"source_type"`
	SourceRuntime      string                     `json:"source_runtime"`
	CutoverAt          string                     `json:"cutover_at"`
	DatabaseClock      string                     `json:"database_clock"`
	ProjectionContract string                     `json:"projection_contract"`
	ConfigurationHash  string                     `json:"configuration_hash"`
	UnitCode           string                     `json:"unit_code"`
	SigningKeyID       string                     `json:"signing_key_id"`
	ManifestHash       string                     `json:"manifest_hash"`
	BaselineSnapshotID string                     `json:"baseline_snapshot_id"`
	BaselineRowCount   string                     `json:"baseline_row_count"`
	HighWaters         map[string]SourceHighWater `json:"high_waters"`
}

type BalanceSnapshotRow struct {
	ExternalUserID  string `json:"external_user_id"`
	ServiceUnits    string `json:"service_units"`
	BalanceNegative bool   `json:"balance_negative"`
	// DeficitServiceUnits (XM-INV-NEGATIVE-DEFICIT) is the magnitude of a
	// negative balance, "0" otherwise. omitempty keeps the content hash of a
	// baseline snapshot sealed before this field existed unchanged.
	DeficitServiceUnits string `json:"deficit_service_units,omitempty"`
	BaselineMember      bool   `json:"baseline_member"`
}

type BalanceSnapshot struct {
	SchemaVersion      int                  `json:"schema_version"`
	SourceID           string               `json:"source_id"`
	SourceType         string               `json:"source_type"`
	SnapshotID         string               `json:"snapshot_id"`
	PreviousSnapshotID string               `json:"previous_snapshot_id,omitempty"`
	CheckpointKind     string               `json:"checkpoint_kind"`
	AsOf               string               `json:"as_of"`
	CutoverAt          string               `json:"cutover_at"`
	UnitCode           string               `json:"unit_code"`
	Rows               []BalanceSnapshotRow `json:"rows"`
}

type EncryptedStateFile struct {
	Path    string
	Purpose string
	Keys    SpoolKeyProvider
}

type encryptedStateEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Algorithm     string `json:"algorithm"`
	Nonce         string `json:"nonce"`
	Ciphertext    string `json:"ciphertext"`
}

func (s EncryptedStateFile) Exists() (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !secureStatePermissions(info) || info.Size() <= 0 || info.Size() > cutoverMaxBytes*2 {
		return false, errors.New("encrypted source state file is unsafe")
	}
	return true, nil
}

func (s EncryptedStateFile) SaveNew(ctx context.Context, value any) error {
	if err := s.validate(); err != nil {
		return err
	}
	if exists, err := s.Exists(); err != nil {
		return err
	} else if exists {
		return errors.New("encrypted source state already exists; refusing overwrite")
	}
	plaintext, err := json.Marshal(value)
	if err != nil || len(plaintext) == 0 || len(plaintext) > cutoverMaxBytes {
		return errors.New("encode encrypted source state failed")
	}
	defer zeroBytes(plaintext)
	raw, err := s.seal(ctx, plaintext)
	if err != nil {
		return err
	}
	defer zeroBytes(raw)
	return writePrivateAtomicFile(s.Path, raw)
}

func (s EncryptedStateFile) Replace(ctx context.Context, value any) error {
	if err := s.validate(); err != nil {
		return err
	}
	plaintext, err := json.Marshal(value)
	if err != nil || len(plaintext) == 0 || len(plaintext) > cutoverMaxBytes {
		return errors.New("encode encrypted source state failed")
	}
	defer zeroBytes(plaintext)
	raw, err := s.seal(ctx, plaintext)
	if err != nil {
		return err
	}
	defer zeroBytes(raw)
	return writePrivateAtomicFile(s.Path, raw)
}

func (s EncryptedStateFile) Load(ctx context.Context, target any) error {
	if err := s.validate(); err != nil {
		return err
	}
	info, err := os.Lstat(s.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !secureStatePermissions(info) || info.Size() <= 0 || info.Size() > cutoverMaxBytes*2 {
		return errors.New("encrypted source state is missing or unsafe")
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return errors.New("open encrypted source state failed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, cutoverMaxBytes*2+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) == 0 || len(raw) > cutoverMaxBytes*2 {
		return errors.New("read encrypted source state failed")
	}
	defer zeroBytes(raw)
	plaintext, err := s.open(ctx, raw)
	if err != nil {
		return err
	}
	defer zeroBytes(plaintext)
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil || ensureDecodeEOF(decoder) != nil {
		return errors.New("decode encrypted source state failed")
	}
	return nil
}

func (s EncryptedStateFile) seal(ctx context.Context, plaintext []byte) ([]byte, error) {
	key, err := s.Keys.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	block, err := aes.NewCipher(key.key)
	if err != nil {
		return nil, errors.New("initialize encrypted source state cipher failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize encrypted source state GCM failed")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, errors.New("generate encrypted source state nonce failed")
	}
	envelope := encryptedStateEnvelope{SchemaVersion: cutoverSchemaVersion, Algorithm: "AES-256-GCM",
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plaintext, s.aad()))}
	return json.Marshal(envelope)
}

func (s EncryptedStateFile) open(ctx context.Context, raw []byte) ([]byte, error) {
	var envelope encryptedStateEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || ensureDecodeEOF(decoder) != nil || envelope.SchemaVersion != cutoverSchemaVersion || envelope.Algorithm != "AES-256-GCM" {
		return nil, errors.New("decode encrypted source state envelope failed")
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(envelope.Nonce)
	if err != nil {
		return nil, errors.New("decode encrypted source state nonce failed")
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) == 0 {
		return nil, errors.New("decode encrypted source state ciphertext failed")
	}
	key, err := s.Keys.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	block, err := aes.NewCipher(key.key)
	if err != nil {
		return nil, errors.New("initialize encrypted source state cipher failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, errors.New("encrypted source state nonce is invalid")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, s.aad())
	if err != nil || len(plaintext) == 0 || len(plaintext) > cutoverMaxBytes {
		return nil, errors.New("encrypted source state authentication failed")
	}
	return plaintext, nil
}

func (s EncryptedStateFile) validate() error {
	if s.Keys == nil || !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path ||
		!causalDomainPattern.MatchString(s.Purpose) {
		return errors.New("encrypted source state configuration is invalid")
	}
	info, err := os.Lstat(filepath.Dir(s.Path))
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !secureStateDirectoryPermissions(info) {
		return errors.New("encrypted source state directory is unsafe")
	}
	return nil
}

func (s EncryptedStateFile) aad() []byte { return []byte("invoice-source-state-v1\x00" + s.Purpose) }

type CutoverCaptureConfig struct {
	SourceID           string
	SourceType         string
	SourceRuntime      string
	SigningKeyID       string
	EligibilityStartAt time.Time
	Manifest           EncryptedStateFile
	Snapshot           EncryptedStateFile
}

// ValidateCutoverEligibility keeps the global technical baseline strictly
// before the inclusive business eligibility boundary. A baseline at or after
// the boundary would conservatively swallow otherwise eligible facts.
func ValidateCutoverEligibility(manifest CutoverManifest, eligibilityStartAt time.Time) error {
	if eligibilityStartAt.IsZero() {
		return errors.New("invoice eligibility start is required for cutover")
	}
	cutoverAt, err := time.Parse(time.RFC3339Nano, manifest.CutoverAt)
	if err != nil {
		return errors.New("cutover manifest time is invalid")
	}
	databaseClock, err := time.Parse(time.RFC3339Nano, manifest.DatabaseClock)
	if err != nil {
		return errors.New("cutover database clock is invalid")
	}
	start := eligibilityStartAt.UTC()
	expectedContract, err := expectedEconomicProjectionContract(manifest.SourceType)
	if err != nil {
		return err
	}
	if manifest.ProjectionContract != expectedContract {
		return errors.New("cutover projection contract does not match source type")
	}
	if !cutoverAt.UTC().Before(start) || !databaseClock.UTC().Before(start) {
		return errors.New("source cutover must be strictly before invoice eligibility start")
	}
	return nil
}

// CaptureCutover performs the mandatory atomic baseline read. No upstream
// write is possible: the transaction is explicitly REPEATABLE READ READ ONLY.
// Both output files are create-only; a second invocation fails closed.
func CaptureCutover(ctx context.Context, db *sql.DB, config CutoverCaptureConfig) (CutoverManifest, error) {
	if db == nil || !uuidPattern.MatchString(config.SourceID) ||
		(config.SourceType != SourceSub2API && config.SourceType != SourceNewAPI) ||
		strings.TrimSpace(config.SourceRuntime) == "" || len(config.SourceRuntime) > 64 ||
		config.EligibilityStartAt.IsZero() || ValidateSigningKeyID(config.SigningKeyID) != nil {
		return CutoverManifest{}, errors.New("cutover capture configuration is invalid")
	}
	for _, store := range []EncryptedStateFile{config.Manifest, config.Snapshot} {
		if exists, err := store.Exists(); err != nil {
			return CutoverManifest{}, err
		} else if exists {
			return CutoverManifest{}, errors.New("cutover output already exists; refusing overwrite")
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return CutoverManifest{}, fmt.Errorf("begin read-only cutover snapshot: %w", err)
	}
	defer tx.Rollback()
	var dbReadOnly string
	if err = tx.QueryRowContext(ctx, `SHOW transaction_read_only`).Scan(&dbReadOnly); err != nil || dbReadOnly != "on" {
		return CutoverManifest{}, errors.New("cutover transaction is not read only")
	}
	var cutover time.Time
	if err = tx.QueryRowContext(ctx, `SELECT transaction_timestamp()`).Scan(&cutover); err != nil {
		return CutoverManifest{}, errors.New("capture source database cutover clock failed")
	}
	if !cutover.UTC().Before(config.EligibilityStartAt.UTC()) {
		return CutoverManifest{}, errors.New("source cutover reached invoice eligibility start")
	}
	contractRelation, err := bridgeJSONRecordRelation(config.SourceType, StreamBalances, "contract",
		"projection_contract text,contract_ok boolean,configuration_hash text,payments_event_at timestamptz,payments_cursor text,usage_event_at timestamptz,usage_cursor text,credits_event_at timestamptz,credits_cursor text")
	if err != nil {
		return CutoverManifest{}, err
	}
	request, err := marshalBridgeRequest(nil)
	if err != nil {
		return CutoverManifest{}, err
	}
	var contract, configurationHash string
	var contractOK bool
	var paymentAt, usageAt, creditAt time.Time
	var paymentCursor, usageCursor, creditCursor string
	query := `SELECT projection_contract,contract_ok,configuration_hash,payments_event_at,payments_cursor,usage_event_at,usage_cursor,credits_event_at,credits_cursor FROM ` + contractRelation
	if err = tx.QueryRowContext(ctx, query, request).Scan(&contract, &contractOK, &configurationHash, &paymentAt, &paymentCursor, &usageAt, &usageCursor, &creditAt, &creditCursor); err != nil {
		return CutoverManifest{}, fmt.Errorf("capture cutover contract: %w", err)
	}
	expectedContract, err := expectedEconomicProjectionContract(config.SourceType)
	if err != nil {
		return CutoverManifest{}, err
	}
	if !contractOK || contract != expectedContract || !hexHashPattern.MatchString(configurationHash) {
		return CutoverManifest{}, errors.New("source projection contract is not healthy at cutover")
	}
	balanceRelation, err := bridgeJSONRecordRelation(config.SourceType, StreamBalances, "rows", balanceRowRecordDefinition)
	if err != nil {
		return CutoverManifest{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id,balance_service_units,balance_negative,deficit_service_units FROM `+balanceRelation+` ORDER BY user_id`, request)
	if err != nil {
		return CutoverManifest{}, fmt.Errorf("capture cutover balances: %w", err)
	}
	baseline := make([]BalanceSnapshotRow, 0, 4096)
	for rows.Next() {
		var userID int64
		var units string
		var negative bool
		var deficit sql.NullString
		if err = rows.Scan(&userID, &units, &negative, &deficit); err != nil || userID <= 0 || !serviceUnitsPattern.MatchString(units) {
			_ = rows.Close()
			return CutoverManifest{}, errors.New("cutover balance projection contains an invalid row")
		}
		row := BalanceSnapshotRow{ExternalUserID: fmt.Sprint(userID), ServiceUnits: units, BalanceNegative: negative, BaselineMember: true}
		if row.DeficitServiceUnits, err = balanceRowDeficit(deficit, units, negative); err != nil {
			_ = rows.Close()
			return CutoverManifest{}, err
		}
		baseline = append(baseline, row)
		if len(baseline) > balanceSnapshotMaxRows {
			_ = rows.Close()
			return CutoverManifest{}, errors.New("cutover balance projection exceeds the reviewed bound")
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return CutoverManifest{}, errors.New("iterate cutover balance projection failed")
	}
	if err = rows.Close(); err != nil {
		return CutoverManifest{}, errors.New("close cutover balance projection failed")
	}
	if err = tx.Commit(); err != nil {
		return CutoverManifest{}, errors.New("commit read-only cutover snapshot failed")
	}
	cutoverAt := cutover.UTC().Format(time.RFC3339Nano)
	snapshot := BalanceSnapshot{SchemaVersion: cutoverSchemaVersion, SourceID: config.SourceID, SourceType: config.SourceType,
		CheckpointKind: "cutover", AsOf: cutoverAt, CutoverAt: cutoverAt, UnitCode: unitCodeForSource(config.SourceType), Rows: baseline}
	snapshot.SnapshotID, err = balanceSnapshotID(snapshot)
	if err != nil {
		return CutoverManifest{}, err
	}
	manifest := CutoverManifest{SchemaVersion: cutoverSchemaVersion, SourceID: config.SourceID, SourceType: config.SourceType,
		SourceRuntime: config.SourceRuntime, CutoverAt: cutoverAt, DatabaseClock: cutoverAt, ProjectionContract: contract,
		ConfigurationHash: configurationHash, BaselineSnapshotID: snapshot.SnapshotID, BaselineRowCount: fmt.Sprint(len(baseline)),
		UnitCode: unitCodeForSource(config.SourceType), SigningKeyID: config.SigningKeyID,
		HighWaters: map[string]SourceHighWater{
			StreamPayments: {EventTime: paymentAt.UTC().Format(time.RFC3339Nano), Cursor: paymentCursor},
			StreamUsage:    {EventTime: usageAt.UTC().Format(time.RFC3339Nano), Cursor: usageCursor},
			StreamCredits:  {EventTime: creditAt.UTC().Format(time.RFC3339Nano), Cursor: creditCursor},
			StreamBalances: {EventTime: cutoverAt, Cursor: "balance_snapshot:0"},
		}}
	manifest.ManifestHash, err = cutoverManifestHash(manifest)
	if err != nil {
		return CutoverManifest{}, err
	}
	if err = validateCutoverManifest(manifest); err != nil {
		return CutoverManifest{}, err
	}
	// The balance baseline is written first. If the manifest write fails, the
	// orphan is intentionally not overwritten by a retry; an operator must
	// inspect and remove both files together.
	if err = config.Snapshot.SaveNew(ctx, snapshot); err != nil {
		return CutoverManifest{}, err
	}
	if err = config.Manifest.SaveNew(ctx, manifest); err != nil {
		return CutoverManifest{}, err
	}
	return manifest, nil
}

func LoadAndCheckCutover(ctx context.Context, manifestStore, snapshotStore EncryptedStateFile, sourceID, sourceType, runtimeVersion string) (CutoverManifest, BalanceSnapshot, error) {
	manifest, err := LoadCutoverManifest(ctx, manifestStore, sourceID, sourceType, runtimeVersion)
	if err != nil {
		return manifest, BalanceSnapshot{}, err
	}
	var snapshot BalanceSnapshot
	if err := snapshotStore.Load(ctx, &snapshot); err != nil {
		return manifest, snapshot, err
	}
	if err := validateBalanceSnapshot(snapshot); err != nil || snapshot.SourceID != sourceID || snapshot.SourceType != sourceType ||
		snapshot.SnapshotID != manifest.BaselineSnapshotID || fmt.Sprint(len(snapshot.Rows)) != manifest.BaselineRowCount || snapshot.CutoverAt != manifest.CutoverAt || snapshot.CheckpointKind != "cutover" {
		return manifest, snapshot, errors.New("cutover balance snapshot does not match manifest")
	}
	return manifest, snapshot, nil
}

func LoadCutoverManifest(ctx context.Context, store EncryptedStateFile, sourceID, sourceType, runtimeVersion string) (CutoverManifest, error) {
	var manifest CutoverManifest
	if err := store.Load(ctx, &manifest); err != nil {
		return manifest, err
	}
	if err := validateCutoverManifest(manifest); err != nil || manifest.SourceID != sourceID || manifest.SourceType != sourceType || manifest.SourceRuntime != runtimeVersion {
		return manifest, errors.New("cutover manifest source or runtime mismatch")
	}
	return manifest, nil
}

func validateCutoverManifest(value CutoverManifest) error {
	if value.SchemaVersion != cutoverSchemaVersion || !uuidPattern.MatchString(value.SourceID) ||
		(value.SourceType != SourceSub2API && value.SourceType != SourceNewAPI) || value.SourceRuntime == "" ||
		value.ProjectionContract == "" || !hexHashPattern.MatchString(value.ConfigurationHash) || !hexHashPattern.MatchString(value.BaselineSnapshotID) ||
		!hexHashPattern.MatchString(value.ManifestHash) || !serviceUnitsPattern.MatchString(value.BaselineRowCount) || value.UnitCode != unitCodeForSource(value.SourceType) || ValidateSigningKeyID(value.SigningKeyID) != nil {
		return errors.New("cutover manifest metadata is invalid")
	}
	cutover, err := time.Parse(time.RFC3339Nano, value.CutoverAt)
	if err != nil || cutover.After(time.Now().Add(time.Minute)) {
		return errors.New("cutover manifest time is invalid")
	}
	if databaseClock, parseErr := time.Parse(time.RFC3339Nano, value.DatabaseClock); parseErr != nil || databaseClock.Before(cutover) || databaseClock.After(cutover.Add(5*time.Minute)) {
		return errors.New("cutover manifest database clock is invalid")
	}
	for _, stream := range []string{StreamPayments, StreamUsage, StreamCredits, StreamBalances} {
		watermark, ok := value.HighWaters[stream]
		if !ok || strings.TrimSpace(watermark.Cursor) == "" || len(watermark.Cursor) > 256 || strings.ContainsAny(watermark.Cursor, "\x00\r\n") {
			return errors.New("cutover manifest is missing a stream high-water")
		}
		if _, err = time.Parse(time.RFC3339Nano, watermark.EventTime); err != nil {
			return errors.New("cutover manifest high-water time is invalid")
		}
	}
	expected, err := cutoverManifestHash(value)
	if err != nil || expected != value.ManifestHash {
		return errors.New("cutover manifest content hash mismatch")
	}
	return nil
}

func cutoverManifestHash(value CutoverManifest) (string, error) {
	payload := value.Payload()
	payload.ManifestHash = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return SHA256Hex(raw), nil
}

func (value CutoverManifest) Payload() CutoverManifestPayload {
	return CutoverManifestPayload{
		SourceInstanceID: value.SourceID, ManifestHash: value.ManifestHash, CutoverAt: value.CutoverAt,
		DatabaseClock: value.DatabaseClock, SourceRuntimeVersion: value.SourceRuntime,
		ConfigurationHash: value.ConfigurationHash, UnitCode: value.UnitCode,
		ProjectionContract:   value.ProjectionContract,
		PaymentsCeiling:      value.HighWaters[StreamPayments].Cursor,
		UsageCeiling:         value.HighWaters[StreamUsage].Cursor,
		CreditsCeiling:       value.HighWaters[StreamCredits].Cursor,
		BalancesCeiling:      value.HighWaters[StreamBalances].Cursor,
		BaselineSnapshotHash: value.BaselineSnapshotID, BaselineRowCount: value.BaselineRowCount, SigningKeyID: value.SigningKeyID,
	}
}

func validateBalanceSnapshot(value BalanceSnapshot) error {
	if value.SchemaVersion != cutoverSchemaVersion || !uuidPattern.MatchString(value.SourceID) ||
		(value.SourceType != SourceSub2API && value.SourceType != SourceNewAPI) || !hexHashPattern.MatchString(value.SnapshotID) ||
		(value.CheckpointKind != "cutover" && value.CheckpointKind != "reconciliation") || value.UnitCode != unitCodeForSource(value.SourceType) || len(value.Rows) > balanceSnapshotMaxRows {
		return errors.New("balance snapshot metadata is invalid")
	}
	if value.PreviousSnapshotID != "" && !hexHashPattern.MatchString(value.PreviousSnapshotID) {
		return errors.New("balance snapshot predecessor is invalid")
	}
	if value.CheckpointKind == "cutover" && value.PreviousSnapshotID != "" {
		return errors.New("cutover balance snapshot cannot have a predecessor")
	}
	if _, err := time.Parse(time.RFC3339Nano, value.AsOf); err != nil {
		return errors.New("balance snapshot as_of is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, value.CutoverAt); err != nil {
		return errors.New("balance snapshot cutover_at is invalid")
	}
	prior := ""
	for _, row := range value.Rows {
		if !externalReferencePattern.MatchString(row.ExternalUserID) || !serviceUnitsPattern.MatchString(row.ServiceUnits) || (prior != "" && compareDecimalIDs(prior, row.ExternalUserID) >= 0) {
			return errors.New("balance snapshot rows are invalid or unordered")
		}
		if (row.DeficitServiceUnits != "" && !serviceUnitsPattern.MatchString(row.DeficitServiceUnits)) ||
			(row.DeficitServiceUnits != "" && (row.DeficitServiceUnits != "0") != row.BalanceNegative) ||
			(row.BalanceNegative && row.ServiceUnits != "0") {
			return errors.New("balance snapshot row deficit is inconsistent")
		}
		prior = row.ExternalUserID
		if value.CheckpointKind == "cutover" && !row.BaselineMember {
			return errors.New("cutover balance snapshot contains a non-baseline row")
		}
	}
	expected, err := balanceSnapshotID(value)
	if err != nil || expected != value.SnapshotID {
		return errors.New("balance snapshot content hash mismatch")
	}
	return nil
}

// balanceRowRecordDefinition is the jsonb_to_record column list every
// balances 'rows' reader declares. deficit_service_units arrived with
// XM-INV-NEGATIVE-DEFICIT; a bridge installed before it yields NULL there,
// which balanceRowDeficit refuses -- the operator must run install-economic
// before this agent build may capture a snapshot.
const balanceRowRecordDefinition = "user_id bigint,balance_service_units text,balance_negative boolean,deficit_service_units text"

// balanceRowDeficit validates a captured deficit against the row's own
// units/negative flag: a negative balance is "0" units plus a positive
// deficit, a non-negative balance is a "0" deficit.
func balanceRowDeficit(deficit sql.NullString, units string, negative bool) (string, error) {
	if !deficit.Valid {
		return "", errors.New("balance projection does not report deficit_service_units; install the current economic bridge first")
	}
	if !serviceUnitsPattern.MatchString(deficit.String) || (deficit.String != "0") != negative || (negative && units != "0") {
		return "", errors.New("balance projection row deficit is inconsistent")
	}
	return deficit.String, nil
}

func balanceSnapshotID(value BalanceSnapshot) (string, error) {
	copy := value
	copy.SnapshotID = ""
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return SHA256Hex(raw), nil
}

func unitCodeForSource(sourceType string) string {
	if sourceType == SourceSub2API {
		return "SUB2_BALANCE_1E8"
	}
	return "NEWAPI_QUOTA"
}

func compareDecimalIDs(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func sortedHighWaterKeys(values map[string]SourceHighWater) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
