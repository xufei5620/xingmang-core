package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	archive "github.com/xufei5620/xingmang-platform/internal/platform/audit/archive"
)

type fakeSource struct {
	events  []audit.Event
	root    audit.ChainRoot
	problem *audit.ChainProblem
	err     error
}

func (source *fakeSource) Tip(context.Context) (int64, string, error) {
	if source.err != nil {
		return 0, "", source.err
	}
	if len(source.events) == 0 {
		return 0, audit.GenesisHash, nil
	}
	last := source.events[len(source.events)-1]
	return last.Sequence, last.EventHash, nil
}

func (source *fakeSource) List(_ context.Context, from, to int64) ([]audit.Event, error) {
	if source.err != nil {
		return nil, source.err
	}
	result := make([]audit.Event, 0)
	for _, event := range source.events {
		if event.Sequence >= from && event.Sequence <= to {
			result = append(result, event)
		}
	}
	return result, nil
}

func (source *fakeSource) VerifyChain(context.Context, int64, int64) (*audit.ChainProblem, error) {
	return source.problem, source.err
}

func (source *fakeSource) LatestRoot(context.Context) (audit.ChainRoot, error) {
	return source.root, source.err
}

func cliFixture(t *testing.T) (*fakeSource, archive.Keyring, archive.Keyring, archive.PurposeSigner) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "audit", "archive", "testdata", "mixed-v1-v2.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := archive.DecodePayload(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "audit", "archive", "testdata", "manifest-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := archive.DecodeSignedManifestV1(manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	rootTime, err := archive.ParseWireTime(manifest.Unsigned.ChainRoot.ComputedAt)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{events: events, root: audit.ChainRoot{
		ID: mustUUID(t, manifest.Unsigned.ChainRoot.ID), ComputedAt: rootTime,
		FromSequence: manifest.Unsigned.ChainRoot.FromSequence,
		ToSequence:   manifest.Unsigned.ChainRoot.ToSequence,
		RootHash:     manifest.Unsigned.ChainRoot.RootHash,
		Signature:    manifest.Unsigned.ChainRoot.Signature,
		KeyID:        manifest.Unsigned.ChainRoot.KeyID,
	}}
	rootRaw, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "audit", "archive", "testdata", "root-keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	rootKeys, err := archive.LoadStaticKeyringJSON(rootRaw)
	if err != nil {
		t.Fatal(err)
	}
	manifestKeysRaw, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "audit", "archive", "testdata", "manifest-keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestKeys, err := archive.LoadStaticKeyringJSON(manifestKeysRaw)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := archive.NewEd25519PurposeSigner("manifest-key-1", bytes.Repeat([]byte{0x22}, 32),
		archive.PurposeManifest, archive.ProtocolManifestV1)
	if err != nil {
		t.Fatal(err)
	}
	return source, rootKeys, manifestKeys, signer
}

func mustUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	value, err := uuid.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testDependencies(t *testing.T) dependencies {
	t.Helper()
	source, rootKeys, manifestKeys, signer := cliFixture(t)
	return dependencies{
		source: source, environment: "development", localRoot: t.TempDir(),
		rootKeyring: rootKeys, manifestKeyring: manifestKeys, manifestSigner: signer,
		exporterVersion: "test", exporterCommit: "543087fab9f8f1dee3a3b3412681e1b49a1ca7f5",
	}
}

func TestCLIPlanDoesNotWriteFiles(t *testing.T) {
	deps := testDependencies(t)
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"plan", "--from", "1", "--to", "2"}, deps, &stdout, &stderr)
	if exitCode != exitOK {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	entries, err := os.ReadDir(deps.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("plan wrote files: %v", entries)
	}
	if !strings.Contains(stdout.String(), `"from_sequence":1`) || !strings.Contains(stdout.String(), `"row_count":2`) {
		t.Fatalf("plan output = %s", stdout.String())
	}
}

func TestCLIExportLocalRejectsProduction(t *testing.T) {
	deps := testDependencies(t)
	deps.environment = "production"
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"export-local", "--to", "2", "--root-id", deps.source.(*fakeSource).root.ID.String()}, deps, &stdout, &stderr)
	if exitCode != exitConfig {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	entries, err := os.ReadDir(deps.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("production rejection wrote files: %v", entries)
	}
}

func TestCLIExportLocalRejectsUnknownEnvironment(t *testing.T) {
	deps := testDependencies(t)
	deps.environment = "unknown"
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{
		"export-local", "--to", "2", "--root-id", deps.source.(*fakeSource).root.ID.String(),
	}, deps, &stdout, &stderr); exitCode != exitConfig {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	entries, err := os.ReadDir(deps.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unknown environment caused writes: %v", entries)
	}
}

func TestCLIExportLocalRejectsPartialFirstSegmentAndFromFlag(t *testing.T) {
	for name, test := range map[string]struct {
		configure func(*dependencies, *[]string)
		want      int
	}{
		"from-flag": {func(_ *dependencies, args *[]string) { *args = append(*args, "--from", "2") }, exitConfig},
		"partial-source": {func(deps *dependencies, _ *[]string) {
			source := deps.source.(*fakeSource)
			source.events = source.events[1:]
		}, exitIntegrity},
	} {
		t.Run(name, func(t *testing.T) {
			deps := testDependencies(t)
			args := []string{"export-local", "--to", "2", "--root-id", deps.source.(*fakeSource).root.ID.String()}
			test.configure(&deps, &args)
			var stdout, stderr bytes.Buffer
			if exitCode := run(context.Background(), args, deps, &stdout, &stderr); exitCode != test.want {
				t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
			}
		})
	}
}

func TestCLIExportLocalRejectsUntrustedRootBeforeWritingFiles(t *testing.T) {
	deps := testDependencies(t)
	source := deps.source.(*fakeSource)
	source.root.Signature = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, ed25519.SignatureSize))
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"export-local", "--to", "2", "--root-id", source.root.ID.String(),
	}, deps, &stdout, &stderr)
	if exitCode != exitIntegrity {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	entries, err := os.ReadDir(deps.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("untrusted root caused writes before verification: %v", entries)
	}
}

func TestCLIExportLocalTreatsRootHashMismatchAsIntegrityFailure(t *testing.T) {
	deps := testDependencies(t)
	source := deps.source.(*fakeSource)
	source.root.RootHash = strings.Repeat("f", 64)
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"export-local", "--to", "2", "--root-id", source.root.ID.String(),
	}, deps, &stdout, &stderr)
	if exitCode != exitIntegrity {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	entries, err := os.ReadDir(deps.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("root mismatch caused writes before integrity rejection: %v", entries)
	}
}

func TestCLIExportLocalCreatesImmediatelyVerifiableArtifact(t *testing.T) {
	deps := testDependencies(t)
	rootID := deps.source.(*fakeSource).root.ID.String()
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{
		"export-local", "--to", "2", "--root-id", rootID,
	}, deps, &stdout, &stderr); exitCode != exitOK {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	var result struct {
		ManifestPath string `json:"manifest_path"`
		ManifestSHA  string `json:"manifest_sha256"`
		Rows         int64  `json:"rows"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Rows != 2 || len(result.ManifestSHA) != 64 {
		t.Fatalf("export result = %+v", result)
	}
	manifestAbsolute, err := filepath.Abs(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	rootAbsolute, err := filepath.Abs(deps.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(manifestAbsolute, rootAbsolute+string(os.PathSeparator)) {
		t.Fatalf("manifest escaped local root: %s", manifestAbsolute)
	}
	report, err := defaultVerifyLocal(context.Background(), result.ManifestPath, deps)
	if err != nil {
		t.Fatal(err)
	}
	if report.Code != archive.VerificationOK || report.VerifiedRows != 2 ||
		report.CaptureCompleteness != "not_checked" {
		t.Fatalf("verification report = %+v", report)
	}
}

func TestCLIExportLocalRecordsActualObservationTimeInsteadOfRootTime(t *testing.T) {
	deps := testDependencies(t)
	observedAt := time.Date(2026, 8, 29, 2, 30, 0, 654321000, time.UTC)
	deps.now = func() time.Time { return observedAt }
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{
		"export-local", "--to", "2", "--root-id", deps.source.(*fakeSource).root.ID.String(),
	}, deps, &stdout, &stderr); exitCode != exitOK {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	var result struct {
		ManifestPath string `json:"manifest_path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := archive.DecodeSignedManifestV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := archive.NewWireTime(observedAt)
	if manifest.Unsigned.SourceTipObservedAt != want || manifest.Unsigned.CreatedAt != want ||
		manifest.Unsigned.ActionRunSnapshotAt != want {
		t.Fatalf("observation times = %+v want %s", manifest.Unsigned, want)
	}
	if manifest.Unsigned.SourceTipObservedAt == archive.NewWireTime(deps.source.(*fakeSource).root.ComputedAt) {
		t.Fatal("source observation time was fabricated from Chain Root creation time")
	}
}

func TestCLIVerifyWalksExactPreviousRefsFromTerminalToGenesis(t *testing.T) {
	deps := testDependencies(t)
	laterPath := writeTwoManifestChain(t, deps)
	chain, err := loadLocalManifestChain(laterPath, deps.localRoot, deps.manifestKeyring)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0].Unsigned.FromSequence != 1 || chain[1].Unsigned.FromSequence != 3 {
		t.Fatalf("chain = %+v", chain)
	}
}

func writeTwoManifestChain(t *testing.T, deps dependencies) string {
	t.Helper()
	firstRaw, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "audit", "archive", "testdata", "manifest-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := archive.DecodeSignedManifestV1(firstRaw)
	if err != nil {
		t.Fatal(err)
	}
	firstSum := sha256.Sum256(firstRaw)
	firstDigest := hex.EncodeToString(firstSum[:])
	firstKey := filepath.ToSlash(filepath.Join("audit", "v1", "manifest",
		"seq-0000000000000000001-0000000000000000002-"+firstDigest+".json"))
	firstPath := filepath.Join(deps.localRoot, filepath.FromSlash(firstKey))
	if err := os.MkdirAll(filepath.Dir(firstPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstPath, firstRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	later := first.Unsigned
	later.FromSequence = 3
	later.ToSequence = 4
	const oldRange = "seq-0000000000000000001-0000000000000000002-"
	const newRange = "seq-0000000000000000003-0000000000000000004-"
	later.Payload.Key = strings.Replace(later.Payload.Key, oldRange, newRange, 1)
	later.Projections = append([]archive.ProjectionRefV1(nil), later.Projections...)
	for index := range later.Projections {
		later.Projections[index].Object.Key = strings.Replace(
			later.Projections[index].Object.Key, oldRange, newRange, 1)
	}
	later.PreviousManifest = archive.PreviousManifestRefV1{
		Kind: "object", BucketID: "local-fixture", Key: firstKey, VersionID: "local-v1",
		SHA256: firstDigest,
	}
	signedLater, err := archive.SignManifest(later, deps.manifestSigner)
	if err != nil {
		t.Fatal(err)
	}
	laterRaw, err := archive.EncodeSignedManifestV1(signedLater)
	if err != nil {
		t.Fatal(err)
	}
	laterPath := filepath.Join(deps.localRoot, "terminal.json")
	if err := os.WriteFile(laterPath, laterRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatal("exact previous path was not used")
	}
	return laterPath
}

func TestCLIVerifyRejectsUntrustedTerminalBeforeFollowingPrevious(t *testing.T) {
	deps := testDependencies(t)
	laterPath := writeTwoManifestChain(t, deps)
	raw, err := os.ReadFile(laterPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := archive.DecodeSignedManifestV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, ed25519.SignatureSize))
	raw, err = archive.EncodeSignedManifestV1(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(laterPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadLocalManifestChain(laterPath, deps.localRoot, deps.manifestKeyring)
	var integrity *archive.IntegrityError
	if !errors.As(err, &integrity) || integrity.Report.Code != archive.VerificationManifestSignature {
		t.Fatalf("error = %#v, want terminal signature rejection", err)
	}
}

func TestVerifyLocalRuntimeDoesNotRequireDatabaseURL(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "platform", "audit", "archive", "testdata")
	values := map[string]string{
		"XM_AUDIT_ARCHIVE_LOCAL_ROOT":            t.TempDir(),
		"XM_AUDIT_ARCHIVE_ROOT_KEYRING_FILE":     filepath.Join(root, "root-keyring.json"),
		"XM_AUDIT_ARCHIVE_MANIFEST_KEYRING_FILE": filepath.Join(root, "manifest-keyring.json"),
	}
	runtime, err := dependenciesForCommand(context.Background(), "verify-local", func(name string) string {
		return values[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.close()
	if runtime.pool != nil || runtime.dependencies.source != nil {
		t.Fatal("offline verify-local opened a database dependency")
	}
}

func TestCLIVerifyMapsTamperToExitOneAndConfigToExitTwo(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want int
	}{
		"tamper": {&archive.IntegrityError{Report: archive.VerificationReport{Code: archive.VerificationBrokenLink}}, exitIntegrity},
		"config": {&configError{code: "invalid_config"}, exitConfig},
	} {
		t.Run(name, func(t *testing.T) {
			deps := testDependencies(t)
			deps.verifyLocal = func(context.Context, string, dependencies) (archive.VerificationReport, error) {
				return archive.VerificationReport{}, test.err
			}
			var stdout, stderr bytes.Buffer
			if got := run(context.Background(), []string{"verify-local", "--manifest", "terminal.json"}, deps, &stdout, &stderr); got != test.want {
				t.Fatalf("exit=%d want=%d stderr=%s", got, test.want, stderr.String())
			}
		})
	}
}

func TestCLIVerifyMapsMalformedArtifactToIntegrity(t *testing.T) {
	deps := testDependencies(t)
	manifestPath := filepath.Join(deps.localRoot, "malformed.json")
	if err := os.WriteFile(manifestPath, []byte("{\"unsigned\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{
		"verify-local", "--manifest", manifestPath,
	}, deps, &stdout, &stderr); exitCode != exitIntegrity {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
}

func TestCLIVerifyMapsOutdatedOperationalAndConflictSeparately(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want int
	}{
		"outdated":    {&archive.CompatibilityError{Code: archive.CompatibilityVerifierOutdated, FormatVersion: 2}, exitOutdated},
		"operational": {&archive.OperationalError{Code: "object_read_failed", Cause: context.DeadlineExceeded}, exitOperational},
		"conflict":    {&conflictError{code: "manifest_conflict"}, exitConflict},
	} {
		t.Run(name, func(t *testing.T) {
			deps := testDependencies(t)
			deps.verifyLocal = func(context.Context, string, dependencies) (archive.VerificationReport, error) {
				return archive.VerificationReport{}, test.err
			}
			var stdout, stderr bytes.Buffer
			if got := run(context.Background(), []string{"verify-local", "--manifest", "terminal.json"}, deps, &stdout, &stderr); got != test.want {
				t.Fatalf("exit=%d want=%d stderr=%s", got, test.want, stderr.String())
			}
		})
	}
}

func TestCLIOutputNeverContainsKeyMaterial(t *testing.T) {
	const marker = "private-seed-marker-never-print"
	deps := testDependencies(t)
	deps.verifyLocal = func(context.Context, string, dependencies) (archive.VerificationReport, error) {
		return archive.VerificationReport{}, &archive.OperationalError{Code: "object_read_failed", Cause: errors.New(marker)}
	}
	var stdout, stderr bytes.Buffer
	_ = run(context.Background(), []string{"verify-local", "--manifest", "terminal.json"}, deps, &stdout, &stderr)
	if strings.Contains(stdout.String(), marker) || strings.Contains(stderr.String(), marker) {
		t.Fatalf("CLI leaked key material: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCLIVerifySuccessOutputUsesStableSnakeCaseFields(t *testing.T) {
	deps := testDependencies(t)
	deps.verifyLocal = func(context.Context, string, dependencies) (archive.VerificationReport, error) {
		return archive.VerificationReport{
			Code: archive.VerificationOK, VerifiedRows: 2, VerifiedObjects: 3,
			ChainIntegrity: "verified", CaptureCompleteness: "not_checked", Caveats: []string{},
		}, nil
	}
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{
		"verify-local", "--manifest", "terminal.json",
	}, deps, &stdout, &stderr); exitCode != exitOK {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"code", "first_bad_sequence", "detail", "verified_rows", "verified_objects",
		"verified_root_hash", "chain_integrity", "capture_completeness",
		"canonical_version_counts", "caveats",
	} {
		if _, found := fields[key]; !found {
			t.Fatalf("missing stable field %q in %s", key, stdout.String())
		}
	}
	if _, found := fields["Code"]; found {
		t.Fatalf("Go field name leaked into CLI wire: %s", stdout.String())
	}
}
