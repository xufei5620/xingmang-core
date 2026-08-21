package document

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"invoice-system/backend/internal/securefields"
)

const (
	encryptedPDFMagic      = "INVPDF1\x00"
	encryptedPDFChunkBytes = 64 << 10
	maxDocumentKeyIDBytes  = 128
)

// EncryptedLocalStore scans plaintext in a private quarantine and promotes
// only chunk-authenticated ciphertext. Decryption occurs only after the caller
// has completed request ownership/authorization checks.
type EncryptedLocalStore struct {
	Root           string
	QuarantineRoot string
	MaxBytes       int64
	Scanner        Scanner
	Keyring        securefields.Keyring
	DirectorySync  func(string) error
}

var _ Store = EncryptedLocalStore{}

func (s EncryptedLocalStore) SavePDF(ctx context.Context, reader io.Reader) (StoredPDF, error) {
	if strings.TrimSpace(s.Root) == "" || s.Scanner == nil {
		return StoredPDF{}, errors.New("encrypted document store is not safely configured")
	}
	if err := s.Keyring.Validate(); err != nil {
		return StoredPDF{}, fmt.Errorf("document keyring: %w", err)
	}
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxPDFBytes
	}
	quarantineRoot := strings.TrimSpace(s.QuarantineRoot)
	if quarantineRoot == "" {
		quarantineRoot = s.Root
	}
	quarantine := filepath.Join(quarantineRoot, "invoice-quarantine")
	issued := filepath.Join(s.Root, "issued")
	if err := os.MkdirAll(quarantine, 0o700); err != nil {
		return StoredPDF{}, err
	}
	if err := os.MkdirAll(issued, 0o700); err != nil {
		return StoredPDF{}, err
	}
	objectID, err := randomKey()
	if err != nil {
		return StoredPDF{}, err
	}
	plainPath := filepath.Join(quarantine, objectID+".pdf.part")
	plain, err := os.OpenFile(plainPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return StoredPDF{}, err
	}
	defer func() {
		_ = plain.Close()
		_ = os.Remove(plainPath)
	}()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(plain, hash), io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return StoredPDF{}, err
	}
	if written > maxBytes {
		return StoredPDF{}, ErrTooLarge
	}
	if err = plain.Sync(); err != nil {
		return StoredPDF{}, err
	}
	if err = plain.Close(); err != nil {
		return StoredPDF{}, err
	}
	valid, err := hasPDFMagic(plainPath)
	if err != nil {
		return StoredPDF{}, err
	}
	if !valid {
		return StoredPDF{}, ErrInvalidPDF
	}
	if err = s.Scanner.Scan(ctx, plainPath); err != nil {
		return StoredPDF{}, fmt.Errorf("%w: %v", ErrScanRejected, err)
	}

	objectKey := "issued/" + objectID + ".pdf.enc"
	finalPath := filepath.Join(s.Root, filepath.FromSlash(objectKey))
	tempEncryptedPath := finalPath + ".part"
	if err = encryptPDFFile(plainPath, tempEncryptedPath, objectKey, s.Keyring); err != nil {
		_ = os.Remove(tempEncryptedPath)
		return StoredPDF{}, err
	}
	if err = os.Rename(tempEncryptedPath, finalPath); err != nil {
		_ = os.Remove(tempEncryptedPath)
		return StoredPDF{}, err
	}
	if err = s.syncDirectory(issued); err != nil {
		_ = os.Remove(finalPath)
		_ = s.syncDirectory(issued)
		return StoredPDF{}, fmt.Errorf("sync encrypted issued directory: %w", err)
	}
	return StoredPDF{
		ObjectKey:     objectKey,
		ObjectVersion: "encrypted-local-v1:" + s.Keyring.CurrentKeyID,
		SHA256:        hex.EncodeToString(hash.Sum(nil)),
		SizeBytes:     written,
		MIME:          "application/pdf",
	}, nil
}

func (s EncryptedLocalStore) OpenAuthorized(objectKey string) (io.ReadCloser, error) {
	target, err := resolveIssuedPath(s.Root, objectKey, ".pdf.enc")
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	reader, err := newEncryptedPDFReader(file, objectKey, s.Keyring)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return reader, nil
}

func (s EncryptedLocalStore) Delete(objectKey string) error {
	target, err := resolveIssuedPath(s.Root, objectKey, ".pdf.enc")
	if err != nil {
		return err
	}
	removed := false
	if err = os.Remove(target); err == nil {
		removed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if removed {
		return s.syncDirectory(filepath.Dir(target))
	}
	return nil
}

func (s EncryptedLocalStore) syncDirectory(path string) error {
	if s.DirectorySync != nil {
		return s.DirectorySync(path)
	}
	return syncDocumentDirectory(path)
}

func encryptPDFFile(sourcePath, destinationPath, objectKey string, keyring securefields.Keyring) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = destination.Close()
		if !ok {
			_ = os.Remove(destinationPath)
		}
	}()

	gcm, err := documentGCM(keyring.EncryptionKeys[keyring.CurrentKeyID])
	if err != nil {
		return err
	}
	keyID := []byte(keyring.CurrentKeyID)
	if len(keyID) == 0 || len(keyID) > maxDocumentKeyIDBytes {
		return errors.New("invalid document encryption key ID")
	}
	noncePrefix := make([]byte, 8)
	if _, err = rand.Read(noncePrefix); err != nil {
		return err
	}
	if err = writeAll(destination, []byte(encryptedPDFMagic)); err != nil {
		return err
	}
	var short [2]byte
	binary.BigEndian.PutUint16(short[:], uint16(len(keyID)))
	if err = writeAll(destination, short[:]); err != nil {
		return err
	}
	if err = writeAll(destination, keyID); err != nil {
		return err
	}
	if err = writeAll(destination, noncePrefix); err != nil {
		return err
	}

	plain := make([]byte, encryptedPDFChunkBytes)
	var counter uint32
	var length [4]byte
	for {
		read, readErr := source.Read(plain)
		if read > 0 {
			if counter == ^uint32(0) {
				return errors.New("document has too many encryption chunks")
			}
			counter++
			binary.BigEndian.PutUint32(length[:], uint32(read))
			nonce := documentNonce(noncePrefix, counter)
			aad := documentAAD(objectKey, keyring.CurrentKeyID, counter, uint32(read))
			sealed := gcm.Seal(nil, nonce, plain[:read], aad)
			if err = writeAll(destination, length[:]); err != nil {
				return err
			}
			if err = writeAll(destination, sealed); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	binary.BigEndian.PutUint32(length[:], 0)
	if err = writeAll(destination, length[:]); err != nil {
		return err
	}
	if err = destination.Sync(); err != nil {
		return err
	}
	if err = destination.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

type encryptedPDFReader struct {
	file        *os.File
	gcm         cipher.AEAD
	objectKey   string
	keyID       string
	nonce       []byte
	counter     uint32
	buffer      []byte
	finished    bool
	terminalErr error
}

func newEncryptedPDFReader(file *os.File, objectKey string, keyring securefields.Keyring) (*encryptedPDFReader, error) {
	magic := make([]byte, len(encryptedPDFMagic))
	if _, err := io.ReadFull(file, magic); err != nil || string(magic) != encryptedPDFMagic {
		return nil, errors.New("invalid encrypted document header")
	}
	var short [2]byte
	if _, err := io.ReadFull(file, short[:]); err != nil {
		return nil, errors.New("truncated encrypted document key ID")
	}
	keyIDLength := int(binary.BigEndian.Uint16(short[:]))
	if keyIDLength == 0 || keyIDLength > maxDocumentKeyIDBytes {
		return nil, errors.New("invalid encrypted document key ID")
	}
	keyIDBytes := make([]byte, keyIDLength)
	if _, err := io.ReadFull(file, keyIDBytes); err != nil {
		return nil, errors.New("truncated encrypted document key ID")
	}
	keyID := string(keyIDBytes)
	key, ok := keyring.EncryptionKeys[keyID]
	if !ok {
		return nil, errors.New("document encryption key is unavailable")
	}
	gcm, err := documentGCM(key)
	if err != nil {
		return nil, err
	}
	noncePrefix := make([]byte, 8)
	if _, err = io.ReadFull(file, noncePrefix); err != nil {
		return nil, errors.New("truncated encrypted document nonce")
	}
	return &encryptedPDFReader{file: file, gcm: gcm, objectKey: objectKey, keyID: keyID, nonce: noncePrefix}, nil
}

func (r *encryptedPDFReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if len(r.buffer) == 0 && !r.finished && r.terminalErr == nil {
		r.readChunk()
	}
	if len(r.buffer) > 0 {
		read := copy(destination, r.buffer)
		r.buffer = r.buffer[read:]
		return read, nil
	}
	if r.terminalErr != nil {
		return 0, r.terminalErr
	}
	return 0, io.EOF
}

func (r *encryptedPDFReader) readChunk() {
	var length [4]byte
	if _, err := io.ReadFull(r.file, length[:]); err != nil {
		r.terminalErr = errors.New("truncated encrypted document")
		return
	}
	plainLength := binary.BigEndian.Uint32(length[:])
	if plainLength == 0 {
		var extra [1]byte
		read, err := r.file.Read(extra[:])
		if read != 0 || (err != nil && !errors.Is(err, io.EOF)) {
			r.terminalErr = errors.New("encrypted document has trailing data")
			return
		}
		r.finished = true
		return
	}
	if plainLength > encryptedPDFChunkBytes || r.counter == ^uint32(0) {
		r.terminalErr = errors.New("invalid encrypted document chunk")
		return
	}
	r.counter++
	sealed := make([]byte, int(plainLength)+r.gcm.Overhead())
	if _, err := io.ReadFull(r.file, sealed); err != nil {
		r.terminalErr = errors.New("truncated encrypted document chunk")
		return
	}
	opened, err := r.gcm.Open(nil, documentNonce(r.nonce, r.counter), sealed, documentAAD(r.objectKey, r.keyID, r.counter, plainLength))
	if err != nil {
		r.terminalErr = errors.New("encrypted document authentication failed")
		return
	}
	r.buffer = opened
}

func (r *encryptedPDFReader) Close() error { return r.file.Close() }

func documentGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func documentNonce(prefix []byte, counter uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[8:], counter)
	return nonce
}

func documentAAD(objectKey, keyID string, counter, plainLength uint32) []byte {
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%d\n%d", encryptedPDFMagic, objectKey, keyID, counter, plainLength))
}

func resolveIssuedPath(root, objectKey, requiredSuffix string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(objectKey))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || !strings.HasPrefix(filepath.ToSlash(clean), "issued/") || !strings.HasSuffix(clean, requiredSuffix) {
		return "", errors.New("invalid object key")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(absoluteRoot, clean))
	if err != nil {
		return "", err
	}
	if target == absoluteRoot || !strings.HasPrefix(target, absoluteRoot+string(os.PathSeparator)) {
		return "", errors.New("object path escapes storage root")
	}
	return target, nil
}
