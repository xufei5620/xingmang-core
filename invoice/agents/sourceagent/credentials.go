package sourceagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxCredentialBytes = 64 << 10

// CredentialSnapshot is an immutable request-scoped credential. Its secret is
// intentionally private, has no String method, and must never be logged.
type CredentialSnapshot struct {
	version    string
	headerName string
	prefix     string
	secret     []byte
}

func NewCredentialSnapshot(version, headerName, prefix string, secret []byte) (CredentialSnapshot, error) {
	headerName = strings.TrimSpace(headerName)
	if headerName == "" || strings.ContainsAny(headerName, "\r\n:") || strings.ContainsAny(prefix, "\r\n") {
		return CredentialSnapshot{}, errors.New("invalid credential header name")
	}
	secret = bytes.TrimSpace(secret)
	if len(secret) == 0 || len(secret) > maxCredentialBytes || bytes.ContainsAny(secret, "\r\n") {
		return CredentialSnapshot{}, ErrCredentialUnavailable
	}
	return CredentialSnapshot{
		version:    strings.TrimSpace(version),
		headerName: headerName,
		prefix:     prefix,
		secret:     append([]byte(nil), secret...),
	}, nil
}

func (c CredentialSnapshot) apply(reqHeader interface{ Set(string, string) }) {
	reqHeader.Set(c.headerName, c.prefix+string(c.secret))
}

func (c CredentialSnapshot) remove(reqHeader interface{ Del(string) }) {
	reqHeader.Del(c.headerName)
}

func (c CredentialSnapshot) Version() string { return c.version }

func (c *CredentialSnapshot) Destroy() {
	if c == nil {
		return
	}
	for i := range c.secret {
		c.secret[i] = 0
	}
	c.secret = nil
}

// CredentialProvider creates a fresh snapshot for each attempt. Connectors
// retry a 401/403 at most once with a second snapshot, providing a clean
// active-key rotation boundary without exposing credentials to callers.
type CredentialProvider interface {
	Snapshot(context.Context) (CredentialSnapshot, error)
}

type FileCredentialProvider struct {
	Path       string
	HeaderName string
	Prefix     string
}

func (p FileCredentialProvider) Snapshot(context.Context) (CredentialSnapshot, error) {
	path := strings.TrimSpace(p.Path)
	if path == "" || !filepath.IsAbs(path) {
		return CredentialSnapshot{}, fmt.Errorf("%w: credential file path must be absolute", ErrCredentialUnavailable)
	}
	file, err := os.Open(path)
	if err != nil {
		return CredentialSnapshot{}, fmt.Errorf("%w: open credential file", ErrCredentialUnavailable)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return CredentialSnapshot{}, fmt.Errorf("%w: stat credential file", ErrCredentialUnavailable)
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > maxCredentialBytes {
		return CredentialSnapshot{}, fmt.Errorf("%w: invalid credential file", ErrCredentialUnavailable)
	}
	secret := make([]byte, info.Size())
	if _, err := io.ReadFull(file, secret); err != nil {
		return CredentialSnapshot{}, fmt.Errorf("%w: read credential file", ErrCredentialUnavailable)
	}
	digest := sha256.Sum256(secret)
	version := hex.EncodeToString(digest[:8])
	credential, err := NewCredentialSnapshot(version, p.HeaderName, p.Prefix, secret)
	for i := range secret {
		secret[i] = 0
	}
	return credential, err
}

// AtomicCredentialProvider is useful for secret-manager integrations and
// tests. Rotate swaps the active snapshot; in-flight requests retain their own
// copy and the previous stored secret is zeroed.
type AtomicCredentialProvider struct {
	mu         sync.RWMutex
	version    uint64
	headerName string
	prefix     string
	secret     []byte
}

func NewAtomicCredentialProvider(headerName, prefix string, secret []byte) (*AtomicCredentialProvider, error) {
	provider := &AtomicCredentialProvider{headerName: headerName, prefix: prefix}
	if err := provider.Rotate(secret); err != nil {
		return nil, err
	}
	return provider, nil
}

func (p *AtomicCredentialProvider) Rotate(secret []byte) error {
	if p == nil {
		return ErrCredentialUnavailable
	}
	credential, err := NewCredentialSnapshot("candidate", p.headerName, p.prefix, secret)
	if err != nil {
		return err
	}
	defer credential.Destroy()
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.secret {
		p.secret[i] = 0
	}
	p.secret = append([]byte(nil), credential.secret...)
	p.version++
	return nil
}

func (p *AtomicCredentialProvider) Snapshot(context.Context) (CredentialSnapshot, error) {
	if p == nil {
		return CredentialSnapshot{}, ErrCredentialUnavailable
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.secret) == 0 {
		return CredentialSnapshot{}, ErrCredentialUnavailable
	}
	return NewCredentialSnapshot(fmt.Sprintf("rotation-%d", p.version), p.headerName, p.prefix, p.secret)
}
