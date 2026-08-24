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
	// freshclam.dat is implementation-owned updater state, not a signature
	// database. Neither an old file nor its absence may make current daily
	// signatures look stale.
	freshClamState := filepath.Join(root, "freshclam.dat")
	if err := os.Chtimes(freshClamState, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := CheckClamAVDatabaseFreshness(root, 48*time.Hour, now); err != nil {
		t.Fatalf("old FreshClam state rejected despite current daily signatures: %v", err)
	}
	if err := os.Remove(freshClamState); err != nil {
		t.Fatal(err)
	}
	if err := CheckClamAVDatabaseFreshness(root, 48*time.Hour, now); err != nil {
		t.Fatalf("missing FreshClam state rejected despite current daily signatures: %v", err)
	}
	// During an atomic CVD/CLD transition both names can exist. Freshness is
	// determined by the newest safe daily database, not filename preference.
	if err := os.Chtimes(filepath.Join(root, "daily.cvd"), now.Add(-72*time.Hour), now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	dailyCLD := filepath.Join(root, "daily.cld")
	if err := os.WriteFile(dailyCLD, []byte("signature"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dailyCLD, now, now); err != nil {
		t.Fatal(err)
	}
	if err := CheckClamAVDatabaseFreshness(root, 48*time.Hour, now); err != nil {
		t.Fatalf("new daily.cld did not supersede stale daily.cvd: %v", err)
	}
	scanner := ClamAVDatabaseScanner{Root: root, MaxUpdateAge: 48 * time.Hour, Now: func() time.Time { return now }}
	if err := scanner.Scan(context.Background(), "ignored.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dailyCLD, now.Add(-72*time.Hour), now.Add(-72*time.Hour)); err != nil {
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

func TestCheckClamAVDatabaseFreshnessRejectsUnsafeAlternateDailyFile(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	for _, name := range []string{"main.cvd", "daily.cvd"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("signature"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "alternate-daily")
	if err := os.WriteFile(target, []byte("signature"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "daily.cld")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := CheckClamAVDatabaseFreshness(root, 48*time.Hour, now); err == nil {
		t.Fatal("unsafe alternate daily database was ignored")
	}
}
