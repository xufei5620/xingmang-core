package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

func validOpaqueToken(value string) bool {
	if len(value) < 43 || len(value) > 128 || strings.ContainsAny(value, "=\r\n\x00") {
		return false
	}
	decoded, err := url.QueryUnescape(value)
	return err == nil && decoded == value
}

func randomURLToken(bytes int) (string, error) {
	if bytes < 32 {
		return "", errors.New("security token entropy must be at least 256 bits")
	}
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
