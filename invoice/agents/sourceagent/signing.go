package sourceagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sourceBatchPath = "/internal/v1/source-batches"

var signingKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidateSigningKeyID(value string) error {
	if !signingKeyIDPattern.MatchString(value) {
		return errors.New("signing key id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	return nil
}

type SigningKeySnapshot struct {
	KeyID      string
	privateKey ed25519.PrivateKey
}

func (s *SigningKeySnapshot) Destroy() {
	if s == nil {
		return
	}
	for i := range s.privateKey {
		s.privateKey[i] = 0
	}
	s.privateKey = nil
}

type SigningKeyProvider interface {
	Snapshot(context.Context) (SigningKeySnapshot, error)
}

type FileSigningKeyProvider struct {
	KeyID          string
	PrivateKeyFile string
}

func (p FileSigningKeyProvider) Snapshot(context.Context) (SigningKeySnapshot, error) {
	if ValidateSigningKeyID(p.KeyID) != nil || strings.TrimSpace(p.PrivateKeyFile) == "" || !filepath.IsAbs(p.PrivateKeyFile) {
		return SigningKeySnapshot{}, errors.New("invalid signing key configuration")
	}
	info, err := os.Lstat(p.PrivateKeyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxCredentialBytes || !secureStatePermissions(info) {
		return SigningKeySnapshot{}, errors.New("invalid signing key file")
	}
	raw, err := os.ReadFile(p.PrivateKeyFile)
	if err != nil {
		return SigningKeySnapshot{}, errors.New("read signing key failed")
	}
	block, _ := pem.Decode(raw)
	for i := range raw {
		raw[i] = 0
	}
	if block == nil || block.Type != "PRIVATE KEY" {
		return SigningKeySnapshot{}, errors.New("signing key must be PKCS8 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	for i := range block.Bytes {
		block.Bytes[i] = 0
	}
	if err != nil {
		return SigningKeySnapshot{}, errors.New("parse signing key failed")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return SigningKeySnapshot{}, errors.New("signing key is not Ed25519")
	}
	return SigningKeySnapshot{KeyID: p.KeyID, privateKey: append(ed25519.PrivateKey(nil), privateKey...)}, nil
}

type AtomicSigningKeyProvider struct {
	mu         sync.RWMutex
	keyID      string
	privateKey ed25519.PrivateKey
}

func NewAtomicSigningKeyProvider(keyID string, privateKey ed25519.PrivateKey) (*AtomicSigningKeyProvider, error) {
	provider := &AtomicSigningKeyProvider{}
	if err := provider.Rotate(keyID, privateKey); err != nil {
		return nil, err
	}
	return provider, nil
}

func (p *AtomicSigningKeyProvider) Rotate(keyID string, privateKey ed25519.PrivateKey) error {
	if p == nil || !signingKeyIDPattern.MatchString(keyID) || len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 signing key")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.privateKey {
		p.privateKey[i] = 0
	}
	p.keyID = keyID
	p.privateKey = append(ed25519.PrivateKey(nil), privateKey...)
	return nil
}

func (p *AtomicSigningKeyProvider) Snapshot(context.Context) (SigningKeySnapshot, error) {
	if p == nil {
		return SigningKeySnapshot{}, errors.New("signing key provider is nil")
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !signingKeyIDPattern.MatchString(p.keyID) || len(p.privateKey) != ed25519.PrivateKeySize {
		return SigningKeySnapshot{}, errors.New("signing key unavailable")
	}
	return SigningKeySnapshot{KeyID: p.keyID, privateKey: append(ed25519.PrivateKey(nil), p.privateKey...)}, nil
}

type SignatureMetadata struct {
	SourceID   string
	StreamID   string
	BatchID    string
	Sequence   uint64
	SentAt     string
	BodySHA256 string
	KeyID      string
	Signature  string
}

func SignBatch(ctx context.Context, batch ValidatedBatch, keys SigningKeyProvider, now time.Time) (SignatureMetadata, error) {
	if keys == nil || len(batch.RawBody) == 0 || batch.BodyHash != SHA256Hex(batch.RawBody) {
		return SignatureMetadata{}, ErrBodyHashMismatch
	}
	decoded, err := decodeBatch(batch.RawBody)
	if err != nil || decoded.SourceInstanceID != batch.Batch.SourceInstanceID || decoded.StreamID != batch.Batch.StreamID || decoded.BatchID != batch.Batch.BatchID || decoded.Sequence != batch.Batch.Sequence {
		return SignatureMetadata{}, errors.New("signed batch metadata does not match raw body")
	}
	key, err := keys.Snapshot(ctx)
	if err != nil {
		return SignatureMetadata{}, err
	}
	defer key.Destroy()
	metadata := SignatureMetadata{
		SourceID:   decoded.SourceInstanceID,
		StreamID:   decoded.StreamID,
		BatchID:    decoded.BatchID,
		Sequence:   decoded.Sequence,
		SentAt:     now.UTC().Format(time.RFC3339Nano),
		BodySHA256: batch.BodyHash,
		KeyID:      key.KeyID,
	}
	signature := ed25519.Sign(key.privateKey, signatureInput(metadata))
	metadata.Signature = base64.StdEncoding.EncodeToString(signature)
	return metadata, nil
}

func signatureInput(metadata SignatureMetadata) []byte {
	fields := []string{
		http.MethodPost,
		sourceBatchPath,
		metadata.SourceID,
	}
	// Preserve the v1 detached-signature input for legacy/offline fixtures.
	// Every production batch is v2 and therefore signs the non-empty stream.
	if metadata.StreamID != "" {
		fields = append(fields, metadata.StreamID)
	}
	fields = append(fields,
		metadata.BatchID,
		strconv.FormatUint(metadata.Sequence, 10),
		metadata.SentAt,
		metadata.BodySHA256,
	)
	return []byte(strings.Join(fields, "\n"))
}

type PublicKeyResolver interface {
	Resolve(sourceID, streamID, keyID string) (ed25519.PublicKey, bool)
}

type StaticPublicKeySet map[string]map[string]map[string]ed25519.PublicKey

func LoadRawEd25519PublicKeyFile(path string) (ed25519.PublicKey, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("Ed25519 public key path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 256 {
		return nil, errors.New("Ed25519 public key file is missing or unsafe")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("read Ed25519 public key failed")
	}
	encoded := bytes.TrimSpace(raw)
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, decodeErr := base64.StdEncoding.Strict().Decode(decoded, encoded)
	for index := range raw {
		raw[index] = 0
	}
	if decodeErr != nil || n != ed25519.PublicKeySize {
		return nil, errors.New("Ed25519 public key must be standard base64 of exactly 32 raw bytes")
	}
	result := ed25519.PublicKey(append([]byte(nil), decoded[:n]...))
	for index := range decoded {
		decoded[index] = 0
	}
	return result, nil
}

func (s StaticPublicKeySet) Resolve(sourceID, streamID, keyID string) (ed25519.PublicKey, bool) {
	streams, ok := s[sourceID]
	if !ok {
		return nil, false
	}
	keys, ok := streams[streamID]
	if !ok {
		return nil, false
	}
	key, ok := keys[keyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, false
	}
	return append(ed25519.PublicKey(nil), key...), true
}

func VerifyBatchSignature(raw []byte, metadata SignatureMetadata, keys PublicKeyResolver, now time.Time, maxSkew time.Duration) error {
	if keys == nil || !sourceIDPattern.MatchString(metadata.SourceID) || (metadata.StreamID != "" && !streamIDPattern.MatchString(metadata.StreamID)) || !uuidPattern.MatchString(metadata.BatchID) || metadata.Sequence == 0 || !hexHashPattern.MatchString(metadata.BodySHA256) || !signingKeyIDPattern.MatchString(metadata.KeyID) {
		return errors.New("invalid signature metadata")
	}
	if SHA256Hex(raw) != metadata.BodySHA256 {
		return ErrBodyHashMismatch
	}
	sentAt, err := time.Parse(time.RFC3339Nano, metadata.SentAt)
	if err != nil {
		return errors.New("invalid signature sent_at")
	}
	if maxSkew <= 0 {
		maxSkew = 5 * time.Minute
	}
	delta := now.UTC().Sub(sentAt.UTC())
	if delta < -maxSkew || delta > maxSkew {
		return ErrReplay
	}
	publicKey, ok := keys.Resolve(metadata.SourceID, metadata.StreamID, metadata.KeyID)
	if !ok {
		return errors.New("unknown signature key")
	}
	if len(metadata.Signature) != base64.StdEncoding.EncodedLen(ed25519.SignatureSize) {
		return errors.New("invalid batch signature")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(metadata.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, signatureInput(metadata), signature) {
		return errors.New("invalid batch signature")
	}
	return nil
}

type HTTPIngestClient struct {
	HTTP                     *RestrictedHTTPClient
	Keys                     SigningKeyProvider
	Now                      func() time.Time
	AllowInsecureDevelopment bool
}

// IngestHTTPError exposes only transport control metadata. It deliberately
// never retains or formats the response body.
type IngestHTTPError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *IngestHTTPError) Error() string {
	if e == nil {
		return "ingestion request failed"
	}
	return fmt.Sprintf("ingestion rejected batch with status %d", e.StatusCode)
}

// Permanent reports contract/authentication/chain failures that require an
// operator. Retrying these can amplify a credential incident or sequence fork.
func (e *IngestHTTPError) Permanent() bool {
	if e == nil {
		return false
	}
	switch e.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func (c *HTTPIngestClient) Send(ctx context.Context, batch ValidatedBatch) (IngestAck, error) {
	if c == nil || c.HTTP == nil || c.Keys == nil {
		return IngestAck{}, errors.New("ingestion client is not configured")
	}
	if !c.AllowInsecureDevelopment && (c.HTTP.origin.Scheme != "https" || !c.HTTP.hasClientCertificate) {
		return IngestAck{}, errors.New("production ingestion requires HTTPS and an mTLS client certificate")
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	metadata, err := SignBatch(ctx, batch, c.Keys, now().UTC())
	if err != nil {
		return IngestAck{}, err
	}
	req, err := c.HTTP.NewRequest(ctx, http.MethodPost, sourceBatchPath, nil)
	if err != nil {
		return IngestAck{}, err
	}
	req.Body = io.NopCloser(bytes.NewReader(batch.RawBody))
	req.ContentLength = int64(len(batch.RawBody))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(batch.RawBody)), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Source-ID", metadata.SourceID)
	if metadata.StreamID != "" {
		req.Header.Set("X-Stream-ID", metadata.StreamID)
	}
	req.Header.Set("X-Batch-ID", metadata.BatchID)
	req.Header.Set("X-Sequence", strconv.FormatUint(metadata.Sequence, 10))
	req.Header.Set("X-Sent-At", metadata.SentAt)
	req.Header.Set("X-Content-SHA256", metadata.BodySHA256)
	req.Header.Set("X-Signature-Key-ID", metadata.KeyID)
	req.Header.Set("X-Signature", metadata.Signature)
	response, err := c.HTTP.Do(req)
	if err != nil {
		return IngestAck{}, fmt.Errorf("send signed batch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return IngestAck{}, &IngestHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: boundedRetryAfter(response.Header.Get("Retry-After"), now().UTC()),
		}
	}
	var ack IngestAck
	if err := decodeBoundedJSON(response.Body, 1<<20, &ack, true); err != nil {
		return IngestAck{}, errors.New("invalid ingestion acknowledgement")
	}
	return ack, nil
}

func boundedRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var duration time.Duration
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		if seconds >= int64(time.Hour/time.Second) {
			return time.Hour
		}
		duration = time.Duration(seconds) * time.Second
	} else if retryAt, err := http.ParseTime(raw); err == nil {
		duration = retryAt.Sub(now)
	}
	if duration < 0 {
		return 0
	}
	if duration > time.Hour {
		return time.Hour
	}
	return duration
}
