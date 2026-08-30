package archive

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
)

type terminalReceipt struct {
	bytes  []byte
	ref    *ArtifactRefV1
	digest string
}

// MemoryReceiptJournal is a process-local test double. The production journal is
// the append-only PostgreSQL tables from migration 000018; this type exists only
// to exercise retry/conflict semantics without a database.
type MemoryReceiptJournal struct {
	mu       sync.RWMutex
	intents  map[uuid.UUID]OperationIntentV1
	puts     map[uuid.UUID]map[int32]ObjectVersionV1
	terminal map[uuid.UUID]terminalReceipt
}

func NewMemoryReceiptJournal() *MemoryReceiptJournal {
	return &MemoryReceiptJournal{
		intents:  make(map[uuid.UUID]OperationIntentV1),
		puts:     make(map[uuid.UUID]map[int32]ObjectVersionV1),
		terminal: make(map[uuid.UUID]terminalReceipt),
	}
}

func (j *MemoryReceiptJournal) BeginIntent(ctx context.Context, value OperationIntentV1) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if j == nil {
		return fmt.Errorf("%w: nil journal", ErrJournalConflict)
	}
	if err := ValidateOperationIntent(value); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if current, ok := j.intents[value.OperationID]; ok {
		if operationIntentEqual(current, value) {
			return nil
		}
		return fmt.Errorf("%w: operation intent bytes changed", ErrJournalConflict)
	}
	j.intents[value.OperationID] = cloneOperationIntent(value)
	j.puts[value.OperationID] = make(map[int32]ObjectVersionV1)
	return nil
}

func operationIntentEqual(left, right OperationIntentV1) bool {
	leftBytes, leftErr := CanonicalOperationIntentBytes(left)
	rightBytes, rightErr := CanonicalOperationIntentBytes(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftBytes) == string(rightBytes)
}

func (j *MemoryReceiptJournal) AppendPutResult(ctx context.Context, operationID uuid.UUID, ordinal int32, value ObjectVersionV1) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if operationID == uuid.Nil || ordinal < 0 {
		return fmt.Errorf("%w: operation/ordinal", ErrArchiveValidation)
	}
	if err := ValidateObjectVersion(value); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	intent, ok := j.intents[operationID]
	if !ok {
		return fmt.Errorf("%w: intent is not durable", ErrJournalConflict)
	}
	if int(ordinal) >= len(intent.Objects) || intent.Objects[ordinal].OperationID != operationID ||
		intent.Objects[ordinal].Ordinal != ordinal || !objectMatchesIntent(value, intent.Objects[ordinal]) {
		return fmt.Errorf("%w: object receipt does not match intent", ErrJournalConflict)
	}
	byOrdinal := j.puts[operationID]
	if current, exists := byOrdinal[ordinal]; exists {
		if current == value {
			return nil
		}
		return fmt.Errorf("%w: object receipt bytes changed", ErrJournalConflict)
	}
	byOrdinal[ordinal] = value
	return nil
}

func (j *MemoryReceiptJournal) AppendTerminalResult(ctx context.Context, operationID uuid.UUID, bytes []byte, ref *ArtifactRefV1, digest string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if operationID == uuid.Nil || len(bytes) == 0 || !isLowerHex64(digest) || sha256Hex(bytes) != digest {
		return fmt.Errorf("%w: terminal receipt fields", ErrArchiveValidation)
	}
	if ref != nil {
		if err := validateArtifactRef(*ref); err != nil {
			return err
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	intent, ok := j.intents[operationID]
	if !ok {
		return fmt.Errorf("%w: intent is not durable", ErrJournalConflict)
	}
	byOrdinal := j.puts[operationID]
	if len(byOrdinal) != len(intent.Objects) {
		return fmt.Errorf("%w: terminal receipt before all object receipts", ErrJournalConflict)
	}
	for index, object := range intent.Objects {
		if _, exists := byOrdinal[int32(index)]; !exists || object.OperationID != operationID {
			return fmt.Errorf("%w: missing object receipt ordinal=%d", ErrJournalConflict, index)
		}
	}
	if current, exists := j.terminal[operationID]; exists {
		if string(current.bytes) == string(bytes) && current.digest == digest && artifactRefEqual(current.ref, ref) {
			return nil
		}
		return fmt.Errorf("%w: terminal receipt bytes changed", ErrJournalConflict)
	}
	j.terminal[operationID] = terminalReceipt{bytes: append([]byte(nil), bytes...), ref: cloneArtifactRef(ref), digest: digest}
	return nil
}

func artifactRefEqual(left, right *ArtifactRefV1) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneArtifactRef(value *ArtifactRefV1) *ArtifactRefV1 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (j *MemoryReceiptJournal) LoadOperation(ctx context.Context, operationID uuid.UUID) (OperationReceiptV1, error) {
	if err := contextErr(ctx); err != nil {
		return OperationReceiptV1{}, err
	}
	if j == nil || operationID == uuid.Nil {
		return OperationReceiptV1{}, fmt.Errorf("%w: operation", ErrJournalConflict)
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	intent, ok := j.intents[operationID]
	if !ok {
		return OperationReceiptV1{}, ErrObjectNotFound
	}
	result := OperationReceiptV1{Intent: cloneOperationIntent(intent)}
	type numberedObject struct {
		ordinal int32
		value   ObjectVersionV1
	}
	objects := make([]numberedObject, 0, len(j.puts[operationID]))
	for ordinal, value := range j.puts[operationID] {
		objects = append(objects, numberedObject{ordinal: ordinal, value: value})
	}
	sort.Slice(objects, func(i, k int) bool { return objects[i].ordinal < objects[k].ordinal })
	for _, object := range objects {
		result.PutResults = append(result.PutResults, object.value)
	}
	if current, ok := j.terminal[operationID]; ok {
		result.TerminalResultBytes = append([]byte(nil), current.bytes...)
		result.TerminalResultRef = cloneArtifactRef(current.ref)
		result.TerminalResultDigest = current.digest
	}
	return cloneReceipt(result), nil
}
