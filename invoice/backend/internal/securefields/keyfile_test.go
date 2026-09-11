package securefields

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKeyringFile(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	index := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("i", 32)))
	path := filepath.Join(t.TempDir(), "keyring.json")
	body := `{"current_key_id":"2026-08","encryption_keys":{"2026-08":"` + key + `"},"index_key":"` + index + `"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring, err := LoadKeyringFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if keyring.CurrentKeyID != "2026-08" || len(keyring.EncryptionKeys["2026-08"]) != 32 || len(keyring.IndexKey) != 32 {
		t.Fatalf("unexpected keyring metadata: current=%q encryption=%d index=%d", keyring.CurrentKeyID, len(keyring.EncryptionKeys["2026-08"]), len(keyring.IndexKey))
	}
}

func TestLoadKeyringFileRejectsUnknownInvalidAndTrailingData(t *testing.T) {
	// Otherwise-valid, deterministic fixture material isolates each JSON guard.
	// No generated or deployed key material is used or printed.
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	index := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("i", 32)))
	valid := `{"current_key_id":"fixture","encryption_keys":{"fixture":"` + key + `"},"index_key":"` + index + `"}`
	for name, body := range map[string]string{
		"unknown":  strings.TrimSuffix(valid, "}") + `,"unexpected":true}`,
		"invalid":  `{"current_key_id":"x","encryption_keys":{"x":"not-base64"},"index_key":"not-base64"}`,
		"trailing": valid + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keyring.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadKeyringFile(path); err == nil {
				t.Fatal("invalid keyring file accepted")
			}
		})
	}
}
