package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// fakeSecretProvider hands back a fixed in-memory value - these tests never
// touch the real filesystem-backed credential store (that path has its own
// focused tests below) and never touch the network beyond an httptest
// loopback server.
type fakeSecretProvider struct{ value string }

func (f fakeSecretProvider) Resolve(context.Context, secrets.CredentialRef, string) (secrets.SecretValue, error) {
	return secrets.NewSecretValue([]byte(f.value)), nil
}
func (f fakeSecretProvider) Metadata(_ context.Context, ref secrets.CredentialRef) (secrets.SecretMetadata, error) {
	return secrets.SecretMetadata{Ref: ref, Available: true}, nil
}

type failingSecretProvider struct{ err error }

func (f failingSecretProvider) Resolve(context.Context, secrets.CredentialRef, string) (secrets.SecretValue, error) {
	return secrets.SecretValue{}, f.err
}
func (f failingSecretProvider) Metadata(_ context.Context, ref secrets.CredentialRef) (secrets.SecretMetadata, error) {
	return secrets.SecretMetadata{Ref: ref}, nil
}

func fixedDeps(t *testing.T, tlsServer *httptest.Server, provider secrets.SecretProvider) captureDeps {
	t.Helper()
	fixedNow := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	return captureDeps{
		getenv:        func(string) string { return "" },
		now:           func() time.Time { return fixedNow },
		baseTransport: tlsServer.Client().Transport,
		secretsFor:    func(string, string) (secrets.SecretProvider, error) { return provider, nil },
		randSalt:      func() ([]byte, error) { return []byte("deterministic-test-salt"), nil },
	}
}

func sub2apiFixtureServer(t *testing.T, statusOverride int) *httptest.Server {
	t.Helper()
	versionBody := readTestdata(t, "sub2api", "version.sample.json")
	pageBody := readTestdata(t, "sub2api", "users_page.sample.json")
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusOverride != 0 {
			w.WriteHeader(statusOverride)
			return
		}
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case sub2apiRouteVersion:
			_, _ = w.Write(versionBody)
		case sub2apiRouteUsers:
			_, _ = w.Write(pageBody)
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
		}
	}))
}

func TestRunCaptureSub2APIEndToEndWritesRedactedEvidence(t *testing.T) {
	server := sub2apiFixtureServer(t, 0)
	defer server.Close()

	outRoot := t.TempDir()
	deps := fixedDeps(t, server, fakeSecretProvider{value: "test-token-value"})

	var stdout, stderr bytes.Buffer
	args := []string{
		"--platform", "sub2api",
		"--endpoint", server.URL,
		"--credential-ref", "secret://sub2api-prod/read-token",
		"--out", outRoot,
		"--sample-size", "5",
	}
	code := runCapture(args, &stdout, &stderr, deps)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitOK, stdout.String(), stderr.String())
	}

	outDir := filepath.Join(outRoot, timestampDir(time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)))
	for _, name := range []string{"users_page.redacted.json", "user_detail.redacted.json", "version.json", "request-log.json", "SHA256SUMS", "README.md"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("expected file %s: %v", name, err)
		}
	}

	pageBytes, err := os.ReadFile(filepath.Join(outDir, "users_page.redacted.json"))
	if err != nil {
		t.Fatal(err)
	}
	pageText := string(pageBytes)
	for _, mustNotContain := range []string{"u_10241", "test-user-one@example.invalid", "test-token-value"} {
		if strings.Contains(pageText, mustNotContain) {
			t.Errorf("evidence file leaked %q", mustNotContain)
		}
	}

	versionBytes, err := os.ReadFile(filepath.Join(outDir, "version.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(versionBytes), "0.1.183") {
		t.Errorf("version.json missing the captured version string: %s", versionBytes)
	}

	// SHA256SUMS must actually match what is on disk.
	sumsBytes, err := os.ReadFile(filepath.Join(outDir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(sumsBytes)), "\n") {
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			t.Fatalf("malformed SHA256SUMS line: %q", line)
		}
		wantSum, name := fields[0], fields[1]
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("SHA256SUMS references missing file %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != wantSum {
			t.Errorf("sha256 mismatch for %s: SHA256SUMS says %s, actual is %s", name, wantSum, got)
		}
	}

	readme, err := os.ReadFile(filepath.Join(outDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "SUB2_REAL_APPROVAL") {
		t.Error("README does not name the approval event this evidence feeds")
	}
	if strings.Contains(string(readme), "test-token-value") {
		t.Fatal("README leaked the resolved plaintext credential")
	}
}

func TestRunCaptureComplianceBlockedStillWritesPartialEvidence(t *testing.T) {
	server := sub2apiFixtureServer(t, http.StatusLocked)
	defer server.Close()

	outRoot := t.TempDir()
	deps := fixedDeps(t, server, fakeSecretProvider{value: "test-token-value"})
	var stdout, stderr bytes.Buffer
	args := []string{
		"--platform", "sub2api",
		"--endpoint", server.URL,
		"--credential-ref", "secret://sub2api-prod/read-token",
		"--out", outRoot,
	}
	code := runCapture(args, &stdout, &stderr, deps)
	if code != exitComplianceBlocked {
		t.Fatalf("exit code = %d, want exitComplianceBlocked (%d)", code, exitComplianceBlocked)
	}

	outDir := filepath.Join(outRoot, timestampDir(time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)))
	readme, err := os.ReadFile(filepath.Join(outDir, "README.md"))
	if err != nil {
		t.Fatalf("expected a README even for a blocked run: %v", err)
	}
	if !strings.Contains(string(readme), "AdminComplianceGuard") {
		t.Error("README does not explain the AdminComplianceGuard block")
	}
	// No users_page.redacted.json - both probes 423'd, nothing to redact.
	if _, err := os.Stat(filepath.Join(outDir, "users_page.redacted.json")); err == nil {
		t.Error("users_page.redacted.json should not exist when the probe never got a body")
	}
}

func TestRunCaptureCredentialResolutionFailureWritesNothing(t *testing.T) {
	server := sub2apiFixtureServer(t, 0)
	defer server.Close()

	outRoot := t.TempDir()
	deps := fixedDeps(t, server, failingSecretProvider{err: secrets.ErrNotFound})
	var stdout, stderr bytes.Buffer
	args := []string{
		"--platform", "sub2api",
		"--endpoint", server.URL,
		"--credential-ref", "secret://sub2api-prod/read-token",
		"--out", outRoot,
	}
	code := runCapture(args, &stdout, &stderr, deps)
	if code != exitFailed {
		t.Fatalf("exit code = %d, want exitFailed (%d)", code, exitFailed)
	}
	entries, _ := os.ReadDir(outRoot)
	if len(entries) != 0 {
		t.Errorf("expected no evidence directory when credential resolution fails, found %v", entries)
	}
}

func TestRunCaptureUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing platform", []string{"--endpoint", "https://x.example", "--credential-ref", "secret://a/b"}},
		{"unknown platform", []string{"--platform", "bogus"}},
		{"missing endpoint", []string{"--platform", "sub2api", "--credential-ref", "secret://a/b"}},
		{"http not https", []string{"--platform", "sub2api", "--endpoint", "http://x.example", "--credential-ref", "secret://a/b"}},
		{"missing credential ref", []string{"--platform", "sub2api", "--endpoint", "https://x.example"}},
		{"bad credential ref", []string{"--platform", "sub2api", "--endpoint", "https://x.example", "--credential-ref", "not-a-ref"}},
		{"sample size too large", []string{"--platform", "sub2api", "--sample-size", "9999"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCapture(c.args, &stdout, &stderr, captureDeps{})
			if code != exitUsage {
				t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
			}
		})
	}
}

func TestRunCaptureDryRunNeedsNoFlagsBeyondPlatform(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCapture([]string{"--platform", "newapi", "--dry-run"}, &stdout, &stderr, captureDeps{})
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, newapiRouteStatus) || !strings.Contains(out, newapiRouteUsers) {
		t.Errorf("dry-run output missing expected routes:\n%s", out)
	}
	if !strings.Contains(out, "no network access") {
		t.Errorf("dry-run output should say plainly that it makes no network access:\n%s", out)
	}
}

func TestResolveSecretRootDefaultsAndValidation(t *testing.T) {
	root, err := resolveSecretRoot("", func(string) string { return "" })
	if err != nil || root != defaultSecretRoot {
		t.Fatalf("resolveSecretRoot empty = (%q,%v), want (%q,nil)", root, err, defaultSecretRoot)
	}

	root, err = resolveSecretRoot("", func(k string) string {
		if k == secretRootEnvVar {
			return "/custom/root"
		}
		return ""
	})
	if err != nil || root != "/custom/root" {
		t.Fatalf("resolveSecretRoot env fallback = (%q,%v), want (/custom/root,nil)", root, err)
	}

	if _, err := resolveSecretRoot("relative/path", func(string) string { return "" }); err == nil {
		t.Error("resolveSecretRoot accepted a relative path")
	}
}

func TestDefaultSecretsProviderResolvesFromFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub2api-prod"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub2api-prod", "read-token"), []byte("file-backed-token\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	provider, err := defaultSecretsProvider(root, "test")
	if err != nil {
		t.Fatalf("defaultSecretsProvider: %v", err)
	}
	ref, err := secrets.ParseCredentialRef("secret://sub2api-prod/read-token")
	if err != nil {
		t.Fatal(err)
	}
	v, err := provider.Resolve(context.Background(), ref, "test")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v.Reveal() != "file-backed-token" {
		t.Fatalf("Resolve() = %q, want file-backed-token", v.Reveal())
	}
}
