package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shredFixture(t *testing.T) (string, string, fixtureGuard) {
	t.Helper()
	dir := t.TempDir()
	g := testGuard()
	body, _ := json.Marshal(g)
	guard := filepath.Join(dir, "provider-guard.json")
	if err := os.WriteFile(guard, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staff-password"), []byte("synthetic-no-real-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, guard, g
}
func TestShredRejectsUnknownBeforeAnyWrite(t *testing.T) {
	dir, guard, g := shredFixture(t)
	known := filepath.Join(dir, "staff-password")
	before, _ := os.ReadFile(known)
	if err := os.WriteFile(filepath.Join(dir, "unknown.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := shredFixtures(dir, g, guard); err == nil {
		t.Fatal("unknown file accepted")
	}
	after, _ := os.ReadFile(known)
	if !bytes.Equal(before, after) {
		t.Fatal("changed known file before full inventory passed")
	}
}
func TestShredRejectsOwnerMismatch(t *testing.T) {
	dir, guard, g := shredFixture(t)
	g.Owner = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := shredFixtures(dir, g, guard); err == nil {
		t.Fatal("different owner accepted")
	}
	if _, err := os.Stat(guard); err != nil {
		t.Fatal("guard lost on refusal")
	}
}
func TestShredDeletesOnlyAllowlistAndKeepsMount(t *testing.T) {
	dir, guard, g := shredFixture(t)
	if err := os.Mkdir(filepath.Join(dir, "staff-secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	staffID := strings.Join([]string{"12345678", "1234", "4234", "8234", "123456789012"}, "-")
	if err := os.WriteFile(filepath.Join(dir, "staff-secrets", "staff-totp__"+staffID), []byte("SYNTHETIC"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := shredFixtures(dir, g, guard); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("owned mount should exist and be empty")
	}
}
func TestShredRejectsLinkAndOutsideGuard(t *testing.T) {
	dir, guard, g := shredFixture(t)
	outside := t.TempDir()
	bad := filepath.Join(outside, "outside-guard.json")
	b, _ := json.Marshal(g)
	if err := os.WriteFile(bad, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := shredFixtures(dir, g, bad); err == nil {
		t.Fatal("outside guard accepted")
	}
	target := filepath.Join(outside, "outside-password")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "newapi-password")
	if err := os.Symlink(target, link); err != nil {
		t.Log("OS declined symlink creation; outside guard still exercised")
		return
	}
	if err := shredFixtures(dir, g, guard); err == nil {
		t.Fatal("symlink accepted")
	}
	after, _ := os.ReadFile(target)
	if string(after) != "outside" {
		t.Fatal("outside link target changed")
	}
}

type wipeRecorder struct {
	writes [][]byte
	syncs  int
}

func (r *wipeRecorder) WriteAt(p []byte, off int64) (int, error) {
	if off != 0 {
		panic("unexpected offset")
	}
	r.writes = append(r.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (r *wipeRecorder) Sync() error { r.syncs++; return nil }
func TestWipeThreeRandomThenZeroSynced(t *testing.T) {
	r := &wipeRecorder{}
	entropy := bytes.NewReader(append(append(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)...), bytes.Repeat([]byte{3}, 32)...))
	if err := wipeFixtureFile(r, 32, entropy); err != nil {
		t.Fatal(err)
	}
	if len(r.writes) != 4 || r.syncs != 4 {
		t.Fatalf("wanted four synced passes, got %d/%d", len(r.writes), r.syncs)
	}
	for n := 0; n < 4; n++ {
		want := byte(n + 1)
		if n == 3 {
			want = 0
		}
		if !bytes.Equal(r.writes[n], bytes.Repeat([]byte{want}, 32)) {
			t.Fatalf("wrong wipe pass %d", n)
		}
	}
}

func TestShredAcceptsSeedGuard(t *testing.T) {
	dir, guard, g := shredFixture(t)
	seedGuard := filepath.Join(dir, "seed-guard.json")
	if err := os.Rename(guard, seedGuard); err != nil {
		t.Fatal(err)
	}
	if err := shredFixtures(dir, g, seedGuard); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("owned mount should remain empty")
	}
}

func TestShredNestedTOTPMatchesPlatformReaderAndKeepsMount(t *testing.T) {
	dir, guard, g := shredFixture(t)
	nested := filepath.Join(dir, "staff-secrets", "staff-totp")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "12345678-1234-4234-8234-123456789012"), []byte("SYNTHETIC"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := shredFixtures(dir, g, guard); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("nested TOTP directories must be removed and the mount retained")
	}
}
