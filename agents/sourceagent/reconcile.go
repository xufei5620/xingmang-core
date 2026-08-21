package sourceagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	reconcileSchemaVersion = 1
	maxReconcileFileBytes  = 256 << 20
	maxReconcileEntries    = 2_000_000
)

type reconcileEntry struct {
	Key                 string `json:"key"`
	EntityType          string `json:"entity_type"`
	TombstoneExternalID string `json:"tombstone_external_id"`
	Misses              int    `json:"misses"`
}

type cursorTransition struct {
	OldRevision uint64     `json:"old_revision"`
	Target      ScanCursor `json:"target"`
}

type reconcileControl struct {
	SchemaVersion   int               `json:"schema_version"`
	SourceID        string            `json:"source_id"`
	StreamID        string            `json:"stream_id"`
	Phase           string            `json:"phase"`
	Mode            ScanMode          `json:"mode,omitempty"`
	PlanIndex       int               `json:"plan_index,omitempty"`
	DeletionBlocked bool              `json:"deletion_blocked,omitempty"`
	PendingCursor   *cursorTransition `json:"pending_cursor,omitempty"`
}

type reconcileSnapshot struct {
	SchemaVersion int              `json:"schema_version"`
	SourceID      string           `json:"source_id"`
	StreamID      string           `json:"stream_id"`
	Entries       []reconcileEntry `json:"entries"`
}

type reconcilePlan struct {
	SchemaVersion int              `json:"schema_version"`
	SourceID      string           `json:"source_id"`
	StreamID      string           `json:"stream_id"`
	Inventory     []reconcileEntry `json:"inventory"`
	Tombstones    []reconcileEntry `json:"tombstones"`
}

// FileReconciler keeps deletion evidence outside the upstream databases.  A
// tombstone is planned only after a complete full/reconcile scan and only
// after the same row was absent from several complete scans.  Incremental or
// failed/partial scans can never increment a miss counter.
type FileReconciler struct {
	Path          string
	SourceID      string
	StreamID      string
	SourceType    string
	MissThreshold int
	Now           func() time.Time
	mu            sync.Mutex
}

type ReconcileCheck struct {
	Phase             string
	InventoryEntries  int
	SeenEntries       int
	PlannedTombstones int
	PlanIndex         int
	PendingCursor     bool
}

func (r *FileReconciler) CheckReadOnly() (ReconcileCheck, error) {
	if err := r.validate(); err != nil {
		return ReconcileCheck{}, err
	}
	control, err := r.loadControl()
	if err != nil {
		return ReconcileCheck{}, err
	}
	inventory, err := r.loadInventory()
	if err != nil {
		return ReconcileCheck{}, err
	}
	seen, err := r.loadSeen()
	if err != nil {
		return ReconcileCheck{}, err
	}
	check := ReconcileCheck{Phase: control.Phase, InventoryEntries: len(inventory), SeenEntries: len(seen),
		PlanIndex: control.PlanIndex, PendingCursor: control.PendingCursor != nil}
	plan, planErr := r.loadPlan()
	if control.Phase == "tombstones" || control.Phase == "finalize" {
		if planErr != nil {
			return ReconcileCheck{}, planErr
		}
		if control.PlanIndex < 0 || control.PlanIndex > len(plan.Tombstones) ||
			control.Phase == "tombstones" && control.PlanIndex >= len(plan.Tombstones) {
			return ReconcileCheck{}, errors.New("reconcile plan progress is inconsistent")
		}
		check.PlannedTombstones = len(plan.Tombstones)
	} else if planErr == nil && control.Phase == "idle" {
		if len(plan.Inventory) != len(inventory) {
			return ReconcileCheck{}, errors.New("idle reconcile plan and inventory differ")
		}
		for _, entry := range plan.Inventory {
			current, ok := inventory[entry.Key]
			if !ok || current != entry {
				return ReconcileCheck{}, errors.New("idle reconcile plan and inventory differ")
			}
		}
		check.PlannedTombstones = len(plan.Tombstones)
	} else if planErr != nil && !errors.Is(planErr, os.ErrNotExist) && control.Phase != "idle" && control.Phase != "scanning" {
		return ReconcileCheck{}, planErr
	}
	if control.Phase == "idle" && len(seen) != 0 {
		return ReconcileCheck{}, errors.New("idle reconcile state has an unfinished seen journal")
	}
	if control.PendingCursor != nil {
		if err = validateStoredFileCursor(control.PendingCursor.Target); err != nil {
			return ReconcileCheck{}, err
		}
	}
	return check, nil
}

func (r *FileReconciler) inventoryPath() string { return r.Path + ".inventory" }
func (r *FileReconciler) seenPath() string      { return r.Path + ".seen" }
func (r *FileReconciler) planPath() string      { return r.Path + ".plan" }

func (r *FileReconciler) validate() error {
	if r == nil || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path ||
		!sourceIDPattern.MatchString(r.SourceID) || (r.StreamID != StreamPayments && r.StreamID != StreamIdentities && r.StreamID != StreamUsage && r.StreamID != StreamCredits && r.StreamID != StreamBalances) ||
		(r.SourceType != SourceSub2API && r.SourceType != SourceNewAPI) || r.MissThreshold < 2 || r.MissThreshold > 10 {
		return errors.New("reconcile store configuration is invalid")
	}
	info, err := os.Lstat(filepath.Dir(r.Path))
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !secureStateDirectoryPermissions(info) {
		return errors.New("reconcile state directory is missing or unsafe")
	}
	return nil
}

func InitializeFileReconciler(ctx context.Context, r *FileReconciler) error {
	if err := r.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	for _, path := range []string{r.Path, r.inventoryPath(), r.seenPath(), r.planPath()} {
		if _, err := os.Lstat(path); err == nil {
			return errors.New("reconcile state already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect reconcile state failed")
		}
	}
	control := reconcileControl{SchemaVersion: reconcileSchemaVersion, SourceID: r.SourceID, StreamID: r.StreamID, Phase: "idle"}
	if err := r.writeJSON(r.Path, control); err != nil {
		return err
	}
	snapshot := reconcileSnapshot{SchemaVersion: reconcileSchemaVersion, SourceID: r.SourceID, StreamID: r.StreamID, Entries: []reconcileEntry{}}
	if err := r.writeJSON(r.inventoryPath(), snapshot); err != nil {
		return err
	}
	return r.replaceEmpty(r.seenPath())
}

func (r *FileReconciler) RecoverCursor(ctx context.Context, cursors CursorStore, cursor ScanCursor) (ScanCursor, error) {
	if err := r.validate(); err != nil {
		return ScanCursor{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	control, err := r.loadControl()
	if err != nil {
		return ScanCursor{}, err
	}
	if control.PendingCursor != nil {
		pending := control.PendingCursor
		switch {
		case cursor.Revision == pending.OldRevision:
			matched, casErr := cursors.CompareAndSwap(ctx, r.SourceID, cursor, pending.Target)
			if casErr != nil || !matched {
				if casErr == nil {
					casErr = errors.New("pending reconcile cursor CAS did not match")
				}
				return ScanCursor{}, casErr
			}
			cursor, err = cursors.Load(ctx, r.SourceID)
			if err != nil {
				return ScanCursor{}, err
			}
		case cursor.Revision == pending.OldRevision+1 && sameCursorPosition(cursor, pending.Target):
			// The process crashed after the cursor CAS but before clearing the
			// durable transition. The acknowledged page is already complete.
		default:
			return ScanCursor{}, errors.New("reconcile cursor transition conflicts with durable cursor")
		}
		control.PendingCursor = nil
		if err = r.writeJSON(r.Path, control); err != nil {
			return ScanCursor{}, err
		}
	}
	if control.Phase == "finalize" {
		if err = r.finalizeLocked(&control); err != nil {
			return ScanCursor{}, err
		}
	}
	return cursor, nil
}

func sameCursorPosition(left, right ScanCursor) bool {
	left.Revision = 0
	right.Revision = 0
	return left == right
}

func (r *FileReconciler) StartCycle(mode ScanMode) error {
	if mode != ScanFull && mode != ScanReconcile {
		return errors.New("only complete scans can start reconciliation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	control, err := r.loadControl()
	if err != nil {
		return err
	}
	if control.Phase == "idle" {
		if err = r.replaceEmpty(r.seenPath()); err != nil {
			return err
		}
		control.Phase = "scanning"
		control.Mode = mode
		control.PlanIndex = 0
		control.DeletionBlocked = false
		return r.writeJSON(r.Path, control)
	}
	if control.Phase != "scanning" || control.Mode != mode {
		return errors.New("reconcile scan mode conflicts with durable cycle")
	}
	return nil
}

func (r *FileReconciler) SyntheticPage(limit int) (ScanPage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	control, err := r.loadControl()
	if err != nil {
		return ScanPage{}, false, err
	}
	if control.Phase != "tombstones" {
		return ScanPage{}, false, nil
	}
	plan, err := r.loadPlan()
	if err != nil {
		return ScanPage{}, false, err
	}
	if control.PlanIndex < 0 || control.PlanIndex >= len(plan.Tombstones) {
		return ScanPage{}, false, errors.New("reconcile tombstone plan index is invalid")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	end := control.PlanIndex + limit
	if end > len(plan.Tombstones) {
		end = len(plan.Tombstones)
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	page := ScanPage{Projections: make([]Projection, 0, end-control.PlanIndex), HasMore: end < len(plan.Tombstones)}
	for _, entry := range plan.Tombstones[control.PlanIndex:end] {
		page.Projections = append(page.Projections, Projection{
			EntityType: entry.EntityType, ExternalID: entry.Key, Operation: "tombstone",
			ObservedAt: now.Format(time.RFC3339Nano),
			Payload: TombstonePayload{ExternalID: entry.TombstoneExternalID,
				Reason: fmt.Sprintf("missing_after_%d_complete_scans", r.MissThreshold), ConfirmedAt: now.Format(time.RFC3339Nano)},
		})
	}
	return page, true, nil
}

// CommitAcknowledgedPage records source rows only after the exact signed batch
// was acknowledged.  It durably records a pending cursor transition before
// the caller advances the cursor, allowing crash recovery in either order.
func (r *FileReconciler) CommitAcknowledgedPage(mode ScanMode, oldCursor, target ScanCursor, page ScanPage, synthetic bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	control, err := r.loadControl()
	if err != nil {
		return false, err
	}
	if control.PendingCursor != nil {
		return false, errors.New("previous reconcile cursor transition is still pending")
	}
	cycleComplete := !page.HasMore
	if synthetic {
		if control.Phase != "tombstones" {
			return false, errors.New("unexpected synthetic reconcile page")
		}
		control.PlanIndex += len(page.Projections)
		plan, loadErr := r.loadPlan()
		if loadErr != nil {
			return false, loadErr
		}
		if control.PlanIndex > len(plan.Tombstones) {
			return false, errors.New("tombstone acknowledgement exceeds durable plan")
		}
		if control.PlanIndex == len(plan.Tombstones) {
			control.Phase = "finalize"
			cycleComplete = true
		} else {
			cycleComplete = false
		}
	} else if mode == ScanFull || mode == ScanReconcile {
		if control.Phase != "scanning" || control.Mode != mode {
			return false, errors.New("acknowledged source page is outside its reconcile cycle")
		}
		entries, entryErr := r.entriesForPage(page)
		if entryErr != nil {
			return false, entryErr
		}
		if entryErr = r.appendSeen(entries); entryErr != nil {
			return false, entryErr
		}
		control.DeletionBlocked = control.DeletionBlocked || page.ReconcileBlocked
		if !page.HasMore {
			plan, planErr := r.buildPlan(control.DeletionBlocked)
			if planErr != nil {
				return false, planErr
			}
			if planErr = r.writeJSON(r.planPath(), plan); planErr != nil {
				return false, planErr
			}
			control.PlanIndex = 0
			if len(plan.Tombstones) > 0 {
				control.Phase = "tombstones"
				cycleComplete = false
			} else {
				control.Phase = "finalize"
				cycleComplete = true
			}
		}
	}
	transitionTarget := target
	transitionTarget.Revision = oldCursor.Revision
	control.PendingCursor = &cursorTransition{OldRevision: oldCursor.Revision, Target: transitionTarget}
	if err = r.writeJSON(r.Path, control); err != nil {
		return false, err
	}
	return cycleComplete, nil
}

func (r *FileReconciler) CursorCommitted() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	control, err := r.loadControl()
	if err != nil {
		return err
	}
	if control.PendingCursor == nil {
		return errors.New("reconcile cursor commit has no pending transition")
	}
	control.PendingCursor = nil
	if err = r.writeJSON(r.Path, control); err != nil {
		return err
	}
	if control.Phase == "finalize" {
		return r.finalizeLocked(&control)
	}
	return nil
}

func (r *FileReconciler) finalizeLocked(control *reconcileControl) error {
	plan, err := r.loadPlan()
	if err != nil {
		return err
	}
	snapshot := reconcileSnapshot{SchemaVersion: reconcileSchemaVersion, SourceID: r.SourceID, StreamID: r.StreamID, Entries: plan.Inventory}
	if err = r.writeJSON(r.inventoryPath(), snapshot); err != nil {
		return err
	}
	if err = r.replaceEmpty(r.seenPath()); err != nil {
		return err
	}
	control.Phase = "idle"
	control.Mode = ""
	control.PlanIndex = 0
	control.DeletionBlocked = false
	control.PendingCursor = nil
	if err = r.writeJSON(r.Path, *control); err != nil {
		return err
	}
	return nil
}

func (r *FileReconciler) entriesForPage(page ScanPage) ([]reconcileEntry, error) {
	entries := make([]reconcileEntry, 0, len(page.Projections))
	for _, projection := range page.Projections {
		if projection.Operation == "tombstone" {
			continue
		}
		entry := reconcileEntry{Key: projection.EntityType + "/" + projection.ExternalID}
		switch {
		case r.StreamID == "identities" && projection.EntityType == EntityIdentityBinding:
			entry.EntityType = EntityIdentityBinding
			body, err := json.Marshal(projection.Payload)
			if err != nil {
				return nil, err
			}
			var payload struct {
				ExternalUserID string `json:"external_user_id"`
			}
			if err = json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.ExternalUserID) == "" {
				return nil, errors.New("identity projection is missing its external user ID")
			}
			entry.TombstoneExternalID = payload.ExternalUserID
		case r.StreamID == "payments" && r.SourceType == SourceSub2API && projection.EntityType == EntityPaymentOrder:
			entry.EntityType = EntityPaymentOrder
			entry.TombstoneExternalID = projection.ExternalID
		case r.StreamID == "payments" && r.SourceType == SourceNewAPI && projection.EntityType == EntityPaymentCandidate:
			// The downstream safety action is the same funding-lot revocation;
			// payment_order is the protocol tombstone entity for both sources.
			entry.EntityType = EntityPaymentOrder
			entry.TombstoneExternalID = projection.ExternalID
		default:
			continue
		}
		if len(entry.Key) > 1024 || len(entry.TombstoneExternalID) > 512 {
			return nil, errors.New("reconcile identity exceeds bounded storage")
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (r *FileReconciler) buildPlan(deletionBlocked bool) (reconcilePlan, error) {
	prior, err := r.loadInventory()
	if err != nil {
		return reconcilePlan{}, err
	}
	seen, err := r.loadSeen()
	if err != nil {
		return reconcilePlan{}, err
	}
	for key, entry := range seen {
		entry.Misses = 0
		prior[key] = entry
	}
	tombstones := make([]reconcileEntry, 0)
	for key, entry := range prior {
		if _, ok := seen[key]; ok {
			continue
		}
		if deletionBlocked {
			continue
		}
		entry.Misses++
		if entry.Misses >= r.MissThreshold {
			tombstones = append(tombstones, entry)
			delete(prior, key)
			continue
		}
		prior[key] = entry
	}
	inventory := make([]reconcileEntry, 0, len(prior))
	for _, entry := range prior {
		inventory = append(inventory, entry)
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i].Key < inventory[j].Key })
	sort.Slice(tombstones, func(i, j int) bool { return tombstones[i].Key < tombstones[j].Key })
	return reconcilePlan{SchemaVersion: reconcileSchemaVersion, SourceID: r.SourceID, StreamID: r.StreamID, Inventory: inventory, Tombstones: tombstones}, nil
}

func (r *FileReconciler) appendSeen(entries []reconcileEntry) error {
	if len(entries) == 0 {
		return nil
	}
	info, err := os.Lstat(r.seenPath())
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!secureStatePermissions(info) || info.Size() < 0 || info.Size() > maxReconcileFileBytes-int64(len(entries)*2048) {
		return errors.New("reconcile seen journal is missing, unsafe, or full")
	}
	file, err := os.OpenFile(r.seenPath(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return errors.New("open reconcile seen journal failed")
	}
	encoder := json.NewEncoder(file)
	for _, entry := range entries {
		if err = encoder.Encode(entry); err != nil {
			_ = file.Close()
			return errors.New("append reconcile seen journal failed")
		}
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("fsync reconcile seen journal failed")
	}
	return file.Close()
}

func (r *FileReconciler) loadSeen() (map[string]reconcileEntry, error) {
	info, err := os.Lstat(r.seenPath())
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!secureStatePermissions(info) || info.Size() < 0 || info.Size() > maxReconcileFileBytes {
		return nil, errors.New("reconcile seen journal is missing or unsafe")
	}
	file, err := os.Open(r.seenPath())
	if err != nil {
		return nil, errors.New("open reconcile seen journal failed")
	}
	defer file.Close()
	seen := map[string]reconcileEntry{}
	scanner := bufio.NewScanner(io.LimitReader(file, maxReconcileFileBytes+1))
	scanner.Buffer(make([]byte, 4096), 4096)
	for scanner.Scan() {
		var entry reconcileEntry
		if err = json.Unmarshal(scanner.Bytes(), &entry); err != nil || entry.Key == "" || entry.EntityType == "" || entry.TombstoneExternalID == "" {
			return nil, errors.New("decode reconcile seen journal failed")
		}
		seen[entry.Key] = entry
		if len(seen) > maxReconcileEntries {
			return nil, errors.New("reconcile seen inventory exceeds limit")
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, errors.New("read reconcile seen journal failed")
	}
	return seen, nil
}

func (r *FileReconciler) loadInventory() (map[string]reconcileEntry, error) {
	var snapshot reconcileSnapshot
	if err := r.readJSON(r.inventoryPath(), &snapshot); err != nil {
		return nil, err
	}
	if snapshot.SchemaVersion != reconcileSchemaVersion || snapshot.SourceID != r.SourceID || snapshot.StreamID != r.StreamID || len(snapshot.Entries) > maxReconcileEntries {
		return nil, errors.New("reconcile inventory identity is invalid")
	}
	out := make(map[string]reconcileEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if entry.Key == "" || entry.EntityType == "" || entry.TombstoneExternalID == "" || entry.Misses < 0 || entry.Misses >= r.MissThreshold {
			return nil, errors.New("reconcile inventory entry is invalid")
		}
		if _, duplicate := out[entry.Key]; duplicate {
			return nil, errors.New("duplicate reconcile inventory entry")
		}
		out[entry.Key] = entry
	}
	return out, nil
}

func (r *FileReconciler) loadControl() (reconcileControl, error) {
	var control reconcileControl
	if err := r.readJSON(r.Path, &control); err != nil {
		return reconcileControl{}, err
	}
	if control.SchemaVersion != reconcileSchemaVersion || control.SourceID != r.SourceID || control.StreamID != r.StreamID ||
		(control.Phase != "idle" && control.Phase != "scanning" && control.Phase != "tombstones" && control.Phase != "finalize") {
		return reconcileControl{}, errors.New("reconcile control identity is invalid")
	}
	return control, nil
}

func (r *FileReconciler) loadPlan() (reconcilePlan, error) {
	var plan reconcilePlan
	if err := r.readJSON(r.planPath(), &plan); err != nil {
		return reconcilePlan{}, err
	}
	if plan.SchemaVersion != reconcileSchemaVersion || plan.SourceID != r.SourceID || plan.StreamID != r.StreamID ||
		len(plan.Inventory) > maxReconcileEntries || len(plan.Tombstones) > maxReconcileEntries {
		return reconcilePlan{}, errors.New("reconcile plan identity is invalid")
	}
	return plan, nil
}

func (r *FileReconciler) readJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !secureStatePermissions(info) || info.Size() <= 0 || info.Size() > maxReconcileFileBytes {
		return errors.New("reconcile state file is missing or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("open reconcile state failed")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxReconcileFileBytes+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return errors.New("decode reconcile state failed")
	}
	if err = ensureDecodeEOF(decoder); err != nil {
		return errors.New("decode reconcile state failed")
	}
	return nil
}

func (r *FileReconciler) writeJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > maxReconcileFileBytes {
		return errors.New("encode reconcile state failed")
	}
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".source-reconcile-*.tmp")
	if err != nil {
		return errors.New("create reconcile temporary file failed")
	}
	tempPath := temp.Name()
	keep := true
	defer func() {
		_ = temp.Close()
		if keep {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(raw)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("persist reconcile temporary file failed")
	}
	if err = atomicReplaceFile(tempPath, path); err != nil {
		return err
	}
	keep = false
	if err = os.Chmod(path, 0o600); err != nil {
		return errors.New("set reconcile state permissions failed")
	}
	return syncStateDirectory(directory)
}

func (r *FileReconciler) replaceEmpty(path string) error {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".source-reconcile-empty-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err = temp.Chmod(0o600); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err = atomicReplaceFile(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err = os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncStateDirectory(directory)
}

// DurableSyncCoordinator adds repeated-miss reconciliation to the existing
// signed publisher without weakening ACK-before-cursor semantics.
type DurableSyncCoordinator struct {
	SourceID  string
	Connector Connector
	Cursors   CursorStore
	Publisher *Publisher
	Reconcile *FileReconciler
	Limit     int
}

func (c *DurableSyncCoordinator) SyncPage(ctx context.Context, mode ScanMode) (ScanPage, IngestAck, error) {
	if c == nil || !sourceIDPattern.MatchString(c.SourceID) || c.Connector == nil || c.Cursors == nil || c.Publisher == nil || c.Reconcile == nil {
		return ScanPage{}, IngestAck{}, errors.New("durable sync coordinator is not configured")
	}
	cursor, err := c.Cursors.Load(ctx, c.SourceID)
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	cursor, err = c.Reconcile.RecoverCursor(ctx, c.Cursors, cursor)
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	page, synthetic, err := c.Reconcile.SyntheticPage(c.Limit)
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	if !synthetic {
		if mode == ScanFull || mode == ScanReconcile {
			if err = c.Reconcile.StartCycle(mode); err != nil {
				return ScanPage{}, IngestAck{}, err
			}
		}
		page, err = c.Connector.Scan(ctx, ScanRequest{Mode: mode, Cursor: cursor, Limit: c.Limit})
		if err != nil {
			return ScanPage{}, IngestAck{}, err
		}
	}
	if page.ReconcileBlocked {
		c.Publisher.Builder.ProjectionStatus = "blocked"
	} else {
		c.Publisher.Builder.ProjectionStatus = "healthy"
	}
	target := page.NextCursor
	if synthetic {
		target = cursor
	}
	receipt, err := c.Publisher.PublishPage(ctx, cursor, target, page.Projections)
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	complete, err := c.Reconcile.CommitAcknowledgedPage(mode, cursor, receipt.CursorAfter, page, synthetic)
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	committed, err := c.Cursors.CompareAndSwap(ctx, c.SourceID, cursor, receipt.CursorAfter)
	if err != nil || !committed {
		if err == nil {
			err = errors.New("source cursor changed concurrently")
		}
		return ScanPage{}, IngestAck{}, err
	}
	if err = c.Reconcile.CursorCommitted(); err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	if err = c.Publisher.FinalizePage(ctx, receipt); err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	page.HasMore = !complete
	page.NextCursor = receipt.CursorAfter
	return page, receipt.Ack, nil
}
