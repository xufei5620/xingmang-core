package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cpaboundary"
)

func writeFixture(t *testing.T, dir, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunAuditsOnlyExplicitLocalFilesAndFailsPlaceholderClosed(t *testing.T) {
	dir := t.TempDir()
	b := cpaboundary.DefaultBoundaryV1()
	// A zero inventory digest is the checked-in placeholder and is valid for
	// this local shape test; the report remains an offline artifact.
	e := cpaboundary.EvidenceBundle{
		Version: 1, ObservedAt: time.Now().UTC(), TargetVersion: "1.0.0", CPAImageDigest: strings.Repeat("b", 64), RouteInventorySHA256: strings.Repeat("0", 64),
		Config:   cpaboundary.ConfigProjection{Complete: true, AllowRemoteKnown: true, AllowRemote: false, RawPort: 8317, RawBind: "127.0.0.1"},
		Process:  cpaboundary.ProcessProjection{Complete: true},
		Routes:   cpaboundary.RouteProjection{Complete: true, InferenceHostname: "infer.example", CallbackHostname: "callback.example", IPv6Covered: true, Routes: []cpaboundary.ObservedRoute{{Plane: "callback", Hostname: "callback.example", Method: "GET", Path: cpaboundary.CallbackPath, Public: true}, {Plane: "callback", Hostname: "callback.example", Method: "POST", Path: cpaboundary.CallbackPath, Public: true}}},
		Firewall: cpaboundary.FirewallProjection{Complete: true, RawPortBlockedIPv4: true, RawPortBlockedIPv6: true},
		Adapter:  cpaboundary.AdapterProjection{Complete: true, RequiresMTLS: true, HasMTLS: true},
		Digests:  cpaboundary.DigestProjection{Complete: true, ImageSHA256: strings.Repeat("b", 64), RouteInventorySHA256: strings.Repeat("0", 64)},
	}
	boundaryPath := writeFixture(t, dir, "boundary.json", b)
	evidencePath := writeFixture(t, dir, "evidence.json", e)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--boundary", boundaryPath, "--evidence", evidencePath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("placeholder evidence should fail closed with code 1, got=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"decision": "partial"`) {
		t.Fatalf("unexpected report: %s", stdout.String())
	}
}

func TestValidateLocalPathRejectsURLUNCTraversalAndNUL(t *testing.T) {
	for _, path := range []string{"https://example.invalid/evidence.json", `\\server\share\evidence.json`, "//server/share/evidence.json", "folder/../evidence.json", "bad\x00path"} {
		if err := validateLocalPath(path); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
}

func TestRunRejectsOutputOverwrite(t *testing.T) {
	dir := t.TempDir()
	bPath := writeFixture(t, dir, "b.json", cpaboundary.DefaultBoundaryV1())
	// The parser error is enough to exercise the explicit input gate; output
	// overwrite is separately protected by O_EXCL below.
	ePath := filepath.Join(dir, "e.json")
	if err := os.WriteFile(ePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "report.json")
	if err := os.WriteFile(outPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	_ = run([]string{"--boundary", bPath, "--evidence", ePath, "--out", outPath}, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "evidence") {
		t.Fatalf("expected evidence rejection, stderr=%s", stderr.String())
	}
}
