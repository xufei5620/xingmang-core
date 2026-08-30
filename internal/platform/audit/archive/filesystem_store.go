package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// FilesystemStore is a disposable, local-only ObjectWriter/ExactObjectReader
// fixture. It intentionally has no delete/list/latest methods and is not a
// production archive backend.
type FilesystemStore struct {
	root     string
	bucketID string
	mu       sync.RWMutex
}

type filesystemObjectMetadata struct {
	Object ObjectVersionV1 `json:"object"`
}

func NewFilesystemStore(root, bucketID string) (*FilesystemStore, error) {
	if strings.TrimSpace(root) == "" || !validBucketID(bucketID) {
		return nil, fmt.Errorf("%w: filesystem store root/bucket", ErrArchiveValidation)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: filesystem store root", ErrArchiveValidation)
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create filesystem store root: %w", err)
	}
	return &FilesystemStore{root: absRoot, bucketID: bucketID}, nil
}

func (s *FilesystemStore) objectPath(key string) (string, error) {
	if s == nil || !validObjectKey(key) {
		return "", fmt.Errorf("%w: object key", ErrArchiveValidation)
	}
	// filepath.FromSlash handles Windows separators; validObjectKey already
	// rejects backslashes, but the additional checks keep this invariant local.
	relative := filepath.FromSlash(key)
	if filepath.IsAbs(relative) || strings.Contains(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return "", fmt.Errorf("%w: object path traversal", ErrArchiveValidation)
	}
	candidate := filepath.Join(s.root, s.bucketID, relative)
	rel, err := filepath.Rel(s.root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: object path escapes root", ErrArchiveValidation)
	}
	// Resolve existing path components so a pre-created symlink cannot redirect a
	// fixture write outside its managed root. Missing suffixes are safe because
	// they are created only after this lexical check and under the verified parent.
	resolvedRoot, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve store root", ErrArchiveValidation)
	}
	probe := candidate
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			resolvedProbe, evalErr := filepath.EvalSymlinks(probe)
			if evalErr != nil {
				return "", fmt.Errorf("%w: resolve object path", ErrArchiveValidation)
			}
			resolvedRel, relErr := filepath.Rel(resolvedRoot, resolvedProbe)
			if relErr != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("%w: symlink escapes store root", ErrArchiveValidation)
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	return candidate, nil
}

func (s *FilesystemStore) metadataPath(dataPath string) string { return dataPath + ".xm-meta" }

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func readFilesystemMetadata(path string) (ObjectVersionV1, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ObjectVersionV1{}, ErrObjectNotFound
		}
		return ObjectVersionV1{}, err
	}
	var metadata filesystemObjectMetadata
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return ObjectVersionV1{}, fmt.Errorf("%w: metadata decode", ErrObjectConflict)
	}
	if err := ValidateObjectVersion(metadata.Object); err != nil {
		return ObjectVersionV1{}, fmt.Errorf("%w: metadata invalid", ErrObjectConflict)
	}
	return metadata.Object, nil
}

func writeFilesystemMetadata(path string, value ObjectVersionV1) error {
	encoded, err := json.Marshal(filesystemObjectMetadata{Object: value})
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + uuid.NewString()
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func expectedObjectFromIntent(intent ObjectWriteIntentV1) ObjectVersionV1 {
	return ObjectVersionV1{
		BucketID: intent.BucketID, Key: intent.Key,
		VersionID: "fs-" + intent.SHA256,
		SHA256:    intent.SHA256, SizeBytes: intent.SizeBytes,
		ContentType: intent.ContentType, ProviderChecksum: "sha256:" + intent.SHA256,
		ETag: intent.SHA256, EncryptionMode: intent.EncryptionMode,
		KMSKeyID: intent.KMSKeyID, ObjectLockMode: intent.ObjectLockMode,
		RetainUntil: intent.RetainUntil,
	}
}

func objectMatchesIntent(object ObjectVersionV1, intent ObjectWriteIntentV1) bool {
	return object.BucketID == intent.BucketID && object.Key == intent.Key &&
		object.SHA256 == intent.SHA256 && object.SizeBytes == intent.SizeBytes &&
		object.ContentType == intent.ContentType && object.EncryptionMode == intent.EncryptionMode &&
		object.KMSKeyID == intent.KMSKeyID && object.ObjectLockMode == intent.ObjectLockMode &&
		object.RetainUntil == intent.RetainUntil
}

func (s *FilesystemStore) putIfAbsentLocked(ctx context.Context, intent ObjectWriteIntentV1, body []byte) (ObjectVersionV1, error) {
	dataPath, err := s.objectPath(intent.Key)
	if err != nil {
		return ObjectVersionV1{}, err
	}
	metadataPath := s.metadataPath(dataPath)
	if existing, err := readFilesystemMetadata(metadataPath); err == nil {
		if objectMatchesIntent(existing, intent) {
			if err := verifyFilesystemBody(dataPath, existing); err != nil {
				return ObjectVersionV1{}, err
			}
			return existing, nil
		}
		return ObjectVersionV1{}, fmt.Errorf("%w: same key has different metadata", ErrObjectConflict)
	} else if !errors.Is(err, ErrObjectNotFound) {
		return ObjectVersionV1{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		return ObjectVersionV1{}, err
	}
	file, err := os.OpenFile(dataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ObjectVersionV1{}, fmt.Errorf("%w: object exists without matching receipt", ErrObjectConflict)
		}
		return ObjectVersionV1{}, err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		_ = os.Remove(dataPath)
		return ObjectVersionV1{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dataPath)
		return ObjectVersionV1{}, err
	}
	object := expectedObjectFromIntent(intent)
	if err := ValidateObjectVersion(object); err != nil {
		_ = os.Remove(dataPath)
		return ObjectVersionV1{}, err
	}
	if err := writeFilesystemMetadata(metadataPath, object); err != nil {
		// Preserve the body as an uncommitted orphan. AUD2 never performs automatic
		// deletion; a later approved reconciliation may inspect it by exact intent.
		return ObjectVersionV1{}, err
	}
	return object, nil
}

func verifyFilesystemBody(dataPath string, expected ObjectVersionV1) error {
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrObjectNotFound
		}
		return err
	}
	if int64(len(raw)) != expected.SizeBytes || sha256Hex(raw) != expected.SHA256 {
		return fmt.Errorf("%w: stored body digest/size", ErrObjectConflict)
	}
	return nil
}

func (s *FilesystemStore) PutIfAbsent(ctx context.Context, intent ObjectWriteIntentV1, body io.Reader) (ObjectVersionV1, error) {
	if err := contextErr(ctx); err != nil {
		return ObjectVersionV1{}, err
	}
	if err := ValidateObjectWriteIntent(intent); err != nil {
		return ObjectVersionV1{}, err
	}
	if body == nil {
		return ObjectVersionV1{}, fmt.Errorf("%w: nil body", ErrArchiveValidation)
	}
	limited := io.LimitReader(body, intent.SizeBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return ObjectVersionV1{}, err
	}
	if int64(len(raw)) != intent.SizeBytes || sha256Hex(raw) != intent.SHA256 {
		return ObjectVersionV1{}, fmt.Errorf("%w: body digest/size", ErrArchiveValidation)
	}
	if err := contextErr(ctx); err != nil {
		return ObjectVersionV1{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putIfAbsentLocked(ctx, intent, raw)
}

func (s *FilesystemStore) exactMetadata(expected ObjectVersionV1) (ObjectVersionV1, string, error) {
	if err := ValidateObjectVersion(expected); err != nil {
		return ObjectVersionV1{}, "", err
	}
	dataPath, err := s.objectPath(expected.Key)
	if err != nil {
		return ObjectVersionV1{}, "", err
	}
	actual, err := readFilesystemMetadata(s.metadataPath(dataPath))
	if err != nil {
		return ObjectVersionV1{}, "", err
	}
	if actual != expected {
		return ObjectVersionV1{}, "", fmt.Errorf("%w: exact version metadata mismatch", ErrObjectConflict)
	}
	return actual, dataPath, nil
}

func (s *FilesystemStore) HeadVersion(ctx context.Context, expected ObjectVersionV1) (ObjectVersionV1, error) {
	if err := contextErr(ctx); err != nil {
		return ObjectVersionV1{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	actual, _, err := s.exactMetadata(expected)
	return actual, err
}

func (s *FilesystemStore) GetVersion(ctx context.Context, expected ObjectVersionV1) (io.ReadCloser, ObjectVersionV1, error) {
	if err := contextErr(ctx); err != nil {
		return nil, ObjectVersionV1{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	actual, dataPath, err := s.exactMetadata(expected)
	if err != nil {
		return nil, ObjectVersionV1{}, err
	}
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		return nil, ObjectVersionV1{}, err
	}
	if int64(len(raw)) != actual.SizeBytes || sha256Hex(raw) != actual.SHA256 {
		return nil, ObjectVersionV1{}, fmt.Errorf("%w: readback digest/size", ErrObjectConflict)
	}
	return io.NopCloser(bytes.NewReader(raw)), actual, nil
}

func (s *FilesystemStore) RecoverPutResult(ctx context.Context, intent ObjectWriteIntentV1) (ObjectVersionV1, error) {
	if err := contextErr(ctx); err != nil {
		return ObjectVersionV1{}, err
	}
	if err := ValidateObjectWriteIntent(intent); err != nil {
		return ObjectVersionV1{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	dataPath, err := s.objectPath(intent.Key)
	if err != nil {
		return ObjectVersionV1{}, err
	}
	actual, err := readFilesystemMetadata(s.metadataPath(dataPath))
	if err != nil {
		return ObjectVersionV1{}, err
	}
	if !objectMatchesIntent(actual, intent) {
		return ObjectVersionV1{}, fmt.Errorf("%w: recovery intent mismatch", ErrObjectConflict)
	}
	if err := verifyFilesystemBody(dataPath, actual); err != nil {
		return ObjectVersionV1{}, err
	}
	return actual, nil
}
