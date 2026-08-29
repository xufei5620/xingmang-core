package finance

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
)

type BindingService struct {
	ID          uuid.UUID
	ServiceType string
	InstanceID  string
	Environment string
	Status      string
}

type BindingUpstreamAccount struct {
	ID           uuid.UUID
	SystemType   string
	AccessMethod string
	Environment  string
	Status       string
}

type SetBindingInput struct {
	Environment       string
	Channel           ChannelRef
	UpstreamAccountID uuid.UUID
	ExpectedBindingID *uuid.UUID
	Provenance        string
	Reason            string
	CreatedBy         string
}

type ChannelBindingStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	now  func() time.Time
}

func NewChannelBindingStore(pool *pgxpool.Pool, now func() time.Time) *ChannelBindingStore {
	if now == nil {
		now = time.Now
	}
	return &ChannelBindingStore{pool: pool, q: gen.New(pool), now: now}
}

func bindingTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func bindingFromRow(row gen.FinancePlatformChannelBinding) PlatformChannelBinding {
	var validTo *time.Time
	if row.ValidTo.Valid {
		value := row.ValidTo.Time.UTC()
		validTo = &value
	}
	return PlatformChannelBinding{
		ID: row.ID, Environment: row.Environment,
		Channel:           ChannelRef{ServiceID: row.ServiceID, ExternalChannelID: row.ExternalChannelID},
		UpstreamAccountID: row.UpstreamAccountID,
		ValidFrom:         bindingTime(row.ValidFrom), ValidTo: validTo,
		Provenance: row.Provenance, Reason: row.Reason, CreatedBy: row.CreatedBy,
		CreatedAt: bindingTime(row.CreatedAt),
	}
}

func bindingTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func validateSetBindingInput(in *SetBindingInput) error {
	ref, err := NewChannelRef(in.Channel.ServiceID, in.Channel.ExternalChannelID)
	if err != nil {
		return err
	}
	in.Channel = ref
	in.Environment = strings.TrimSpace(in.Environment)
	in.Reason = strings.TrimSpace(in.Reason)
	in.CreatedBy = strings.TrimSpace(in.CreatedBy)
	in.Provenance = strings.TrimSpace(in.Provenance)
	if in.Environment == "" || in.UpstreamAccountID == uuid.Nil || in.Reason == "" || in.CreatedBy == "" {
		return ErrMissingField
	}
	if in.Provenance != "manual" && in.Provenance != "token_map_backfill" {
		return ErrInvalidFormat
	}
	return nil
}

func (s *ChannelBindingStore) GetService(ctx context.Context, id uuid.UUID) (BindingService, error) {
	row, err := s.q.GetBindingService(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return BindingService{}, ErrNotFound
	}
	if err != nil {
		return BindingService{}, err
	}
	return BindingService{ID: row.ID, ServiceType: row.ServiceType, InstanceID: row.InstanceID, Environment: row.Environment, Status: row.Status}, nil
}

func (s *ChannelBindingStore) GetUpstreamAccount(ctx context.Context, id uuid.UUID) (BindingUpstreamAccount, error) {
	row, err := s.q.GetBindingUpstreamAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return BindingUpstreamAccount{}, ErrNotFound
	}
	if err != nil {
		return BindingUpstreamAccount{}, err
	}
	return BindingUpstreamAccount{ID: row.ID, SystemType: row.SystemType, AccessMethod: row.AccessMethod, Environment: row.Environment, Status: row.Status}, nil
}

func validateBindingEndpoints(service BindingService, account BindingUpstreamAccount, environment string) error {
	if service.Environment != environment || account.Environment != environment {
		return ErrBindingConflict
	}
	if service.Status == "retired" || (service.ServiceType != "sub2api" && service.ServiceType != "newapi") {
		return ErrBindingPrecondition
	}
	if account.SystemType != service.ServiceType {
		return ErrBindingConflict
	}
	return nil
}

func (s *ChannelBindingStore) Set(ctx context.Context, in SetBindingInput) (SetBindingResult, error) {
	if err := validateSetBindingInput(&in); err != nil {
		return SetBindingResult{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SetBindingResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	serviceRow, err := q.GetBindingService(ctx, in.Channel.ServiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SetBindingResult{}, ErrNotFound
	}
	if err != nil {
		return SetBindingResult{}, err
	}
	accountRow, err := q.GetBindingUpstreamAccount(ctx, in.UpstreamAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SetBindingResult{}, ErrNotFound
	}
	if err != nil {
		return SetBindingResult{}, err
	}
	if err := validateBindingEndpoints(
		BindingService{ID: serviceRow.ID, ServiceType: serviceRow.ServiceType, InstanceID: serviceRow.InstanceID, Environment: serviceRow.Environment, Status: serviceRow.Status},
		BindingUpstreamAccount{ID: accountRow.ID, SystemType: accountRow.SystemType, AccessMethod: accountRow.AccessMethod, Environment: accountRow.Environment, Status: accountRow.Status},
		in.Environment,
	); err != nil {
		return SetBindingResult{}, err
	}
	if err := q.AcquirePlatformChannelBindingLock(ctx, gen.AcquirePlatformChannelBindingLockParams{
		ServiceID: in.Channel.ServiceID, ExternalChannelID: in.Channel.ExternalChannelID,
	}); err != nil {
		return SetBindingResult{}, err
	}

	currentRow, currentErr := q.LockActivePlatformChannelBinding(ctx, gen.LockActivePlatformChannelBindingParams{
		ServiceID: in.Channel.ServiceID, ExternalChannelID: in.Channel.ExternalChannelID,
	})
	hasCurrent := currentErr == nil
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return SetBindingResult{}, currentErr
	}
	if !hasCurrent && in.ExpectedBindingID != nil {
		return SetBindingResult{}, ErrBindingConflict
	}
	if hasCurrent {
		current := bindingFromRow(currentRow)
		if in.ExpectedBindingID == nil || *in.ExpectedBindingID != current.ID {
			return SetBindingResult{}, ErrBindingConflict
		}
		if current.UpstreamAccountID == in.UpstreamAccountID {
			if err := tx.Commit(ctx); err != nil {
				return SetBindingResult{}, err
			}
			return SetBindingResult{Binding: current, Previous: &current, Changed: false}, nil
		}
	}

	now := s.now().UTC()
	var previous *PlatformChannelBinding
	if hasCurrent {
		closed, closeErr := q.ClosePlatformChannelBinding(ctx, gen.ClosePlatformChannelBindingParams{
			ValidTo: bindingTimestamp(now), ID: currentRow.ID,
		})
		if closeErr != nil {
			return SetBindingResult{}, closeErr
		}
		value := bindingFromRow(closed)
		previous = &value
	}
	overlaps, err := q.ListOverlappingPlatformChannelBindings(ctx, gen.ListOverlappingPlatformChannelBindingsParams{
		ServiceID: in.Channel.ServiceID, ExternalChannelID: in.Channel.ExternalChannelID,
		ToTime:   bindingTimestamp(time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)),
		FromTime: bindingTimestamp(now),
	})
	if err != nil {
		return SetBindingResult{}, err
	}
	if len(overlaps) > 0 {
		return SetBindingResult{}, ErrBindingConflict
	}
	inserted, err := q.InsertPlatformChannelBinding(ctx, gen.InsertPlatformChannelBindingParams{
		ID: uuid.New(), Environment: in.Environment, ServiceID: in.Channel.ServiceID,
		ExternalChannelID: in.Channel.ExternalChannelID, UpstreamAccountID: in.UpstreamAccountID,
		ValidFrom: bindingTimestamp(now), Provenance: in.Provenance,
		Reason: in.Reason, CreatedBy: in.CreatedBy,
	})
	if err != nil {
		return SetBindingResult{}, ErrBindingConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return SetBindingResult{}, err
	}
	return SetBindingResult{Binding: bindingFromRow(inserted), Previous: previous, Changed: true}, nil
}

func (s *ChannelBindingStore) Remove(
	ctx context.Context, environment string, ref ChannelRef, expected uuid.UUID, reason, actor string,
) (PlatformChannelBinding, error) {
	in := SetBindingInput{Environment: environment, Channel: ref, UpstreamAccountID: uuid.New(), ExpectedBindingID: &expected, Provenance: "manual", Reason: reason, CreatedBy: actor}
	// Validate common canonical/reason/actor fields; generated UpstreamAccountID is discarded.
	if err := validateSetBindingInput(&in); err != nil {
		return PlatformChannelBinding{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PlatformChannelBinding{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	if err := q.AcquirePlatformChannelBindingLock(ctx, gen.AcquirePlatformChannelBindingLockParams{ServiceID: in.Channel.ServiceID, ExternalChannelID: in.Channel.ExternalChannelID}); err != nil {
		return PlatformChannelBinding{}, err
	}
	current, err := q.LockActivePlatformChannelBinding(ctx, gen.LockActivePlatformChannelBindingParams{ServiceID: in.Channel.ServiceID, ExternalChannelID: in.Channel.ExternalChannelID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && current.ID != expected) {
		return PlatformChannelBinding{}, ErrBindingConflict
	}
	if err != nil {
		return PlatformChannelBinding{}, err
	}
	if current.Environment != in.Environment {
		return PlatformChannelBinding{}, ErrBindingConflict
	}
	closed, err := q.ClosePlatformChannelBinding(ctx, gen.ClosePlatformChannelBindingParams{ValidTo: bindingTimestamp(s.now()), ID: current.ID})
	if err != nil {
		return PlatformChannelBinding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlatformChannelBinding{}, err
	}
	return bindingFromRow(closed), nil
}

func (s *ChannelBindingStore) ListActive(ctx context.Context, serviceID uuid.UUID) ([]PlatformChannelBinding, error) {
	rows, err := s.q.ListActivePlatformChannelBindingsByService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make([]PlatformChannelBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, bindingFromRow(row))
	}
	return out, nil
}

func (s *ChannelBindingStore) History(ctx context.Context, ref ChannelRef) ([]PlatformChannelBinding, error) {
	normalized, err := NewChannelRef(ref.ServiceID, ref.ExternalChannelID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPlatformChannelBindingHistory(ctx, gen.ListPlatformChannelBindingHistoryParams{ServiceID: normalized.ServiceID, ExternalChannelID: normalized.ExternalChannelID})
	if err != nil {
		return nil, err
	}
	out := make([]PlatformChannelBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, bindingFromRow(row))
	}
	return out, nil
}

func (s *ChannelBindingStore) TokenEvidence(ctx context.Context, environment string) ([]TokenMapEvidence, error) {
	rows, err := s.q.ListTokenMapEvidenceByEnvironment(ctx, environment)
	if err != nil {
		return nil, err
	}
	out := make([]TokenMapEvidence, 0, len(rows))
	for _, row := range rows {
		out = append(out, TokenMapEvidence{
			ExternalChannelID: row.OwnAccountID, UpstreamAccountID: row.UpstreamAccountID,
			SystemType:                row.SystemType,
			ActiveServiceCount:        int(row.ActiveServiceCount),
			SystemTypeMatches:         true,
			PlatformAssignmentMissing: row.PlatformID == nil,
		})
	}
	return out, nil
}
