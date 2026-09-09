package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSourceConfigStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	body := `{"sources":[{"id":"10000000-0000-4000-8000-000000000001","type":"sub2api","name":"Sub2API","runtime_version":"0.1.178","streams":["payments","identities","usage","credits","balances"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := readConfig(path)
	if err != nil || len(config.Sources) != 1 {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	if err = os.WriteFile(path, []byte(body+` {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readConfig(path); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
