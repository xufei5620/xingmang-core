package savedviews

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/savedviews/gen"
)

var (
	ErrNotFound      = errors.New("saved_view_not_found")
	ErrQuotaExceeded = errors.New("saved_view_quota_exceeded")
	ErrStore         = errors.New("saved_view_store_failed")
	ErrCorrupt       = errors.New("saved_view_stored_state_invalid")
)

type SetResult struct {
	BeforeHash *string
	After      SavedView
}

type RemoveResult struct {
	ID         uuid.UUID
	BeforeHash string
}

type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

func validateOwner(owner Owner) error {
	if strings.TrimSpace(owner.Issuer) == "" || strings.TrimSpace(owner.Subject) == "" ||
		strings.TrimSpace(owner.IdentityZone) == "" || strings.TrimSpace(owner.Environment) == "" {
		return ErrInvalidOwner
	}
	return nil
}

// ownerLockKey is server-derived. Length prefixes prevent field-boundary ambiguity before the
// SHA-256 reduction ("ab"+"c" must not equal "a"+"bc"). The 64-bit advisory key is used only
// for serialization; the full owner tuple remains the authorization and unique-key boundary.
func ownerLockKey(owner Owner) int64 {
	h := sha256.New()
	for _, field := range []string{owner.Issuer, owner.Subject, owner.IdentityZone, owner.Environment} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(field))
	}
	sum := h.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func fromTimestamp(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func rowToSavedView(row gen.UiSavedView) (SavedView, error) {
	filters, err := DecodeFiltersJSON(row.Filters)
	if err != nil {
		return SavedView{}, ErrCorrupt
	}
	state := StateV1{
		SchemaVersion:  int(row.StateVersion),
		Query:          row.Query,
		Filters:        filters,
		KnownColumns:   append([]string(nil), row.KnownColumns...),
		VisibleColumns: append([]string(nil), row.VisibleColumns...),
		Density:        Density(row.Density),
	}
	if row.SortColumn != nil || row.SortDirection != nil {
		if row.SortColumn == nil || row.SortDirection == nil {
			return SavedView{}, ErrCorrupt
		}
		state.Sort = &Sort{ColumnID: *row.SortColumn, Direction: SortDirection(*row.SortDirection)}
	}
	if err := ValidateState(state); err != nil {
		return SavedView{}, ErrCorrupt
	}
	hash, err := CanonicalStateHash(state)
	if err != nil || hash != row.StateHash {
		return SavedView{}, ErrCorrupt
	}
	return SavedView{
		ID: row.ID, TableKey: row.TableKey, Name: row.Name, State: state,
		StateHash: row.StateHash, CreatedAt: fromTimestamp(row.CreatedAt),
		UpdatedAt: fromTimestamp(row.UpdatedAt),
	}, nil
}

func (s *Store) List(ctx context.Context, owner Owner, tableKey string) ([]SavedView, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	if err := ValidateTableKey(tableKey); err != nil {
		return nil, err
	}
	rows, err := s.q.ListSavedViews(ctx, gen.ListSavedViewsParams{
		OwnerIssuer: owner.Issuer, OwnerSubject: owner.Subject,
		IdentityZone: owner.IdentityZone, Environment: owner.Environment, TableKey: tableKey,
	})
	if err != nil {
		return nil, ErrStore
	}
	items := make([]SavedView, 0, len(rows))
	for _, row := range rows {
		item, convertErr := rowToSavedView(row)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) Set(ctx context.Context, owner Owner, in SavedView) (SetResult, error) {
	if err := validateOwner(owner); err != nil {
		return SetResult{}, err
	}
	if err := ValidateTableKey(in.TableKey); err != nil {
		return SetResult{}, err
	}
	name, err := NormalizeName(in.Name)
	if err != nil {
		return SetResult{}, err
	}
	if err := ValidateState(in.State); err != nil {
		return SetResult{}, err
	}
	stateHash, err := CanonicalStateHash(in.State)
	if err != nil {
		return SetResult{}, err
	}
	filters, err := json.Marshal(in.State.Filters)
	if err != nil || len(filters) > MaxFiltersJSONBytes {
		return SetResult{}, ErrInvalidState
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SetResult{}, ErrStore
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	if err := q.LockSavedViewOwner(ctx, ownerLockKey(owner)); err != nil {
		return SetResult{}, ErrStore
	}

	before, lookupErr := q.GetSavedViewForUpdate(ctx, gen.GetSavedViewForUpdateParams{
		OwnerIssuer: owner.Issuer, OwnerSubject: owner.Subject,
		IdentityZone: owner.IdentityZone, Environment: owner.Environment,
		TableKey: in.TableKey, Name: name,
	})
	creating := errors.Is(lookupErr, pgx.ErrNoRows)
	if lookupErr != nil && !creating {
		return SetResult{}, ErrStore
	}
	if creating {
		tableCount, countErr := q.CountSavedViewsByTable(ctx, gen.CountSavedViewsByTableParams{
			OwnerIssuer: owner.Issuer, OwnerSubject: owner.Subject,
			IdentityZone: owner.IdentityZone, Environment: owner.Environment,
			TableKey: in.TableKey,
		})
		if countErr != nil {
			return SetResult{}, ErrStore
		}
		environmentCount, countErr := q.CountSavedViewsByEnvironment(ctx, gen.CountSavedViewsByEnvironmentParams{
			OwnerIssuer: owner.Issuer, OwnerSubject: owner.Subject,
			IdentityZone: owner.IdentityZone, Environment: owner.Environment,
		})
		if countErr != nil {
			return SetResult{}, ErrStore
		}
		if tableCount >= MaxViewsPerTable || environmentCount >= MaxViewsPerEnvironment {
			return SetResult{}, ErrQuotaExceeded
		}
	}

	var sortColumn, sortDirection *string
	if in.State.Sort != nil {
		column := in.State.Sort.ColumnID
		direction := string(in.State.Sort.Direction)
		sortColumn, sortDirection = &column, &direction
	}
	row, err := q.UpsertSavedView(ctx, gen.UpsertSavedViewParams{
		ID: in.ID, OwnerIssuer: owner.Issuer, OwnerSubject: owner.Subject,
		IdentityZone: owner.IdentityZone, Environment: owner.Environment,
		TableKey: in.TableKey, Name: name, StateVersion: int16(in.State.SchemaVersion),
		Query: in.State.Query, Filters: filters, SortColumn: sortColumn,
		SortDirection: sortDirection, KnownColumns: append([]string(nil), in.State.KnownColumns...),
		VisibleColumns: append([]string(nil), in.State.VisibleColumns...),
		Density:        string(in.State.Density), StateHash: stateHash,
	})
	if err != nil {
		return SetResult{}, ErrStore
	}
	after, err := rowToSavedView(row)
	if err != nil {
		return SetResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SetResult{}, ErrStore
	}
	result := SetResult{After: after}
	if !creating {
		result.BeforeHash = &before
	}
	return result, nil
}

func (s *Store) Remove(ctx context.Context, owner Owner, id uuid.UUID) (RemoveResult, error) {
	if err := validateOwner(owner); err != nil {
		return RemoveResult{}, err
	}
	if id == uuid.Nil {
		return RemoveResult{}, ErrNotFound
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RemoveResult{}, ErrStore
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	if err := q.LockSavedViewOwner(ctx, ownerLockKey(owner)); err != nil {
		return RemoveResult{}, ErrStore
	}
	row, err := q.DeleteSavedViewOwned(ctx, gen.DeleteSavedViewOwnedParams{
		ID: id, OwnerIssuer: owner.Issuer, OwnerSubject: owner.Subject,
		IdentityZone: owner.IdentityZone, Environment: owner.Environment,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RemoveResult{}, ErrNotFound
	}
	if err != nil {
		return RemoveResult{}, ErrStore
	}
	if err := tx.Commit(ctx); err != nil {
		return RemoveResult{}, ErrStore
	}
	return RemoveResult{ID: row.ID, BeforeHash: row.StateHash}, nil
}
