package sourceagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	fileStateSchemaVersion = 1
	maxFileStateBytes      = 64 << 10
	fileLockWait           = 5 * time.Second
)

// FileStateStore persists sender-only state on a dedicated agent volume. It
// contains no credential or source row. Use its Cursor() and Sequence()
// adapters; both share one lock and one atomic state file.
type FileStateStore struct {
	Path     string
	SourceID string
	StreamID string
	mu       sync.Mutex
}

type FileCursorStore struct{ State *FileStateStore }
type FileSequenceStore struct{ State *FileStateStore }

// CheckReadOnly validates and loads the state envelope without creating a lock
// file or changing any byte. It is safe only after the agent has been stopped;
// a pre-existing lock is rejected by the caller as an unclean backup.
func (s *FileStateStore) CheckReadOnly() (ScanCursor, SequenceState, error) {
	if err := s.validateConfiguration(); err != nil {
		return ScanCursor{}, SequenceState{}, err
	}
	envelope, err := s.readUnlocked()
	if err != nil {
		return ScanCursor{}, SequenceState{}, err
	}
	cursor := envelope.Cursor
	cursor.Revision = envelope.CursorRevision
	sequence := SequenceState{Revision: envelope.PublishRevision, Sequence: envelope.Sequence, LastBatchHash: envelope.LastBatchHash}
	if err = validateStoredFileCursorForStream(cursor, s.StreamID); err != nil {
		return ScanCursor{}, SequenceState{}, err
	}
	if err = validateFileSequenceState(sequence); err != nil {
		return ScanCursor{}, SequenceState{}, err
	}
	return cursor, sequence, nil
}

var _ CursorStore = FileCursorStore{}
var _ SequenceStore = FileSequenceStore{}

type fileStateEnvelope struct {
	SchemaVersion   int        `json:"schema_version"`
	SourceID        string     `json:"source_id"`
	StreamID        string     `json:"stream_id"`
	CursorRevision  uint64     `json:"cursor_revision"`
	Cursor          ScanCursor `json:"cursor"`
	PublishRevision uint64     `json:"publish_revision"`
	Sequence        uint64     `json:"sequence"`
	LastBatchHash   string     `json:"last_batch_hash"`
	// LastReconcileAt and LastFullAt (XM-INV-AGENT-RESTART-GRACE) persist the
	// SyncRunner reconcile/full-scan schedule so a process restart resumes it
	// instead of forcing an immediate ScanReconcile/ScanFull cycle. Both are
	// RFC3339Nano UTC timestamps; empty means "never completed". A state file
	// written before this field existed decodes both as empty strings, which
	// reproduces today's always-forced-first-cycle behavior exactly once,
	// after which the new schedule is persisted going forward.
	LastReconcileAt string `json:"last_reconcile_at,omitempty"`
	LastFullAt      string `json:"last_full_at,omitempty"`
}

// InitializeFileState explicitly creates an empty state file. It refuses to
// overwrite existing state; production startup must call Load and fail closed
// if the mounted volume/file is missing.
func InitializeFileState(ctx context.Context, state *FileStateStore) error {
	if err := state.validateConfiguration(); err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.withProcessLock(ctx, func() error {
		if _, err := os.Lstat(state.Path); err == nil {
			return errors.New("agent state file already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect agent state file failed")
		}
		return state.writeLocked(fileStateEnvelope{
			SchemaVersion: fileStateSchemaVersion,
			SourceID:      state.SourceID,
			StreamID:      state.StreamID,
		})
	})
}

func (s FileCursorStore) Load(ctx context.Context, sourceID string) (ScanCursor, error) {
	if s.State == nil || sourceID != s.State.SourceID {
		return ScanCursor{}, ErrSourceMismatch
	}
	var cursor ScanCursor
	err := s.State.readLocked(ctx, func(envelope fileStateEnvelope) error {
		cursor = envelope.Cursor
		cursor.Revision = envelope.CursorRevision
		return validateStoredFileCursorForStream(cursor, s.State.StreamID)
	})
	return cursor, err
}

func (s FileCursorStore) CompareAndSwap(ctx context.Context, sourceID string, oldCursor, newCursor ScanCursor) (bool, error) {
	if s.State == nil || sourceID != s.State.SourceID {
		return false, ErrSourceMismatch
	}
	if err := validateStoredFileCursorForStream(oldCursor, s.State.StreamID); err != nil {
		return false, err
	}
	if err := validateStoredFileCursorForStream(newCursor, s.State.StreamID); err != nil {
		return false, err
	}
	if newCursor.Revision != oldCursor.Revision {
		return false, errors.New("new cursor must retain the loaded CAS revision")
	}
	s.State.mu.Lock()
	defer s.State.mu.Unlock()
	matched := false
	err := s.State.withProcessLock(ctx, func() error {
		envelope, err := s.State.readUnlocked()
		if err != nil {
			return err
		}
		current := envelope.Cursor
		current.Revision = envelope.CursorRevision
		if current != oldCursor {
			return nil
		}
		newCursor.Revision = 0
		envelope.Cursor = newCursor
		envelope.CursorRevision++
		if err := s.State.writeLocked(envelope); err != nil {
			return err
		}
		matched = true
		return nil
	})
	return matched, err
}

func (s FileSequenceStore) Load(ctx context.Context) (SequenceState, error) {
	if s.State == nil {
		return SequenceState{}, errors.New("file sequence store is not configured")
	}
	var state SequenceState
	err := s.State.readLocked(ctx, func(envelope fileStateEnvelope) error {
		state = SequenceState{
			Revision: envelope.PublishRevision, Sequence: envelope.Sequence,
			LastBatchHash: envelope.LastBatchHash,
		}
		return validateFileSequenceState(state)
	})
	return state, err
}

func (s FileSequenceStore) CompareAndSwap(ctx context.Context, oldState, newState SequenceState) (bool, error) {
	if s.State == nil {
		return false, errors.New("file sequence store is not configured")
	}
	if err := validateFileSequenceState(oldState); err != nil {
		return false, err
	}
	if newState.Revision != oldState.Revision || newState.Sequence != oldState.Sequence+1 || !hexHashPattern.MatchString(newState.LastBatchHash) {
		return false, errors.New("invalid file publish-state transition")
	}
	s.State.mu.Lock()
	defer s.State.mu.Unlock()
	matched := false
	err := s.State.withProcessLock(ctx, func() error {
		envelope, err := s.State.readUnlocked()
		if err != nil {
			return err
		}
		current := SequenceState{
			Revision: envelope.PublishRevision, Sequence: envelope.Sequence,
			LastBatchHash: envelope.LastBatchHash,
		}
		if current != oldState {
			return nil
		}
		envelope.Sequence = newState.Sequence
		envelope.LastBatchHash = newState.LastBatchHash
		envelope.PublishRevision++
		if err := s.State.writeLocked(envelope); err != nil {
			return err
		}
		matched = true
		return nil
	})
	return matched, err
}

// ScheduleState is the SyncRunner reconcile/full-scan schedule
// (XM-INV-AGENT-RESTART-GRACE). Both timestamps are RFC3339Nano UTC strings;
// empty means the corresponding cycle kind has never completed.
type ScheduleState struct {
	LastReconcileAt string
	LastFullAt      string
}

// ScheduleStore is implemented by FileScheduleStore in production. SyncRunner
// treats a nil ScheduleStore as "no persistence available" and keeps its
// pre-existing in-memory-only behavior (always forces an initial cycle).
type ScheduleStore interface {
	Load(context.Context) (ScheduleState, error)
	Save(context.Context, ScheduleState) error
}

// FileScheduleStore shares its underlying envelope, mutex and process lock
// with FileCursorStore/FileSequenceStore over the same FileStateStore -- all
// three read-modify-write the one JSON file sequentially within one process,
// so a save from one never clobbers fields owned by the others.
type FileScheduleStore struct{ State *FileStateStore }

var _ ScheduleStore = FileScheduleStore{}

func (s FileScheduleStore) Load(ctx context.Context) (ScheduleState, error) {
	if s.State == nil {
		return ScheduleState{}, errors.New("file schedule store is not configured")
	}
	var state ScheduleState
	err := s.State.readLocked(ctx, func(envelope fileStateEnvelope) error {
		state = ScheduleState{LastReconcileAt: envelope.LastReconcileAt, LastFullAt: envelope.LastFullAt}
		return validateFileScheduleState(state)
	})
	return state, err
}

func (s FileScheduleStore) Save(ctx context.Context, state ScheduleState) error {
	if s.State == nil {
		return errors.New("file schedule store is not configured")
	}
	if err := validateFileScheduleState(state); err != nil {
		return err
	}
	s.State.mu.Lock()
	defer s.State.mu.Unlock()
	return s.State.withProcessLock(ctx, func() error {
		envelope, err := s.State.readUnlocked()
		if err != nil {
			return err
		}
		envelope.LastReconcileAt = state.LastReconcileAt
		envelope.LastFullAt = state.LastFullAt
		return s.State.writeLocked(envelope)
	})
}

func validateFileScheduleState(state ScheduleState) error {
	for _, value := range []string{state.LastReconcileAt, state.LastFullAt} {
		if value == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return errors.New("stored reconcile/full schedule timestamp is invalid")
		}
	}
	return nil
}

func (s *FileStateStore) readLocked(ctx context.Context, use func(fileStateEnvelope) error) error {
	if s == nil {
		return errors.New("file state store is not configured")
	}
	if err := s.validateConfiguration(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withProcessLock(ctx, func() error {
		envelope, err := s.readUnlocked()
		if err != nil {
			return err
		}
		return use(envelope)
	})
}

func (s *FileStateStore) readUnlocked() (fileStateEnvelope, error) {
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStateEnvelope{}, errors.New("agent state file is missing; state volume is not initialized")
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fileStateEnvelope{}, errors.New("agent state path is not a regular file")
	}
	if !secureStatePermissions(info) {
		return fileStateEnvelope{}, errors.New("agent state file permissions are broader than 0600")
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return fileStateEnvelope{}, errors.New("open agent state file failed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxFileStateBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return fileStateEnvelope{}, errors.New("read agent state file failed")
	}
	if len(raw) == 0 || len(raw) > maxFileStateBytes {
		return fileStateEnvelope{}, errors.New("agent state file size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope fileStateEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fileStateEnvelope{}, errors.New("decode agent state file failed")
	}
	if err := ensureDecodeEOF(decoder); err != nil {
		return fileStateEnvelope{}, errors.New("decode agent state file failed")
	}
	if envelope.SchemaVersion != fileStateSchemaVersion || envelope.SourceID != s.SourceID || envelope.StreamID != s.StreamID {
		return fileStateEnvelope{}, errors.New("agent state source or stream mismatch")
	}
	return envelope, nil
}

func (s *FileStateStore) writeLocked(envelope fileStateEnvelope) error {
	if envelope.SchemaVersion != fileStateSchemaVersion || envelope.SourceID != s.SourceID || envelope.StreamID != s.StreamID {
		return errors.New("refuse to write mismatched agent state")
	}
	raw, err := json.Marshal(envelope)
	if err != nil || len(raw) > maxFileStateBytes {
		return errors.New("encode agent state failed")
	}
	directory := filepath.Dir(s.Path)
	temp, err := os.CreateTemp(directory, ".source-agent-state-*.tmp")
	if err != nil {
		return errors.New("create temporary agent state failed")
	}
	tempPath := temp.Name()
	keepTemp := true
	defer func() {
		_ = temp.Close()
		if keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return errors.New("set temporary agent state permissions failed")
	}
	if _, err := temp.Write(raw); err != nil {
		return errors.New("write temporary agent state failed")
	}
	if err := temp.Sync(); err != nil {
		return errors.New("fsync temporary agent state failed")
	}
	if err := temp.Close(); err != nil {
		return errors.New("close temporary agent state failed")
	}
	if err := atomicReplaceFile(tempPath, s.Path); err != nil {
		return err
	}
	keepTemp = false
	if err := os.Chmod(s.Path, 0o600); err != nil {
		return errors.New("set agent state permissions failed")
	}
	if err := syncStateDirectory(directory); err != nil {
		return err
	}
	return nil
}

func (s *FileStateStore) withProcessLock(ctx context.Context, action func() error) error {
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
				return errors.New("set agent state lock permissions failed")
			}
			if syncErr := lock.Sync(); syncErr != nil {
				_ = lock.Close()
				_ = os.Remove(lockPath)
				return errors.New("fsync agent state lock failed")
			}
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return errors.New("close agent state lock failed")
			}
			actionErr := action()
			removeErr := os.Remove(lockPath)
			if actionErr != nil {
				return actionErr
			}
			if removeErr != nil {
				return errors.New("remove agent state lock failed")
			}
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return errors.New("create agent state lock failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("agent state lock is held or stale; fail closed")
		case <-ticker.C:
		}
	}
}

func (s *FileStateStore) validateConfiguration() error {
	if s == nil || !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path || !sourceIDPattern.MatchString(s.SourceID) || !streamIDPattern.MatchString(s.StreamID) {
		return errors.New("file state store configuration is invalid")
	}
	directory := filepath.Dir(s.Path)
	info, err := os.Lstat(directory)
	if err != nil || !secureStateDirectoryPermissions(info) || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("agent state volume directory is missing or unsafe")
	}
	return nil
}

func validateStoredFileCursor(cursor ScanCursor) error {
	if cursor.Version != 0 && cursor.Version != 1 && cursor.Version != 2 {
		return errors.New("stored cursor version is unsupported")
	}
	if cursor.ID < 0 || cursor.Page < 0 || cursor.BoundaryID < 0 {
		return errors.New("stored cursor values must not be negative")
	}
	if cursor.UpdatedAt != "" {
		if _, err := parseCursorTime(cursor.UpdatedAt); err != nil {
			return err
		}
	}
	for _, value := range []string{cursor.CutoverAt, cursor.WatermarkAt, cursor.CeilingAt} {
		if value != "" {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return errors.New("stored V3 cursor time is invalid")
			}
		}
	}
	for _, value := range []string{cursor.Domain, cursor.WatermarkCursor, cursor.CeilingCursor, cursor.PositionCursor, cursor.SnapshotID, cursor.ScanCycleID, cursor.ReconcileBaselineCursor} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("stored V3 cursor metadata is invalid")
		}
	}
	if cursor.HasSnapshotMetadata != (cursor.SnapshotID != "") {
		return errors.New("stored snapshot identity and row count must be supplied together")
	}
	if cursor.HasSnapshotMetadata && (cursor.SnapshotRowCount < 0 || cursor.SnapshotRowCount > balanceSnapshotMaxRows) {
		return errors.New("stored snapshot row count is invalid")
	}
	if cursor.Version == 2 {
		if cursor.CutoverAt == "" || cursor.WatermarkAt == "" || cursor.WatermarkCursor == "" || cursor.CeilingAt == "" || cursor.CeilingCursor == "" || cursor.PositionCursor == "" {
			return errors.New("stored V3 cursor is incomplete")
		}
		if !uuidPattern.MatchString(cursor.ScanCycleID) {
			return errors.New("stored V3 scan cycle id is invalid")
		}
		watermark, _ := time.Parse(time.RFC3339Nano, cursor.WatermarkAt)
		ceiling, _ := time.Parse(time.RFC3339Nano, cursor.CeilingAt)
		if watermark.After(ceiling) {
			return errors.New("stored V3 watermark exceeds ceiling")
		}
	}
	return nil
}

func validateStoredFileCursorForStream(cursor ScanCursor, streamID string) error {
	if err := validateStoredFileCursor(cursor); err != nil {
		return err
	}
	if cursor.Version != 2 {
		return nil
	}
	if streamID == StreamBalances {
		if !cursor.HasSnapshotMetadata || !hexHashPattern.MatchString(cursor.SnapshotID) {
			return errors.New("stored balances cursor is missing durable snapshot metadata")
		}
		return nil
	}
	if cursor.HasSnapshotMetadata || cursor.SnapshotID != "" || cursor.SnapshotRowCount != 0 {
		return errors.New("stored non-balance cursor carried snapshot metadata")
	}
	if streamID != StreamUsage && (cursor.ReconcileBaselineCursor != "" || cursor.ReconcileWindowBounded) {
		return errors.New("stored cursor carried usage-only reconcile window metadata")
	}
	return nil
}

func validateFileSequenceState(state SequenceState) error {
	if state.Sequence == 0 {
		if state.LastBatchHash != "" {
			return errors.New("sequence zero requires an empty predecessor hash")
		}
		return nil
	}
	if !hexHashPattern.MatchString(state.LastBatchHash) {
		return errors.New("published sequence requires a valid predecessor hash")
	}
	return nil
}

var streamIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
