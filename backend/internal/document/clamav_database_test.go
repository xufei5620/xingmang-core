package document

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckClamAVDatabaseFreshness(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	for _, name := range []string{"main.cvd", "daily.cvd", "freshclam.dat"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("signature"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckClamAVDatabaseFreshness(root, 48*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	scanner := ClamAVDatabaseScanner{Root: root, MaxUpdateAge: 48 * time.Hour, Now: func() time.Time { return now }}
	if err := scanner.Scan(context.Background(), "ignored.pdf"); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "daily.cvd")
	if err := os.Chtimes(stale, now.Add(-72*time.Hour), now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := CheckClamAVDatabaseFreshness(root, 48*time.Hour, now); err == nil {
		t.Fatal("stale ClamAV daily database was accepted")
	}
}

func TestCheckClamAVDatabaseFreshnessRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("signature"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "main.cvd")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := CheckClamAVDatabaseFreshness(root, time.Hour, time.Now()); err == nil {
		t.Fatal("symlinked ClamAV database was accepted")
	}
}
