package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRandomKeyLengthAndUniqueness(t *testing.T) {
	first, err := randomKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || len(second) != 32 || string(first) == string(second) {
		t.Fatal("key generation did not produce independent 256-bit keys")
	}
}

func TestGenerateKeyringRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "field-keyring.json")
	created, err := generateKeyring(path, "test-v1")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(created)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = generateKeyring(path, "test-v2"); err == nil {
		t.Fatal("existing keyring was overwritten")
	}
	after, err := os.ReadFile(created)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed overwrite attempt changed the keyring")
	}
}
