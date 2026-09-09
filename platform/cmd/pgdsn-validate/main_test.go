package main

import "testing"

func TestValidateDSNRequiresManagedPassword(t *testing.T) {
	// Keep ambient libpq password sources out of this assertion: DBR1 must be
	// able to prove that the internally-built DSN is password-free.
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGPASSFILE", "")
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	if err := validateDSN("postgres://postgres@127.0.0.1:5432/dbr1?sslmode=disable", true, true); err != nil {
		t.Fatalf("password-free loopback DSN rejected: %v", err)
	}
	if err := validateDSN("postgres://postgres:secret@127.0.0.1:5432/dbr1", true, true); err == nil {
		t.Fatal("inline password accepted while managed-password mode is required")
	}
}

func TestValidateDSNRequiresLoopback(t *testing.T) {
	t.Setenv("PGPASSWORD", "")
	if err := validateDSN("postgres://postgres@db.example.invalid:5432/dbr1", false, true); err == nil {
		t.Fatal("non-loopback DSN accepted")
	}
}
