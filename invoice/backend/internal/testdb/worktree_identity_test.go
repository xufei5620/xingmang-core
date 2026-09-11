package testdb

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWorktreeIdentitySeparatesCollisionClasses(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, left, right string }{
		{"same_basename", "team-a/core", "team-b/core"},
		{"punctuation", "team/wt-a", "team/wt_a"},
		{"case", "team/Feature", "team/feature"},
		{"non_ascii", "team/甲", "team/乙"},
		{"non_ascii_prefix", "team/甲-core", "team/乙-core"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := perWorktreeDatabaseName(filepath.Join(root, filepath.FromSlash(tc.left)))
			b := perWorktreeDatabaseName(filepath.Join(root, filepath.FromSlash(tc.right)))
			if a == b {
				t.Fatalf("distinct worktree identities collided: %s", a)
			}
		})
	}
}

func TestWorktreeIdentityKeepsLegacyDatabaseUnselected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "core")
	if got := perWorktreeDatabaseName(root); got == "invoice_test_core" {
		t.Fatal("legacy basename-only database must remain unselected")
	}
}

func TestWorktreeIdentityCleansEquivalentPaths(t *testing.T) {
	root := t.TempDir()
	plain := root + string(filepath.Separator) + "core"
	alias := root + string(filepath.Separator) + "." + string(filepath.Separator) + "core"
	if perWorktreeDatabaseName(plain) != perWorktreeDatabaseName(alias) {
		t.Fatal("equivalent cleaned worktree paths must retain one identity")
	}
}

func TestResolveURLExplicitDatabaseDoesNotEnsure(t *testing.T) {
	const explicit = "postgres://local.invalid/invoice_test_owned?sslmode=disable"
	got, err := resolveURL(explicit,
		func() (string, error) { return t.TempDir(), nil },
		func(context.Context, string, string) error {
			t.Fatal("explicit non-default database must not call ensure")
			return nil
		})
	if err != nil || got != explicit {
		t.Fatalf("explicit non-default URL must pass through unchanged: err=%v", err)
	}
}
