package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type keyringFile struct {
	CurrentKeyID   string            `json:"current_key_id"`
	EncryptionKeys map[string]string `json:"encryption_keys"`
	IndexKey       string            `json:"index_key"`
}

func main() {
	defaultID := time.Now().UTC().Format("2006-01")
	out := flag.String("out", "", "new keyring file path (must not already exist)")
	keyID := flag.String("key-id", defaultID, "non-secret encryption key version")
	flag.Parse()
	path, err := generateKeyring(*out, *keyID)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("created keyring path=%s key_id=%s\n", path, *keyID)
}

func generateKeyring(output, keyID string) (string, error) {
	if strings.TrimSpace(output) == "" || strings.TrimSpace(keyID) == "" || strings.TrimSpace(keyID) != keyID {
		return "", fmt.Errorf("--out and a whitespace-free --key-id are required")
	}
	path, err := filepath.Abs(output)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create keyring without overwrite: %w", err)
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	encryptionKey, err := randomKey()
	if err != nil {
		return "", fmt.Errorf("generate encryption key: %w", err)
	}
	indexKey, err := randomKey()
	if err != nil {
		return "", fmt.Errorf("generate index key: %w", err)
	}
	payload := keyringFile{
		CurrentKeyID: keyID,
		EncryptionKeys: map[string]string{
			keyID: base64.StdEncoding.EncodeToString(encryptionKey),
		},
		IndexKey: base64.StdEncoding.EncodeToString(indexKey),
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(payload); err != nil {
		return "", fmt.Errorf("write keyring: %w", err)
	}
	if err = file.Sync(); err != nil {
		return "", fmt.Errorf("sync keyring: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("close keyring: %w", err)
	}
	ok = true
	return path, nil
}

func randomKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return key, err
}

func fatal(format string, values ...any) {
	fmt.Fprintf(os.Stderr, "keygen: "+format+"\n", values...)
	os.Exit(1)
}
