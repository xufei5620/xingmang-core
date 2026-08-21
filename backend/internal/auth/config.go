package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const maxClientSecretBytes = 64 * 1024

type EnvironmentLookup func(string) string
type SecretFileReader func(string) ([]byte, error)

// EnforceProductionAuthMode is intended to run before an HTTP listener is
// opened. It rejects every mock/off/disabled spelling and any plaintext client
// secret variable in production.
func EnforceProductionAuthMode(appEnv, authMode string, getenv EnvironmentLookup) error {
	if !strings.EqualFold(strings.TrimSpace(appEnv), "production") {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(authMode)) {
	case "oidc":
	case "", "mock", "disabled", "off", "none":
		return ErrProductionMockAuth
	default:
		return fmt.Errorf("unsupported production authentication mode %q", authMode)
	}
	if getenv != nil {
		for _, name := range []string{"OIDC_CLIENT_SECRET", "OIDC_WEB_CLIENT_SECRET", "OIDC_ADMIN_CLIENT_SECRET"} {
			if getenv(name) != "" {
				return fmt.Errorf("%w: %s", ErrPlainClientSecret, name)
			}
		}
	}
	return nil
}

func LoadClientSecretFile(path string) (string, error) {
	return loadClientSecret(path, os.ReadFile)
}

func loadClientSecret(path string, readFile SecretFileReader) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrClientSecretMissing
	}
	if readFile == nil {
		return "", errors.New("OIDC client secret file reader is nil")
	}
	body, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("read OIDC client secret file: %w", err)
	}
	if len(body) == 0 || len(body) > maxClientSecretBytes {
		return "", errors.New("OIDC client secret file must contain 1 to 65536 bytes")
	}
	// Docker/Kubernetes secret files commonly end in one line ending. Remove
	// exactly one; do not TrimSpace and silently alter a legitimate secret.
	body = stripOneLineEnding(body)
	if len(body) == 0 || strings.ContainsRune(string(body), '\x00') {
		return "", errors.New("OIDC client secret file is empty or contains NUL")
	}
	return string(body), nil
}

func stripOneLineEnding(value []byte) []byte {
	if len(value) >= 2 && value[len(value)-2] == '\r' && value[len(value)-1] == '\n' {
		return value[:len(value)-2]
	}
	if len(value) >= 1 && value[len(value)-1] == '\n' {
		return value[:len(value)-1]
	}
	return value
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
