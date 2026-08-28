package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

func exportFixtureRoot(t *testing.T) audit.ChainRoot {
	t.Helper()
	raw, err := os.ReadFile("testdata/chain-root-ref-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := DecodeChainRootRefV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	computedAt, err := ParseWireTime(wire.ComputedAt)
	if err != nil {
		t.Fatal(err)
	}
	return audit.ChainRoot{
		ID: uuid.MustParse(wire.ID), ComputedAt: computedAt,
		FromSequence: wire.FromSequence, ToSequence: wire.ToSequence,
		RootHash: wire.RootHash, Signature: wire.Signature, KeyID: wire.KeyID,
	}
}

func TestExportRootPerformsZeroDatabaseWritesOnSuccessAndFailure(t *testing.T) {
	root := exportFixtureRoot(t)
	want := root
	dir := t.TempDir()
	path, err := ExportRoot(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(root, want) {
		t.Fatalf("success mutated legacy marker state: before=%+v after=%+v", want, root)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("export escaped target directory: %s", path)
	}
	literal, err := os.ReadFile("testdata/chain-root-ref-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, literal) {
		t.Fatalf("export bytes differ from ChainRootRefV1\nwant=%s\n got=%s", literal, got)
	}

	root.ExportedAt = func() *time.Time { value := time.Unix(1, 0).UTC(); return &value }()
	root.ExportTarget = "legacy-marker"
	want = root
	if _, err := ExportRoot(root, ""); err == nil {
		t.Fatal("empty directory was accepted")
	}
	if !reflect.DeepEqual(root, want) {
		t.Fatalf("failure mutated legacy marker state: before=%+v after=%+v", want, root)
	}
}

func TestExportRootRetryDoesNotMutateLegacyMarker(t *testing.T) {
	root := exportFixtureRoot(t)
	dir := t.TempDir()
	firstPath, err := ExportRoot(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := ExportRoot(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstPath != secondPath || !bytes.Equal(first, second) {
		t.Fatalf("retry produced a different root artifact: %s vs %s", firstPath, secondPath)
	}
	if root.ExportedAt != nil || root.ExportTarget != "" {
		t.Fatalf("retry wrote legacy marker fields: %+v", root)
	}
}
