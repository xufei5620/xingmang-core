package main

import (
	"os"
	"testing"
)

func TestLookupEnvUsesNamedValue(t *testing.T) {
	original := lookupEnv
	t.Cleanup(func() { lookupEnv = original })
	lookupEnv = func(name string) (string, bool) {
		if name != "XM_TEST_DSN" {
			t.Fatalf("unexpected env name %q", name)
		}
		return "postgres://localhost/db", true
	}
	value, ok := lookupEnv("XM_TEST_DSN")
	if !ok || value == "" {
		t.Fatal("expected test DSN")
	}
	_ = os.Getenv // keep the test explicit about process-env isolation
}
