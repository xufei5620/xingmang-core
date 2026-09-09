package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	archive "github.com/xufei5620/xingmang-platform/internal/platform/audit/archive"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

const (
	exitOK          = 0
	exitIntegrity   = 1
	exitConfig      = 2
	exitOutdated    = 3
	exitOperational = 4
	exitConflict    = 5
)

type verifyLocalFunc func(context.Context, string, dependencies) (archive.VerificationReport, error)

type dependencies struct {
	source          archive.LocalAuditSourceReader
	environment     string
	localRoot       string
	rootKeyring     archive.Keyring
	manifestKeyring archive.Keyring
	manifestSigner  archive.PurposeSigner
	exporterVersion string
	exporterCommit  string
	now             func() time.Time
	verifyLocal     verifyLocalFunc
}

type configError struct{ code string }

func (err *configError) Error() string { return err.code }

type conflictError struct{ code string }

func (err *conflictError) Error() string { return err.code }

func main() {
	ctx := context.Background()
	if len(os.Args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, "command_required")
		os.Exit(exitConfig)
	}
	runtime, err := dependenciesForCommand(ctx, os.Args[1], os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "invalid_config")
		os.Exit(exitConfig)
	}
	exitCode := run(ctx, os.Args[1:], runtime.dependencies, os.Stdout, os.Stderr)
	runtime.close()
	os.Exit(exitCode)
}

func run(
	ctx context.Context, args []string, deps dependencies, stdout, stderr io.Writer,
) int {
	if len(args) == 0 {
		writeStableError(stderr, "command_required")
		return exitConfig
	}
	var err error
	switch args[0] {
	case "plan":
		err = runPlan(ctx, args[1:], deps, stdout)
	case "export-local":
		err = runExportLocal(ctx, args[1:], deps, stdout)
	case "verify-local":
		err = runVerifyLocal(ctx, args[1:], deps, stdout)
	default:
		err = &configError{code: "unknown_command"}
	}
	if err == nil {
		return exitOK
	}
	code, stable := classifyError(err)
	writeStableError(stderr, stable)
	return code
}

func writeStableError(writer io.Writer, code string) {
	_, _ = fmt.Fprintln(writer, code)
}

func classifyError(err error) (int, string) {
	var integrity *archive.IntegrityError
	if errors.As(err, &integrity) {
		return exitIntegrity, string(integrity.Report.Code)
	}
	var format *archive.FormatError
	if errors.As(err, &format) {
		return exitIntegrity, format.Code
	}
	var config *configError
	if errors.As(err, &config) {
		return exitConfig, config.code
	}
	var compatibility *archive.CompatibilityError
	if errors.As(err, &compatibility) {
		return exitOutdated, archive.CompatibilityVerifierOutdated
	}
	var operational *archive.OperationalError
	if errors.As(err, &operational) {
		return exitOperational, operational.Code
	}
	var conflict *conflictError
	if errors.As(err, &conflict) {
		return exitConflict, conflict.code
	}
	return exitConfig, "invalid_config"
}

func flagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

func runPlan(ctx context.Context, args []string, deps dependencies, stdout io.Writer) error {
	set := flagSet("plan")
	from := set.Int64("from", 0, "first global sequence")
	to := set.Int64("to", 0, "last global sequence")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || *from < 1 || *to < *from {
		return &configError{code: "invalid_plan_range"}
	}
	if deps.source == nil {
		return &configError{code: "source_unavailable"}
	}
	events, err := deps.source.List(ctx, *from, *to)
	if err != nil {
		return &archive.OperationalError{Code: "source_read_failed", Cause: err}
	}
	if int64(len(events)) != *to-*from+1 || len(events) == 0 ||
		events[0].Sequence != *from || events[len(events)-1].Sequence != *to {
		return &archive.IntegrityError{Report: archive.VerificationReport{
			Code: archive.VerificationSequenceGap, FirstBadSequence: *from,
		}}
	}
	var output bytes.Buffer
	encoded, err := archive.EncodePayload(&output, events)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		FromSequence   int64 `json:"from_sequence"`
		ToSequence     int64 `json:"to_sequence"`
		RowCount       int64 `json:"row_count"`
		EstimatedBytes int64 `json:"estimated_bytes"`
	}{*from, *to, int64(len(events)), encoded.SizeBytes})
}

func runExportLocal(ctx context.Context, args []string, deps dependencies, stdout io.Writer) error {
	set := flagSet("export-local")
	to := set.Int64("to", 0, "last global sequence")
	rootID := set.String("root-id", "", "trusted Chain Root UUID")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || *to < 1 || strings.TrimSpace(*rootID) == "" {
		return &configError{code: "invalid_export_params"}
	}
	if deps.environment == "production" {
		return &configError{code: "production_local_export_forbidden"}
	}
	if deps.environment != "development" && deps.environment != "staging" {
		return &configError{code: "environment_invalid"}
	}
	if deps.source == nil || deps.manifestSigner == nil || deps.rootKeyring == nil ||
		deps.manifestKeyring == nil || strings.TrimSpace(deps.localRoot) == "" {
		return &configError{code: "export_dependencies_missing"}
	}
	events, err := deps.source.List(ctx, 1, *to)
	if err != nil {
		return &archive.OperationalError{Code: "source_read_failed", Cause: err}
	}
	if int64(len(events)) != *to || len(events) == 0 || events[0].Sequence != 1 ||
		events[len(events)-1].Sequence != *to {
		return &archive.IntegrityError{Report: archive.VerificationReport{
			Code: archive.VerificationSequenceGap, FirstBadSequence: 1,
		}}
	}
	problem, err := deps.source.VerifyChain(ctx, 1, *to)
	if err != nil {
		return &archive.OperationalError{Code: "source_verify_failed", Cause: err}
	}
	if problem != nil {
		return &archive.IntegrityError{Report: archive.VerificationReport{
			Code: archive.VerificationBrokenLink, FirstBadSequence: problem.Sequence,
		}}
	}
	root, err := deps.source.LatestRoot(ctx)
	if err != nil {
		return &archive.OperationalError{Code: "root_read_failed", Cause: err}
	}
	parsedRootID, err := uuid.Parse(*rootID)
	if err != nil || root.ID != parsedRootID || root.ToSequence != *to {
		return &configError{code: "root_does_not_cover_export"}
	}
	if root.RootHash != events[len(events)-1].EventHash {
		return &archive.IntegrityError{Report: archive.VerificationReport{
			Code: archive.VerificationRootSignature, FirstBadSequence: root.ToSequence,
		}}
	}
	rootRef := archive.ChainRootRefV1{
		ID: root.ID.String(), ComputedAt: archive.NewWireTime(root.ComputedAt),
		FromSequence: root.FromSequence, ToSequence: root.ToSequence,
		RootHash: root.RootHash, Signature: root.Signature, KeyID: root.KeyID,
	}
	if err := archive.VerifyChainRootSignature(rootRef, deps.rootKeyring); err != nil {
		return &archive.IntegrityError{Report: archive.VerificationReport{
			Code: archive.VerificationRootSignature, FirstBadSequence: root.ToSequence,
		}}
	}
	now := deps.now
	if now == nil {
		now = time.Now
	}
	observedAt := now().UTC().Truncate(time.Microsecond)
	segments, err := archive.SegmentPayloads(events, 25_000, 64<<20)
	if err != nil {
		return err
	}
	if len(segments) != 1 {
		return &configError{code: "local_export_multi_segment_requires_trusted_continuation"}
	}
	segment := segments[0]
	payloadKey := objectKey("payload", "", segment.Boundary.FromSequence,
		segment.Boundary.ToSequence, segment.Encoded.SHA256, "ndjson")
	if _, err := writeLocalObject(deps.localRoot, payloadKey, segment.Bytes); err != nil {
		return err
	}
	payloadObject := localObjectVersion(payloadKey, segment.Encoded)

	environments := make(map[string]struct{})
	for _, event := range events {
		environments[event.Environment] = struct{}{}
	}
	orderedEnvironments := make([]string, 0, len(environments))
	for environment := range environments {
		orderedEnvironments = append(orderedEnvironments, environment)
	}
	sort.Strings(orderedEnvironments)
	projections := make([]archive.ProjectionRefV1, 0, len(orderedEnvironments))
	projectionReaders := make(map[string]io.Reader, len(orderedEnvironments))
	for _, environment := range orderedEnvironments {
		var output bytes.Buffer
		encoded, err := archive.EncodeProjection(&output, environment, events)
		if err != nil {
			return err
		}
		key := objectKey("projection", environment, 1, *to, encoded.SHA256, "ndjson")
		if _, err := writeLocalObject(deps.localRoot, key, output.Bytes()); err != nil {
			return err
		}
		projections = append(projections, archive.ProjectionRefV1{
			Environment: environment, Object: localObjectVersion(key, encoded),
		})
		projectionReaders[environment] = bytes.NewReader(output.Bytes())
	}
	canonicalCounts := [2]archive.CanonicalCountV1{{Version: 1}, {Version: 2}}
	for _, event := range events {
		canonicalCounts[event.CanonicalVersion-1].RowCount++
	}
	manifest := archive.ManifestV1{
		Kind: archive.ManifestKind, FormatVersion: archive.FormatVersion,
		ExporterVersion: deps.exporterVersion, ExporterCommit: deps.exporterCommit,
		FromSequence: 1, ToSequence: *to, RowCount: int64(len(events)),
		FirstPrevHash: events[0].PrevHash, FirstEventHash: events[0].EventHash,
		LastEventHash: events[len(events)-1].EventHash,
		PreviousManifest: archive.PreviousManifestRefV1{
			Kind: "genesis", SHA256: archive.ManifestGenesisSHA256,
		},
		CanonicalVersionCounts: canonicalCounts, OversizedRecordCount: segment.OversizedRecordCount,
		Payload: payloadObject, Projections: projections,
		ChainRoot: rootRef,
		CreatedAt: archive.NewWireTime(observedAt), SourceTipObservedAt: archive.NewWireTime(observedAt),
		ActionRunSnapshotAt: archive.NewWireTime(observedAt),
	}
	signed, err := archive.SignManifest(manifest, deps.manifestSigner)
	if err != nil {
		return err
	}
	manifestBytes, err := archive.EncodeSignedManifestV1(signed)
	if err != nil {
		return err
	}
	manifestSum := sha256.Sum256(manifestBytes)
	manifestSHA := hex.EncodeToString(manifestSum[:])
	manifestKey := objectKey("manifest", "", 1, *to, manifestSHA, "json")
	manifestPath, err := writeLocalObject(deps.localRoot, manifestKey, manifestBytes)
	if err != nil {
		return err
	}
	if _, err := archive.ExportRoot(root, deps.localRoot); err != nil {
		return err
	}
	report, err := archive.VerifySegment(ctx, signed, bytes.NewReader(segment.Bytes), projectionReaders,
		deps.rootKeyring, deps.manifestKeyring, nil)
	if err != nil {
		return err
	}
	if report.Code != archive.VerificationOK {
		return &archive.IntegrityError{Report: report}
	}
	return json.NewEncoder(stdout).Encode(struct {
		ManifestPath string `json:"manifest_path"`
		ManifestSHA  string `json:"manifest_sha256"`
		Rows         int64  `json:"rows"`
	}{manifestPath, manifestSHA, int64(len(events))})
}

func objectKey(kind, environment string, from, to int64, digest, extension string) string {
	if environment == "" {
		return fmt.Sprintf("audit/v1/%s/seq-%019d-%019d-%s.%s", kind, from, to, digest, extension)
	}
	return fmt.Sprintf("audit/v1/%s/%s/seq-%019d-%019d-%s.%s",
		kind, environment, from, to, digest, extension)
}

func localObjectVersion(key string, encoded archive.EncodedObjectV1) archive.ObjectVersionV1 {
	return archive.ObjectVersionV1{
		BucketID: "local-fixture", Key: key, VersionID: encoded.SHA256,
		SHA256: encoded.SHA256, SizeBytes: encoded.SizeBytes, ContentType: encoded.ContentType,
		ProviderChecksum: "sha256:" + encoded.SHA256, ETag: encoded.SHA256,
		EncryptionMode: "LOCAL-ONLY", KMSKeyID: "", ObjectLockMode: "NONE",
		RetainUntil: archive.NewWireTime(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)),
		RowCount:    encoded.RowCount,
	}
}

func runVerifyLocal(ctx context.Context, args []string, deps dependencies, stdout io.Writer) error {
	set := flagSet("verify-local")
	manifestPath := set.String("manifest", "", "approved local manifest path")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || strings.TrimSpace(*manifestPath) == "" {
		return &configError{code: "invalid_verify_params"}
	}
	verify := deps.verifyLocal
	if verify == nil {
		verify = defaultVerifyLocal
	}
	report, err := verify(ctx, *manifestPath, deps)
	if err != nil {
		return err
	}
	if report.Code != archive.VerificationOK {
		return &archive.IntegrityError{Report: report}
	}
	return json.NewEncoder(stdout).Encode(report)
}

func defaultVerifyLocal(
	ctx context.Context, terminalPath string, deps dependencies,
) (archive.VerificationReport, error) {
	if deps.rootKeyring == nil || deps.manifestKeyring == nil || strings.TrimSpace(deps.localRoot) == "" {
		return archive.VerificationReport{}, &configError{code: "verify_dependencies_missing"}
	}
	chain, err := loadLocalManifestChain(terminalPath, deps.localRoot, deps.manifestKeyring)
	if err != nil {
		return archive.VerificationReport{}, err
	}
	var previous *archive.SignedManifestV1
	var final archive.VerificationReport
	for index := range chain {
		manifest := chain[index]
		payloadPath, err := safeLocalPath(deps.localRoot, manifest.Unsigned.Payload.Key)
		if err != nil {
			return archive.VerificationReport{}, err
		}
		payload, err := os.Open(payloadPath)
		if err != nil {
			return archive.VerificationReport{}, &archive.OperationalError{Code: "object_read_failed", Cause: err}
		}
		projections := make(map[string]io.Reader, len(manifest.Unsigned.Projections))
		opened := make([]*os.File, 0, len(manifest.Unsigned.Projections))
		for _, ref := range manifest.Unsigned.Projections {
			path, err := safeLocalPath(deps.localRoot, ref.Object.Key)
			if err != nil {
				_ = payload.Close()
				return archive.VerificationReport{}, err
			}
			file, err := os.Open(path)
			if err != nil {
				_ = payload.Close()
				return archive.VerificationReport{}, &archive.OperationalError{Code: "object_read_failed", Cause: err}
			}
			opened = append(opened, file)
			projections[ref.Environment] = file
		}
		final, err = archive.VerifySegment(ctx, manifest, payload, projections,
			deps.rootKeyring, deps.manifestKeyring, previous)
		_ = payload.Close()
		for _, file := range opened {
			_ = file.Close()
		}
		if err != nil {
			return archive.VerificationReport{}, err
		}
		if final.Code != archive.VerificationOK {
			return final, &archive.IntegrityError{Report: final}
		}
		previous = &chain[index]
	}
	return final, nil
}

func loadLocalManifestChain(
	terminalPath, localRoot string, manifestKeyring archive.Keyring,
) ([]archive.SignedManifestV1, error) {
	currentPath, err := safeLocalPath(localRoot, terminalPath)
	if err != nil {
		return nil, err
	}
	reversed := make([]archive.SignedManifestV1, 0)
	seen := make(map[string]struct{})
	for len(reversed) < 10_000 {
		raw, err := os.ReadFile(currentPath)
		if err != nil {
			return nil, &archive.OperationalError{Code: "object_read_failed", Cause: err}
		}
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		if _, found := seen[digest]; found {
			return nil, &conflictError{code: "manifest_cycle"}
		}
		seen[digest] = struct{}{}
		manifest, err := archive.DecodeSignedManifestV1(raw)
		if err != nil {
			return nil, err
		}
		if err := archive.VerifyManifestSignature(manifest, manifestKeyring); err != nil {
			return nil, &archive.IntegrityError{Report: archive.VerificationReport{
				Code: archive.VerificationManifestSignature,
			}}
		}
		reversed = append(reversed, manifest)
		previous := manifest.Unsigned.PreviousManifest
		if manifest.Unsigned.FromSequence == 1 {
			if previous.Kind != "genesis" || previous.SHA256 != archive.ManifestGenesisSHA256 {
				return nil, &archive.IntegrityError{Report: archive.VerificationReport{Code: archive.VerificationManifestGap}}
			}
			break
		}
		if previous.Kind != "object" || previous.BucketID == "" || previous.Key == "" ||
			previous.VersionID == "" || previous.SHA256 == "" {
			return nil, &archive.IntegrityError{Report: archive.VerificationReport{Code: archive.VerificationManifestGap}}
		}
		currentPath, err = safeLocalPath(localRoot, previous.Key)
		if err != nil {
			return nil, err
		}
		previousRaw, err := os.ReadFile(currentPath)
		if err != nil {
			return nil, &archive.OperationalError{Code: "object_read_failed", Cause: err}
		}
		previousSum := sha256.Sum256(previousRaw)
		if hex.EncodeToString(previousSum[:]) != previous.SHA256 {
			return nil, &archive.IntegrityError{Report: archive.VerificationReport{Code: archive.VerificationManifestGap}}
		}
	}
	if len(reversed) == 0 || reversed[len(reversed)-1].Unsigned.FromSequence != 1 {
		return nil, &conflictError{code: "manifest_chain_too_long"}
	}
	chain := make([]archive.SignedManifestV1, len(reversed))
	for index := range reversed {
		chain[len(reversed)-1-index] = reversed[index]
	}
	return chain, nil
}

func safeLocalPath(root, raw string) (string, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", &configError{code: "invalid_local_root"}
	}
	var candidate string
	if filepath.IsAbs(raw) {
		candidate, err = filepath.Abs(raw)
	} else {
		candidate, err = filepath.Abs(filepath.Join(rootAbsolute, filepath.FromSlash(raw)))
	}
	if err != nil {
		return "", &configError{code: "invalid_local_path"}
	}
	rootPrefix := strings.TrimSuffix(filepath.Clean(rootAbsolute), string(os.PathSeparator)) + string(os.PathSeparator)
	candidateClean := filepath.Clean(candidate)
	if candidateClean != filepath.Clean(rootAbsolute) && !strings.HasPrefix(candidateClean+string(os.PathSeparator), rootPrefix) {
		return "", &configError{code: "local_path_escape"}
	}
	return candidateClean, nil
}

func writeLocalObject(root, key string, data []byte) (string, error) {
	path, err := safeLocalPath(root, key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", &archive.OperationalError{Code: "local_write_failed", Cause: err}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", &archive.OperationalError{Code: "local_read_failed", Cause: readErr}
		}
		if !bytes.Equal(existing, data) {
			return "", &conflictError{code: "local_object_collision"}
		}
		return path, nil
	}
	if err != nil {
		return "", &archive.OperationalError{Code: "local_write_failed", Cause: err}
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return "", &archive.OperationalError{Code: "local_write_failed", Cause: err}
	}
	if err := file.Sync(); err != nil {
		return "", &archive.OperationalError{Code: "local_write_failed", Cause: err}
	}
	return path, nil
}

type runtimeDependencies struct {
	dependencies dependencies
	pool         *pgxpool.Pool
}

func (runtime runtimeDependencies) close() {
	if runtime.pool != nil {
		runtime.pool.Close()
	}
}

func dependenciesForCommand(
	ctx context.Context, command string, getenv func(string) string,
) (runtimeDependencies, error) {
	deps := dependencies{
		localRoot:       strings.TrimSpace(getenv("XM_AUDIT_ARCHIVE_LOCAL_ROOT")),
		exporterVersion: "audit-archive-v1", exporterCommit: strings.TrimSpace(getenv("XM_BUILD_COMMIT")),
		now: time.Now,
	}
	if deps.exporterCommit == "" {
		deps.exporterCommit = "development"
	}
	if path := strings.TrimSpace(getenv("XM_AUDIT_ARCHIVE_ROOT_KEYRING_FILE")); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return runtimeDependencies{}, &configError{code: "root_keyring_unavailable"}
		}
		deps.rootKeyring, err = archive.LoadStaticKeyringJSON(raw)
		if err != nil {
			return runtimeDependencies{}, &configError{code: "root_keyring_invalid"}
		}
	}
	if path := strings.TrimSpace(getenv("XM_AUDIT_ARCHIVE_MANIFEST_KEYRING_FILE")); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return runtimeDependencies{}, &configError{code: "manifest_keyring_unavailable"}
		}
		deps.manifestKeyring, err = archive.LoadStaticKeyringJSON(raw)
		if err != nil {
			return runtimeDependencies{}, &configError{code: "manifest_keyring_invalid"}
		}
	}
	if err := archive.ValidateTrustMaterialSeparation(nil, deps.rootKeyring, deps.manifestKeyring); err != nil {
		return runtimeDependencies{}, &configError{code: "trust_material_reused"}
	}
	if command == "verify-local" {
		return runtimeDependencies{dependencies: deps}, nil
	}
	if command != "plan" && command != "export-local" {
		return runtimeDependencies{dependencies: deps}, nil
	}
	environment := strings.TrimSpace(getenv("ENVIRONMENT"))
	if environment == "" {
		return runtimeDependencies{}, &configError{code: "environment_required"}
	}
	if environment != "development" && environment != "staging" && environment != "production" {
		return runtimeDependencies{}, &configError{code: "environment_invalid"}
	}
	databaseURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if databaseURL == "" {
		return runtimeDependencies{}, &configError{code: "database_required"}
	}
	if err := pgdsn.Validate(databaseURL, environment != "development"); err != nil {
		return runtimeDependencies{}, &configError{code: "database_config_invalid"}
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return runtimeDependencies{}, &configError{code: "database_config_invalid"}
	}
	deps.environment = environment
	deps.source = &storeSource{store: audit.NewStore(pool)}
	if command == "plan" {
		return runtimeDependencies{dependencies: deps, pool: pool}, nil
	}
	refText := strings.TrimSpace(getenv("XM_AUDIT_ARCHIVE_MANIFEST_CREDENTIAL_REF"))
	keyID := strings.TrimSpace(getenv("XM_AUDIT_ARCHIVE_MANIFEST_KEY_ID"))
	if refText == "" || keyID == "" {
		pool.Close()
		return runtimeDependencies{}, &configError{code: "manifest_signer_config_invalid"}
	}
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		pool.Close()
		return runtimeDependencies{}, &configError{code: "manifest_signer_config_invalid"}
	}
	provider, err := secrets.NewEnvProvider(map[string]string{ref.String(): "XM_AUDIT_ARCHIVE_MANIFEST_SEED"},
		secrets.WithLookup(func(name string) (string, bool) {
			value := getenv(name)
			return value, value != ""
		}))
	if err != nil {
		pool.Close()
		return runtimeDependencies{}, &configError{code: "manifest_signer_config_invalid"}
	}
	baseSigner, err := audit.SignerFromSecret(ctx, provider, ref, keyID)
	if err != nil {
		pool.Close()
		return runtimeDependencies{}, &configError{code: "manifest_signer_unavailable"}
	}
	deps.manifestSigner, err = archive.WrapPurposeSigner(baseSigner,
		archive.PurposeManifest, archive.ProtocolManifestV1)
	if err != nil {
		pool.Close()
		return runtimeDependencies{}, &configError{code: "manifest_signer_config_invalid"}
	}
	if err := archive.ValidateTrustMaterialSeparation(
		[]archive.PurposeSigner{deps.manifestSigner}, deps.rootKeyring, deps.manifestKeyring,
	); err != nil {
		pool.Close()
		return runtimeDependencies{}, &configError{code: "trust_material_reused"}
	}
	return runtimeDependencies{dependencies: deps, pool: pool}, nil
}

type storeSource struct{ store *audit.Store }

func (source *storeSource) Tip(ctx context.Context) (int64, string, error) {
	return source.store.Tip(ctx)
}

func (source *storeSource) List(ctx context.Context, from, to int64) ([]audit.Event, error) {
	return source.store.List(ctx, from, to)
}

func (source *storeSource) VerifyChain(ctx context.Context, from, to int64) (*audit.ChainProblem, error) {
	return source.store.VerifyChain(ctx, from, to)
}

func (source *storeSource) LatestRoot(ctx context.Context) (audit.ChainRoot, error) {
	return source.store.LatestRoot(ctx)
}
