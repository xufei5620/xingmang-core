package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type EnvironmentLookup func(string) string

// EnforceProductionAuthMode runs before opening a listener or reading secrets.
// Production can only use the local session and trusted staff callback runtime.
func EnforceProductionAuthMode(appEnv, authMode string, _ EnvironmentLookup) error {
	if !strings.EqualFold(strings.TrimSpace(appEnv), "production") {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(authMode)) {
	case "session":
		return nil
	case "", "mock", "disabled", "off", "none":
		return ErrProductionMockAuth
	default:
		return fmt.Errorf("unsupported production authentication mode %q", authMode)
	}
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
