package sourceagent

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	pendingSpoolSchemaVersion = 1
	pendingPlaintextMaxBytes  = MaxBatchBytes + (128 << 10)
	pendingEnvelopeMaxBytes   = (pendingPlaintextMaxBytes * 2) + (64 << 10)
)

type PendingBatch struct {
	Batch        ValidatedBatch
	CursorBefore ScanCursor
	CursorAfter  ScanCursor
}

func (p *PendingBatch) Destroy() {
	if p == nil {
		return
	}
	for index := range p.Batch.RawBody {
		p.Batch.RawBody[index] = 0
	}
	for recordIndex := range p.Batch.Batch.Records {
		for payloadIndex := range p.Batch.Batch.Records[recordIndex].Payload {
			p.Batch.Batch.Records[recordIndex].Payload[payloadIndex] = 0
		}
		p.Batch.Batch.Records[recordIndex].Payload = nil
	}
	p.Batch.RawBody = nil
	*p = PendingBatch{}
}

type PendingBatchStore interface {
	Load(context.Context) (PendingBatch, bool, error)
	SaveIfAbsent(context.Context, PendingBatch) (bool, error)
	Clear(context.Context, string) error
}

type SpoolKeySnapshot struct{ key []byte }

func (s *SpoolKeySnapshot) Destroy() {
	if s == nil {
		return
	}
	for index := range s.key {
		s.key[index] = 0
	}
	s.key = nil
}

type SpoolKeyProvider interface {
	Snapshot(context.Context) (SpoolKeySnapshot, error)
}

// FileSpoolKeyProvider loads one base64-encoded 32-byte AES key. Rotation is
// allowed only while no pending spool exists; otherwise decryption fails closed
// and the old key must be restored to complete the exact retry.
type FileSpoolKeyProvider struct{ Path string }

func (p FileSpoolKeyProvider) Snapshot(context.Context) (SpoolKeySnapshot, error) {
	if !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path {
		return SpoolKeySnapshot{}, errors.New("pending spool key path must be absolute and clean")
	}
	info, err := os.Lstat(p.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 256 || !secureStatePermissions(info) {
		return SpoolKeySnapshot{}, errors.New("pending spool key file is missing or unsafe")
	}
	raw, err := os.ReadFile(p.Path)
	if err != nil {
		return SpoolKeySnapshot{}, errors.New("read pending spool key failed")
	}
	encoded := bytes.TrimSpace(raw)
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, decodeErr := base64.StdEncoding.Strict().Decode(decoded, encoded)
	for index := range raw {
		raw[index] = 0
	}
	for index := range encoded {
		encoded[index] = 0
	}
	if decodeErr != nil || n != 32 {
		for index := range decoded {
			decoded[index] = 0
		}
		return SpoolKeySnapshot{}, errors.New("pending spool key must be base64 of exactly 32 bytes")
	}
	result := SpoolKeySnapshot{key: append([]byte(nil), decoded[:n]...)}
	for index := range decoded {
		decoded[index] = 0
	}
	return result, nil
}

type EncryptedFilePendingStore struct {
	Path     string
	SourceID string
	StreamID string
	Keys     SpoolKeyProvider
	mu       sync.Mutex
}

var _ PendingBatchStore = (*EncryptedFilePendingStore)(nil)

// CheckReadOnly decrypts and validates a pending spool without acquiring a
// filesystem lock or mutating the spool. The production check-state command
// first proves that no stale lock exists and that the agent is stopped.
func (s *EncryptedFilePendingStore) CheckReadOnly(ctx context.Context) (PendingBatch, bool, error) {
	if err := s.validateConfiguration(); err != nil {
		return PendingBatch{}, false, err
	}
	return s.readUnlocked(ctx)
}

type pendingPlaintext struct {
	SchemaVersion        int             `json:"schema_version"`
	SourceID             string          `json:"source_id"`
	StreamID             string          `json:"stream_id"`
	CursorBeforeRevision uint64          `json:"cursor_before_revision"`
	CursorBefore         ScanCursor      `json:"cursor_before"`
	CursorAfterRevision  uint64          `json:"cursor_after_revision"`
	CursorAfter          ScanCursor      `json:"cursor_after"`
	BodySHA256           string          `json:"body_sha256"`
	RawBody              json.RawMessage `json:"raw_body"`
}

type encryptedPendingEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Algorithm     string `json:"algorithm"`
	Nonce         string `json:"nonce"`
	Ciphertext    string `json:"ciphertext"`
}

func (s *EncryptedFilePendingStore) Load(ctx context.Context) (PendingBatch, bool, error) {
	if err := s.validateConfiguration(); err != nil {
		return PendingBatch{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var pending PendingBatch
	var exists bool
	err := s.withProcessLock(ctx, func() error {
		var err error
		pending, exists, err = s.readUnlocked(ctx)
		return err
	})
	return pending, exists, err
}

func (s *EncryptedFilePendingStore) SaveIfAbsent(ctx context.Context, pending PendingBatch) (bool, error) {
	if err := s.validateConfiguration(); err != nil {
		return false, err
	}
	if err := s.validatePending(pending); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := false
	err := s.withProcessLock(ctx, func() error {
		if _, err := os.Lstat(s.Path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect pending spool failed")
		}
		raw, err := s.encrypt(ctx, pending)
		if err != nil {
			return err
		}
		defer func() {
			for index := range raw {
				raw[index] = 0
			}
		}()
		if err := writePrivateAtomicFile(s.Path, raw); err != nil {
			return err
		}
		stored = true
		return nil
	})
	return stored, err
}

func (s *EncryptedFilePendingStore) Clear(ctx context.Context, expectedBodyHash string) error {
	if err := s.validateConfiguration(); err != nil {
		return err
	}
	if !hexHashPattern.MatchString(expectedBodyHash) {
		return errors.New("invalid pending spool clear hash")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withProcessLock(ctx, func() error {
		pending, exists, err := s.readUnlocked(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		defer pending.Destroy()
		if pending.Batch.BodyHash != expectedBodyHash {
			return errors.New("refuse to clear a different pending batch")
		}
		if err := os.Remove(s.Path); err != nil {
			return errors.New("remove pending spool failed")
		}
		return syncStateDirectory(filepath.Dir(s.Path))
	})
}

func (s *EncryptedFilePendingStore) readUnlocked(ctx context.Context) (PendingBatch, bool, error) {
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return PendingBatch{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !secureStatePermissions(info) || info.Size() <= 0 || info.Size() > pendingEnvelopeMaxBytes {
		return PendingBatch{}, false, errors.New("pending spool path is missing or unsafe")
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return PendingBatch{}, false, errors.New("open pending spool failed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, pendingEnvelopeMaxBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) == 0 || len(raw) > pendingEnvelopeMaxBytes {
		return PendingBatch{}, false, errors.New("read pending spool failed")
	}
	defer func() {
		for index := range raw {
			raw[index] = 0
		}
	}()
	var envelope encryptedPendingEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || ensureDecodeEOF(decoder) != nil || envelope.SchemaVersion != pendingSpoolSchemaVersion || envelope.Algorithm != "AES-256-GCM" {
		return PendingBatch{}, false, errors.New("decode pending spool envelope failed")
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(envelope.Nonce)
	if err != nil {
		return PendingBatch{}, false, errors.New("decode pending spool nonce failed")
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) == 0 {
		return PendingBatch{}, false, errors.New("decode pending spool ciphertext failed")
	}
	key, err := s.Keys.Snapshot(ctx)
	if err != nil {
		return PendingBatch{}, false, err
	}
	defer key.Destroy()
	block, err := aes.NewCipher(key.key)
	if err != nil {
		return PendingBatch{}, false, errors.New("initialize pending spool cipher failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return PendingBatch{}, false, errors.New("pending spool nonce is invalid")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, s.aad())
	if err != nil {
		return PendingBatch{}, false, errors.New("pending spool authentication failed")
	}
	defer func() {
		for index := range plaintext {
			plaintext[index] = 0
		}
	}()
	if len(plaintext) == 0 || len(plaintext) > pendingPlaintextMaxBytes {
		return PendingBatch{}, false, errors.New("pending spool plaintext size is invalid")
	}
	var stored pendingPlaintext
	plainDecoder := json.NewDecoder(bytes.NewReader(plaintext))
	plainDecoder.DisallowUnknownFields()
	if err := plainDecoder.Decode(&stored); err != nil || ensureDecodeEOF(plainDecoder) != nil {
		return PendingBatch{}, false, errors.New("decode pending spool plaintext failed")
	}
	defer func() {
		for index := range stored.RawBody {
			stored.RawBody[index] = 0
		}
	}()
	pending, err := s.fromPlaintext(stored)
	if err != nil {
		return PendingBatch{}, false, err
	}
	return pending, true, nil
}

func (s *EncryptedFilePendingStore) encrypt(ctx context.Context, pending PendingBatch) ([]byte, error) {
	stored := pendingPlaintext{
		SchemaVersion: pendingSpoolSchemaVersion, SourceID: s.SourceID, StreamID: s.StreamID,
		CursorBeforeRevision: pending.CursorBefore.Revision, CursorBefore: pending.CursorBefore,
		CursorAfterRevision: pending.CursorAfter.Revision, CursorAfter: pending.CursorAfter,
		BodySHA256: pending.Batch.BodyHash, RawBody: json.RawMessage(pending.Batch.RawBody),
	}
	plaintext, err := json.Marshal(stored)
	if err != nil || len(plaintext) > pendingPlaintextMaxBytes {
		return nil, errors.New("encode pending spool plaintext failed")
	}
	defer func() {
		for index := range plaintext {
			plaintext[index] = 0
		}
	}()
	key, err := s.Keys.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	block, err := aes.NewCipher(key.key)
	if err != nil {
		return nil, errors.New("initialize pending spool cipher failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize pending spool GCM failed")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("generate pending spool nonce failed")
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, s.aad())
	envelope := encryptedPendingEnvelope{
		SchemaVersion: pendingSpoolSchemaVersion, Algorithm: "AES-256-GCM",
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	raw, err := json.Marshal(envelope)
	if err != nil || len(raw) > pendingEnvelopeMaxBytes {
		return nil, errors.New("encode pending spool envelope failed")
	}
	return raw, nil
}

func (s *EncryptedFilePendingStore) fromPlaintext(stored pendingPlaintext) (PendingBatch, error) {
	stored.CursorBefore.Revision = stored.CursorBeforeRevision
	stored.CursorAfter.Revision = stored.CursorAfterRevision
	pending := PendingBatch{
		CursorBefore: stored.CursorBefore, CursorAfter: stored.CursorAfter,
		Batch: ValidatedBatch{RawBody: append([]byte(nil), stored.RawBody...), BodyHash: stored.BodySHA256},
	}
	batch, err := decodeBatch(pending.Batch.RawBody)
	if err != nil {
		return PendingBatch{}, errors.New("pending spool contains an invalid batch")
	}
	pending.Batch.Batch = batch
	if err := s.validatePending(pending); err != nil {
		return PendingBatch{}, err
	}
	return pending, nil
}

func (s *EncryptedFilePendingStore) validatePending(pending PendingBatch) error {
	decoded, err := decodeBatch(pending.Batch.RawBody)
	if err != nil || (decoded.SchemaVersion != SchemaVersionV2 && decoded.SchemaVersion != SchemaVersionV3) ||
		decoded.SourceInstanceID != s.SourceID || decoded.StreamID != s.StreamID ||
		decoded.BatchID != pending.Batch.Batch.BatchID || decoded.Sequence != pending.Batch.Batch.Sequence ||
		pending.Batch.BodyHash != SHA256Hex(pending.Batch.RawBody) ||
		!hexHashPattern.MatchString(pending.Batch.BodyHash) {
		return errors.New("pending batch metadata or body hash is invalid")
	}
	if err := validateStoredFileCursorForStream(pending.CursorBefore, s.StreamID); err != nil {
		return err
	}
	// The prefix is load-bearing: keep it exact and lead with it so any
	// caller matching on it (or just reading the log) still recognizes this
	// failure, while %w preserves the underlying cause for diagnosis --
	// XM-INV-AGENT-CREDITS-RECONCILE-FIX was this exact message with no
	// cause attached, which took longer to root-cause than it should have.
	if err := validateStoredFileCursorForStream(pending.CursorAfter, s.StreamID); err != nil {
		return fmt.Errorf("pending batch cursor transition is invalid: %w", err)
	}
	if pending.CursorAfter.Revision != pending.CursorBefore.Revision {
		return errors.New("pending batch cursor transition is invalid")
	}
	return nil
}

func (s *EncryptedFilePendingStore) aad() []byte {
	return []byte("invoice-source-agent-pending-v1\x00" + s.SourceID + "\x00" + s.StreamID)
}

func (s *EncryptedFilePendingStore) validateConfiguration() error {
	if s == nil || s.Keys == nil || !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path ||
		!sourceIDPattern.MatchString(s.SourceID) || !streamIDPattern.MatchString(s.StreamID) {
		return errors.New("pending spool configuration is invalid")
	}
	directory := filepath.Dir(s.Path)
	info, err := os.Lstat(directory)
	if err != nil || !secureStateDirectoryPermissions(info) || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("pending spool directory is missing or unsafe")
	}
	return nil
}

func (s *EncryptedFilePendingStore) withProcessLock(ctx context.Context, action func() error) error {
	deadline := time.NewTimer(fileLockWait)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	lockPath := s.Path + ".lock"
	for {
		lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if chmodErr := lock.Chmod(0o600); chmodErr != nil {
				_ = lock.Close()
				_ = os.Remove(lockPath)
				return errors.New("set pending spool lock permissions failed")
			}
			if syncErr := lock.Sync(); syncErr != nil {
				_ = lock.Close()
				_ = os.Remove(lockPath)
				return errors.New("fsync pending spool lock failed")
			}
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return errors.New("close pending spool lock failed")
			}
			actionErr := action()
			removeErr := os.Remove(lockPath)
			if actionErr != nil {
				return actionErr
			}
			if removeErr != nil {
				return errors.New("remove pending spool lock failed")
			}
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return errors.New("create pending spool lock failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("pending spool lock is held or stale; fail closed")
		case <-ticker.C:
		}
	}
}

func writePrivateAtomicFile(path string, raw []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".source-agent-pending-*.tmp")
	if err != nil {
		return errors.New("create temporary pending spool failed")
	}
	temporaryPath := temporary.Name()
	keep := true
	defer func() {
		_ = temporary.Close()
		if keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("set temporary pending spool permissions failed")
	}
	if _, err := temporary.Write(raw); err != nil {
		return errors.New("write temporary pending spool failed")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("fsync temporary pending spool failed")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close temporary pending spool failed")
	}
	if err := atomicReplaceFile(temporaryPath, path); err != nil {
		return err
	}
	keep = false
	if err := os.Chmod(path, 0o600); err != nil {
		return errors.New("set pending spool permissions failed")
	}
	return syncStateDirectory(directory)
}

type MemoryPendingBatchStore struct {
	mu      sync.Mutex
	pending *PendingBatch
}

func (s *MemoryPendingBatchStore) Load(context.Context) (PendingBatch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return PendingBatch{}, false, nil
	}
	return clonePending(*s.pending), true, nil
}

func (s *MemoryPendingBatchStore) SaveIfAbsent(_ context.Context, pending PendingBatch) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		return false, nil
	}
	copy := clonePending(pending)
	s.pending = &copy
	return true, nil
}

func (s *MemoryPendingBatchStore) Clear(_ context.Context, expectedBodyHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	if s.pending.Batch.BodyHash != expectedBodyHash {
		return errors.New("refuse to clear a different pending batch")
	}
	s.pending = nil
	return nil
}

func clonePending(value PendingBatch) PendingBatch {
	value.Batch.RawBody = append([]byte(nil), value.Batch.RawBody...)
	value.Batch.Batch.Records = append([]Record(nil), value.Batch.Batch.Records...)
	for index := range value.Batch.Batch.Records {
		value.Batch.Batch.Records[index].Payload = append([]byte(nil), value.Batch.Batch.Records[index].Payload...)
	}
	return value
}
