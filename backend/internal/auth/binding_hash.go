package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

type ClientBindingHasher struct{ key []byte }

func NewClientBindingHasherFromFile(path string) (*ClientBindingHasher, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("client-binding HMAC key file is required")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	body = stripOneLineEnding(body)
	if len(body) < 32 || len(body) > 1024 {
		return nil, errors.New("client-binding HMAC key must contain 32 to 1024 bytes")
	}
	return &ClientBindingHasher{key: append([]byte(nil), body...)}, nil
}

func newClientBindingHasher(key []byte) (*ClientBindingHasher, error) {
	if len(key) < 32 {
		return nil, errors.New("client-binding HMAC key must be at least 32 bytes")
	}
	return &ClientBindingHasher{key: append([]byte(nil), key...)}, nil
}

func (h *ClientBindingHasher) Hash(ip, userAgent string) (ClientBinding, error) {
	if h == nil || len(h.key) < 32 {
		return ClientBinding{}, errors.New("client-binding HMAC key is unavailable")
	}
	if strings.ContainsAny(ip, "\r\n\x00") || strings.ContainsAny(userAgent, "\r\n\x00") {
		return ClientBinding{}, errors.New("client binding contains control characters")
	}
	binding := ClientBinding{}
	if ip != "" {
		binding.IPHash = h.sum("ip", ip)
	}
	if userAgent != "" {
		binding.UserAgentHash = h.sum("user-agent", userAgent)
	}
	return binding, nil
}

func (h *ClientBindingHasher) sum(namespace, value string) string {
	mac := hmac.New(sha256.New, h.key)
	_, _ = mac.Write([]byte("invoice:session-binding:" + namespace + "\n" + value))
	return hex.EncodeToString(mac.Sum(nil))
}
