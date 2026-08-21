package securefields

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

type Keyring struct {
	CurrentKeyID   string
	EncryptionKeys map[string][]byte
	IndexKey       []byte
}

func (k Keyring) Validate() error {
	if strings.TrimSpace(k.CurrentKeyID) == "" {
		return errors.New("current encryption key ID is required")
	}
	key := k.EncryptionKeys[k.CurrentKeyID]
	if len(key) != 32 {
		return errors.New("current encryption key must be 32 bytes")
	}
	for id, value := range k.EncryptionKeys {
		if strings.TrimSpace(id) == "" || len(value) != 32 {
			return errors.New("every encryption key must have an ID and 32 bytes")
		}
	}
	if len(k.IndexKey) < 32 {
		return errors.New("index HMAC key must be at least 32 bytes")
	}
	return nil
}

// Encrypt binds ciphertext to an application-defined AAD string such as
// tenant/user/record/field. Moving ciphertext between records then fails.
func (k Keyring) Encrypt(plaintext []byte, aad string) ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(aad) == "" {
		return nil, errors.New("AAD is required")
	}
	block, err := aes.NewCipher(k.EncryptionKeys[k.CurrentKeyID])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(aad))
	payload := append(nonce, sealed...)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return []byte("v1:" + k.CurrentKeyID + ":" + encoded), nil
}

func (k Keyring) Decrypt(ciphertext []byte, aad string) ([]byte, error) {
	parts := strings.SplitN(string(ciphertext), ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return nil, errors.New("unsupported ciphertext format")
	}
	key, ok := k.EncryptionKeys[parts[1]]
	if !ok || len(key) != 32 {
		return nil, errors.New("encryption key is unavailable")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("ciphertext encoding is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(payload) < gcm.NonceSize() {
		return nil, errors.New("ciphertext is truncated")
	}
	plaintext, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], []byte(aad))
	if err != nil {
		return nil, errors.New("ciphertext authentication failed")
	}
	return plaintext, nil
}

func (k Keyring) BlindIndex(namespace, value string) (string, error) {
	if err := k.Validate(); err != nil {
		return "", err
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "", errors.New("index namespace is required")
	}
	normalized := strings.ToUpper(strings.Join(strings.Fields(value), ""))
	mac := hmac.New(sha256.New, k.IndexKey)
	_, _ = mac.Write([]byte(namespace + "\n" + normalized))
	return fmt.Sprintf("h1:%x", mac.Sum(nil)), nil
}
