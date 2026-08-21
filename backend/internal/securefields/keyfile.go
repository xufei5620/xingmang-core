package securefields

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxKeyringFileBytes int64 = 64 << 10

type keyringFile struct {
	CurrentKeyID   string            `json:"current_key_id"`
	EncryptionKeys map[string]string `json:"encryption_keys"`
	IndexKey       string            `json:"index_key"`
}

// LoadKeyringFile loads versioned encryption keys from a mounted secret file.
// Production callers should mount this file read-only and never supply its
// contents through environment variables or command-line arguments.
func LoadKeyringFile(path string) (Keyring, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Keyring{}, errors.New("keyring file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Keyring{}, fmt.Errorf("open keyring file: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxKeyringFileBytes+1))
	if err != nil {
		return Keyring{}, fmt.Errorf("read keyring file: %w", err)
	}
	if int64(len(body)) > maxKeyringFileBytes {
		return Keyring{}, errors.New("keyring file is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var raw keyringFile
	if err = decoder.Decode(&raw); err != nil {
		return Keyring{}, fmt.Errorf("decode keyring file: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Keyring{}, errors.New("keyring file contains multiple JSON values")
		}
		return Keyring{}, fmt.Errorf("decode keyring trailing data: %w", err)
	}
	keyring := Keyring{
		CurrentKeyID:   strings.TrimSpace(raw.CurrentKeyID),
		EncryptionKeys: make(map[string][]byte, len(raw.EncryptionKeys)),
	}
	for id, encoded := range raw.EncryptionKeys {
		trimmedID := strings.TrimSpace(id)
		if trimmedID == "" || trimmedID != id {
			return Keyring{}, errors.New("encryption key IDs must be non-empty and whitespace-free")
		}
		decoded, decodeErr := decodeSecretKey(encoded)
		if decodeErr != nil {
			return Keyring{}, fmt.Errorf("decode encryption key %q: %w", id, decodeErr)
		}
		keyring.EncryptionKeys[trimmedID] = decoded
	}
	keyring.IndexKey, err = decodeSecretKey(raw.IndexKey)
	if err != nil {
		return Keyring{}, fmt.Errorf("decode index key: %w", err)
	}
	if err = keyring.Validate(); err != nil {
		return Keyring{}, fmt.Errorf("validate keyring file: %w", err)
	}
	return keyring, nil
}

func decodeSecretKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("key is empty")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, errors.New("key must use padded standard base64")
	}
	return decoded, nil
}
