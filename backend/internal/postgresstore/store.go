package postgresstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/domain"
)

type Store struct {
	pool *pgxpool.Pool
	// evidenceBatchLimit bounds how many pending balance-evidence items one
	// projection job evaluates (XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3).
	// Zero means unbounded, which is the behaviour every release before this
	// knob had. See completeEligibilityProjectionJob for the semantics and
	// why a boundary between two items cannot change a decision.
	evidenceBatchLimit int
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	// Keep one API replica within the dedicated role's 20-connection ceiling
	// and leave capacity for a two-replica rolling replacement.
	config.MaxConns = 10
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return New(pool), nil
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// SetEvidenceBatchLimit sets the per-job bound on pending balance-evidence
// items (0 = unbounded). The api sets it from ELIGIBILITY_EVIDENCE_BATCH_LIMIT;
// the rehearsal tool from --evidence-batch-limit, so the same image can run
// the differential rehearsal in both modes.
func (s *Store) SetEvidenceBatchLimit(limit int) {
	if limit < 0 {
		limit = 0
	}
	s.evidenceBatchLimit = limit
}

// EvidenceBatchLimit reports the configured bound (0 = unbounded).
func (s *Store) EvidenceBatchLimit() int { return s.evidenceBatchLimit }
func (s *Store) Pool() *pgxpool.Pool     { return s.pool }
func (s *Store) Close()                  { s.pool.Close() }

// ProfileRecord deliberately names encrypted fields explicitly. Encryption is
// performed before the persistence boundary; this package never stores profile
// plaintext as if it were ciphertext.
type ProfileRecord struct {
	ID, PrincipalID       string
	Type                  domain.ProfileType
	TitleCiphertext       []byte
	TaxIDCiphertext       []byte
	TaxIDHMAC             string
	EmailCiphertext       []byte
	AddressCiphertext     []byte
	PhoneCiphertext       []byte
	BankNameCiphertext    []byte
	BankAccountCiphertext []byte
	EmailVerified         bool
	IsDefault             bool
	Revision              int64
	CreatedAt, UpdatedAt  time.Time
}

func (s *Store) SaveProfile(ctx context.Context, p ProfileRecord) (ProfileRecord, error) {
	if p.ID == "" || p.PrincipalID == "" || len(p.TitleCiphertext) == 0 || len(p.EmailCiphertext) == 0 {
		return ProfileRecord{}, errors.New("profile id, owner, encrypted title and encrypted email are required")
	}
	if p.Type != domain.ProfilePersonal && p.Type != domain.ProfileEnterprise {
		return ProfileRecord{}, errors.New("unsupported profile type")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProfileRecord{}, fmt.Errorf("begin save profile: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if p.IsDefault {
		// Serialize default-profile changes for one owner so concurrent saves do
		// not race against the partial unique index.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, p.PrincipalID); err != nil {
			return ProfileRecord{}, fmt.Errorf("lock default profile: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE invoice_profiles SET is_default=FALSE, updated_at=now() WHERE invoice_user_id=$1 AND id<>$2 AND is_default`, p.PrincipalID, p.ID); err != nil {
			return ProfileRecord{}, fmt.Errorf("clear previous default profile: %w", err)
		}
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO invoice_profiles (
			id, invoice_user_id, profile_type, title_ciphertext, tax_id_ciphertext, tax_id_hmac,
			email_ciphertext, address_ciphertext, phone_ciphertext, bank_name_ciphertext,
			bank_account_ciphertext, email_verified, is_default, revision)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,1)
		ON CONFLICT (id) DO UPDATE SET
			profile_type=EXCLUDED.profile_type, title_ciphertext=EXCLUDED.title_ciphertext,
			tax_id_ciphertext=EXCLUDED.tax_id_ciphertext, tax_id_hmac=EXCLUDED.tax_id_hmac,
			email_ciphertext=EXCLUDED.email_ciphertext, address_ciphertext=EXCLUDED.address_ciphertext,
			phone_ciphertext=EXCLUDED.phone_ciphertext, bank_name_ciphertext=EXCLUDED.bank_name_ciphertext,
			bank_account_ciphertext=EXCLUDED.bank_account_ciphertext, email_verified=EXCLUDED.email_verified,
			is_default=EXCLUDED.is_default,
			revision=invoice_profiles.revision+1, updated_at=now()
		WHERE invoice_profiles.invoice_user_id=EXCLUDED.invoice_user_id
		RETURNING revision, created_at, updated_at`,
		p.ID, p.PrincipalID, p.Type, p.TitleCiphertext, nullableBytes(p.TaxIDCiphertext), p.TaxIDHMAC,
		p.EmailCiphertext, nullableBytes(p.AddressCiphertext), nullableBytes(p.PhoneCiphertext),
		nullableBytes(p.BankNameCiphertext), nullableBytes(p.BankAccountCiphertext), p.EmailVerified, p.IsDefault,
	).Scan(&p.Revision, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileRecord{}, domain.ErrForbidden
	}
	if err != nil {
		return ProfileRecord{}, fmt.Errorf("save profile: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return ProfileRecord{}, fmt.Errorf("commit profile: %w", err)
	}
	return p, nil
}

func (s *Store) GetProfile(ctx context.Context, principalID, profileID string) (ProfileRecord, error) {
	var p ProfileRecord
	err := s.pool.QueryRow(ctx, `
		SELECT id, invoice_user_id, profile_type, title_ciphertext, COALESCE(tax_id_ciphertext,''::bytea),
			COALESCE(tax_id_hmac,''), email_ciphertext, COALESCE(address_ciphertext,''::bytea),
			COALESCE(phone_ciphertext,''::bytea), COALESCE(bank_name_ciphertext,''::bytea),
			COALESCE(bank_account_ciphertext,''::bytea), email_verified, is_default, revision, created_at, updated_at
		FROM invoice_profiles WHERE id=$1 AND invoice_user_id=$2`, profileID, principalID).Scan(
		&p.ID, &p.PrincipalID, &p.Type, &p.TitleCiphertext, &p.TaxIDCiphertext, &p.TaxIDHMAC,
		&p.EmailCiphertext, &p.AddressCiphertext, &p.PhoneCiphertext, &p.BankNameCiphertext,
		&p.BankAccountCiphertext, &p.EmailVerified, &p.IsDefault, &p.Revision, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileRecord{}, domain.ErrNotFound
	}
	return p, err
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// UpsertFundingLot is retained for deterministic test/bootstrap fixtures.
// Production connector ingestion must call ObserveFundingLot so source events,
// refunds, request invalidation and audit records commit atomically.
func (s *Store) UpsertFundingLot(ctx context.Context, lot domain.FundingLot) error {
	cutoverBasis := valueOrNow(lot.CompletedAt).UTC().Truncate(time.Microsecond)
	lot.CompletedAt = cutoverBasis
	// PostgreSQL stores timestamptz at microsecond precision. Keep a full
	// millisecond between the fixture cutover and payment completion so a
	// round-trip can never collapse the strict completed_at > cutover boundary.
	cutover := cutoverBasis.Add(-time.Millisecond)
	lot.EligibilityKind = domain.EligibilityWalletCash
	lot.EligibilityCutoverAt = cutover
	lot.VerifiedCashMinor = lot.CurrentCapMinor
	lot.ConsumedCashMinor = lot.CurrentCapMinor
	lot.EligibilityRevision = 1
	if err := lot.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var accountID string
	var sourceType domain.SourceType
	if err = tx.QueryRow(ctx, `
		SELECT ea.id,si.source_type FROM external_accounts ea
		JOIN source_instances si ON si.id=ea.source_instance_id
		WHERE ea.invoice_user_id=$1 AND ea.source_instance_id=$2 AND ea.binding_status='verified'
		FOR SHARE OF ea,si`, lot.PrincipalID, lot.SourceInstanceID).Scan(&accountID, &sourceType); err != nil {
		return err
	}
	unitCode := expectedUnitForSource(sourceType)
	manifestBytes := sha256.Sum256([]byte("fixture-manifest\n" + lot.SourceInstanceID))
	configBytes := sha256.Sum256([]byte("fixture-config\n" + lot.SourceInstanceID))
	manifestHash := hex.EncodeToString(manifestBytes[:])
	configHash := hex.EncodeToString(configBytes[:])
	if _, err = tx.Exec(ctx, `UPDATE source_instances SET runtime_version='fixture-runtime' WHERE id=$1 AND runtime_version=''`, lot.SourceInstanceID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO source_cutover_manifests(
			source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
			projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
			credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
			baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'fixture-runtime','fixture-v3',$4,$5,'fixture','fixture','fixture','fixture',$2,$2,1,'fixture')
		ON CONFLICT(source_instance_id) DO NOTHING`, lot.SourceInstanceID, manifestHash, cutover, configHash, unitCode)
	if err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT cutover_at FROM source_cutover_manifests WHERE source_instance_id=$1`,
		lot.SourceInstanceID).Scan(&cutover); err != nil {
		return err
	}
	lot.EligibilityCutoverAt = cutover
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		_, err = tx.Exec(ctx, `
			INSERT INTO source_economic_stream_watermarks(
				source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
			VALUES($1,$2,$3,0,'fixture',$4)
			ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET watermark_at=GREATEST(source_economic_stream_watermarks.watermark_at,EXCLUDED.watermark_at)`,
			lot.SourceInstanceID, stream, valueOrNow(lot.ObservedAt), configHash)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO source_account_eligibility_state(
			external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
			cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,$4,0,$5,$6,900)
		ON CONFLICT(external_account_id) DO NOTHING`, accountID, lot.SourceInstanceID, cutover,
		unitCode, manifestHash, valueOrNow(lot.ObservedAt))
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		INSERT INTO funding_lots (
			id, invoice_user_id, external_account_id, source_instance_id, external_order_id, trade_no,
			currency, original_minor, current_cap_minor, verified_cash_minor, consumed_cash_minor,
			reserved_minor, issued_minor, eligibility_kind, eligibility_cutover_at,eligibility_revision,
			verification_state,source_status, source_revision_hash, completed_at, observed_at, updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$9,$10,$11,'WALLET_CASH',$12,1,$13,$14,$15,NULLIF($16,'')::timestamptz,$17,$18)
		ON CONFLICT (source_instance_id, external_order_id) DO UPDATE SET
			trade_no=EXCLUDED.trade_no, original_minor=EXCLUDED.original_minor,
			current_cap_minor=EXCLUDED.current_cap_minor,verified_cash_minor=EXCLUDED.verified_cash_minor,
			consumed_cash_minor=EXCLUDED.consumed_cash_minor,eligibility_kind=EXCLUDED.eligibility_kind,
			eligibility_cutover_at=EXCLUDED.eligibility_cutover_at,refund_frozen=FALSE,
			eligibility_revision=funding_lots.eligibility_revision+1,verification_state=EXCLUDED.verification_state,
			source_status=EXCLUDED.source_status, source_revision_hash=EXCLUDED.source_revision_hash,
			completed_at=EXCLUDED.completed_at, observed_at=EXCLUDED.observed_at, updated_at=EXCLUDED.updated_at
		WHERE funding_lots.invoice_user_id=EXCLUDED.invoice_user_id
			AND funding_lots.reserved_minor + funding_lots.issued_minor <= EXCLUDED.consumed_cash_minor`,
		lot.ID, lot.PrincipalID, accountID, lot.SourceInstanceID, lot.ExternalOrderID, lot.TradeNo,
		lot.Currency, lot.OriginalMinor, lot.CurrentCapMinor, lot.ReservedMinor, lot.IssuedMinor,
		cutover, lot.Verification, lot.SourceStatus, lot.SourceRevision, nullableTime(lot.CompletedAt),
		valueOrNow(lot.ObservedAt), valueOrNow(lot.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert funding lot: %w", err)
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	if lot.CurrentCapMinor == 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO funding_lot_consumption_state(funding_lot_id,cash_service_units)
			VALUES($1,1) ON CONFLICT(funding_lot_id) DO NOTHING`, lot.ID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO funding_lot_consumption_state(
				funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
				rounded_consumed_cash_minor,rounding_remainder_numerator)
			VALUES($1,1,1,$2::numeric,$2::bigint,0)
			ON CONFLICT(funding_lot_id) DO UPDATE SET cash_service_units=EXCLUDED.cash_service_units,
				consumed_service_units=EXCLUDED.consumed_service_units,
				cumulative_cash_numerator=EXCLUDED.cumulative_cash_numerator,
				rounded_consumed_cash_minor=EXCLUDED.rounded_consumed_cash_minor,
				rounding_remainder_numerator=0,updated_at=now()`, lot.ID, lot.CurrentCapMinor)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}
func valueOrNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value
}

type AllocationInput struct {
	FundingLotID string
	AmountMinor  int64
}

type SubmitInput struct {
	PrincipalID               string
	ProfileID                 string
	SourceInstanceID          string
	IdempotencyKey            string
	ProfileSnapshotCiphertext []byte
	IssuerCode                string
	Allocations               []AllocationInput
	MinimumRequestMinor       int64
	Freshness                 SourceFreshnessPolicy
	Actor                     AuditActor
	// Platform (XM-INV-PLATFORM-SCOPE) is always sourced server-side from the
	// session, never from client input. When set, every allocated funding lot
	// must belong to this platform or the whole submission is rejected --
	// never partially accepted (the existing source-mixing check already
	// makes every allocation share one source_instance_id/type; this adds
	// that the shared type must also match the session's platform).
	Platform domain.SourceType
}

func (s *Store) Submit(ctx context.Context, in SubmitInput) (domain.InvoiceRequest, error) {
	if strings.TrimSpace(in.PrincipalID) == "" || strings.TrimSpace(in.ProfileID) == "" ||
		strings.TrimSpace(in.SourceInstanceID) == "" || strings.TrimSpace(in.IdempotencyKey) == "" ||
		len(in.ProfileSnapshotCiphertext) == 0 || len(in.Allocations) == 0 {
		return domain.InvoiceRequest{}, errors.New("invalid submission")
	}
	if len(in.IdempotencyKey) > 128 || strings.ContainsAny(in.IdempotencyKey, "\r\n\x00") {
		return domain.InvoiceRequest{}, errors.New("idempotency key is invalid")
	}
	allocs := append([]AllocationInput(nil), in.Allocations...)
	sort.Slice(allocs, func(i, j int) bool { return allocs[i].FundingLotID < allocs[j].FundingLotID })
	lotIDs := make([]string, len(allocs))
	var total int64
	for i, a := range allocs {
		if a.FundingLotID == "" || a.AmountMinor <= 0 || (i > 0 && allocs[i-1].FundingLotID == a.FundingLotID) {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		if a.AmountMinor > int64(^uint64(0)>>1)-total {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		total += a.AmountMinor
		lotIDs[i] = a.FundingLotID
	}
	minimum := in.MinimumRequestMinor
	if minimum == 0 {
		minimum = domain.MinimumRequestMinor
	}
	if minimum < domain.MinimumRequestFloorMinor {
		return domain.InvoiceRequest{}, domain.ErrMinimumAmount
	}
	if total < minimum {
		return domain.InvoiceRequest{}, fmt.Errorf("minimum invoice amount is %d minor units: %w", minimum, domain.ErrMinimumAmount)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("begin submit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var eligibilityPolicyStart time.Time
	var eligibilityPolicyVersion int64
	if err = tx.QueryRow(ctx, `
		SELECT eligibility_start_at,policy_version
		FROM invoice_eligibility_policy WHERE singleton_id=1`).Scan(
		&eligibilityPolicyStart, &eligibilityPolicyVersion); err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("load invoice eligibility policy: %w", err)
	}
	if err = assertSourceFreshTx(ctx, tx, in.SourceInstanceID, in.Freshness); err != nil {
		return domain.InvoiceRequest{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, in.PrincipalID+"\n"+in.IdempotencyKey); err != nil {
		return domain.InvoiceRequest{}, err
	}

	if existing, existingProfileID, found, getErr := getRequestByIdempotency(ctx, tx, in.PrincipalID, in.IdempotencyKey); getErr != nil {
		return domain.InvoiceRequest{}, getErr
	} else if found {
		if existingProfileID != in.ProfileID || !sameSubmission(existing, in, total, allocs) {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		return existing, nil
	}
	var profileOwner string
	if err = tx.QueryRow(ctx, `SELECT invoice_user_id FROM invoice_profiles WHERE id=$1 FOR SHARE`, in.ProfileID).Scan(&profileOwner); errors.Is(err, pgx.ErrNoRows) {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	} else if err != nil {
		return domain.InvoiceRequest{}, err
	}
	if profileOwner != in.PrincipalID {
		return domain.InvoiceRequest{}, domain.ErrForbidden
	}

	rows, err := tx.Query(ctx, `
		SELECT fl.id, fl.invoice_user_id, fl.source_instance_id, si.source_type, fl.external_order_id,
			fl.currency, fl.current_cap_minor, fl.consumed_cash_minor, fl.reserved_minor, fl.issued_minor,
			fl.verification_state, fl.source_revision_hash,fl.eligibility_kind,fl.refund_frozen,
			COALESCE(fl.completed_at,'epoch'::timestamptz),COALESCE(fl.eligibility_cutover_at,'epoch'::timestamptz),
			COALESCE(eas.eligibility_status,'missing'),
			EXISTS(SELECT 1 FROM eligibility_projection_jobs epj WHERE epj.external_account_id=fl.external_account_id)
		FROM funding_lots fl JOIN source_instances si ON si.id=fl.source_instance_id
		LEFT JOIN source_account_eligibility_state eas ON eas.external_account_id=fl.external_account_id
		WHERE fl.id=ANY($1::uuid[]) ORDER BY fl.id FOR UPDATE OF fl`, lotIDs)
	if err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("lock funding lots: %w", err)
	}
	type lockedLot struct {
		id, user, source, sourceType, order, currency, verification, revision string
		eligibilityKind, eligibilityStatus                                    string
		cap, consumed, reserved, issued                                       int64
		refundFrozen                                                          bool
		projectionPending                                                     bool
		completedAt, eligibilityCutoverAt                                     time.Time
	}
	locked := map[string]lockedLot{}
	for rows.Next() {
		var l lockedLot
		if err = rows.Scan(&l.id, &l.user, &l.source, &l.sourceType, &l.order, &l.currency,
			&l.cap, &l.consumed, &l.reserved, &l.issued, &l.verification, &l.revision,
			&l.eligibilityKind, &l.refundFrozen, &l.completedAt, &l.eligibilityCutoverAt,
			&l.eligibilityStatus, &l.projectionPending); err != nil {
			rows.Close()
			return domain.InvoiceRequest{}, err
		}
		locked[l.id] = l
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return domain.InvoiceRequest{}, err
	}
	rows.Close()
	if len(locked) != len(allocs) {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	}
	domainAllocs := make([]domain.Allocation, 0, len(allocs))
	var sourceType domain.SourceType
	for _, a := range allocs {
		l := locked[a.FundingLotID]
		if l.user != in.PrincipalID {
			return domain.InvoiceRequest{}, domain.ErrForbidden
		}
		if in.Platform != "" && domain.SourceType(l.sourceType) != in.Platform {
			// Same shape as the ownership check above: from a platform-scoped
			// session's point of view, a lot on the other platform is not
			// theirs to allocate, exactly like a lot owned by someone else.
			return domain.InvoiceRequest{}, domain.ErrForbidden
		}
		if l.source != in.SourceInstanceID {
			return domain.InvoiceRequest{}, domain.ErrSourceMixing
		}
		if l.currency != domain.CurrencyCNY {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		if l.verification != string(domain.VerificationVerified) {
			return domain.InvoiceRequest{}, domain.ErrUnverifiedPayment
		}
		if l.eligibilityKind != string(domain.EligibilityWalletCash) ||
			l.refundFrozen || l.eligibilityStatus != "active" || l.projectionPending ||
			l.completedAt.Before(eligibilityPolicyStart) || l.eligibilityCutoverAt.Before(eligibilityPolicyStart) {
			return domain.InvoiceRequest{}, domain.ErrUnverifiedPayment
		}
		if l.consumed-l.reserved-l.issued < a.AmountMinor {
			return domain.InvoiceRequest{}, domain.ErrInsufficientAmount
		}
		if sourceType == "" {
			sourceType = domain.SourceType(l.sourceType)
		} else if sourceType != domain.SourceType(l.sourceType) {
			return domain.InvoiceRequest{}, domain.ErrSourceMixing
		}
		domainAllocs = append(domainAllocs, domain.Allocation{FundingLotID: l.id, ExternalOrder: l.order, AmountMinor: a.AmountMinor})
	}

	now := time.Now().UTC()
	requestID := randomUUID()
	requestNo := "INV-" + strings.ToUpper(strings.ReplaceAll(requestID, "-", "")[20:])
	issuerCode := strings.TrimSpace(in.IssuerCode)
	if issuerCode == "" {
		issuerCode = "default"
	}
	_, err = tx.Exec(ctx, `INSERT INTO invoice_requests (id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,currency,issuer_code,service_item,amount_minor,status,idempotency_key,version,eligibility_policy_start_at,eligibility_policy_version,submitted_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,'CNY',$7,$8,$9,$10,$11,1,$12,$13,$14,$14)`, requestID, requestNo, in.PrincipalID, in.SourceInstanceID, in.ProfileID, in.ProfileSnapshotCiphertext, issuerCode, domain.FixedServiceItem, total, domain.StatusPendingReview, in.IdempotencyKey, eligibilityPolicyStart, eligibilityPolicyVersion, now)
	if err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("insert invoice request: %w", err)
	}
	for _, a := range allocs {
		l := locked[a.FundingLotID]
		command, updateErr := tx.Exec(ctx, `
			UPDATE funding_lots SET reserved_minor=reserved_minor+$1,updated_at=$2
			WHERE id=$3 AND eligibility_kind='WALLET_CASH'
				AND verification_state='verified' AND refund_frozen=FALSE
				AND completed_at >= (SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1)
				AND eligibility_cutover_at >= (SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1)
				AND consumed_cash_minor-reserved_minor-issued_minor >= $1
				AND EXISTS (
					SELECT 1 FROM source_account_eligibility_state eas
					WHERE eas.external_account_id=funding_lots.external_account_id
						AND eas.eligibility_status='active')
				AND NOT EXISTS (SELECT 1 FROM eligibility_projection_jobs epj
					WHERE epj.external_account_id=funding_lots.external_account_id)`, a.AmountMinor, now, a.FundingLotID)
		if updateErr != nil {
			return domain.InvoiceRequest{}, updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.InvoiceRequest{}, domain.ErrInsufficientAmount
		}
		_, err = tx.Exec(ctx, `INSERT INTO invoice_allocations(id,invoice_request_id,funding_lot_id,amount_minor,source_revision_hash,allocation_state,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'reserved',$6,$6)`, randomUUID(), requestID, a.FundingLotID, a.AmountMinor, l.revision, now)
		if err != nil {
			return domain.InvoiceRequest{}, err
		}
	}
	requestState := domain.InvoiceRequest{ID: requestID, RequestNo: requestNo, PrincipalID: in.PrincipalID, SourceInstanceID: in.SourceInstanceID, SourceType: sourceType, Currency: domain.CurrencyCNY, IssuerCode: issuerCode, ServiceItem: domain.FixedServiceItem, AmountMinor: total, Status: domain.StatusPendingReview, Allocations: domainAllocs, Version: 1, SubmittedAt: now, UpdatedAt: now}
	if err = writeAudit(ctx, tx, in.Actor, "invoice_request.submitted", "invoice_request", requestID, nil, requestState); err != nil {
		return domain.InvoiceRequest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("commit submit: %w", err)
	}
	return requestState, nil
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(b[:])
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32]
}

func getRequestByIdempotency(ctx context.Context, tx pgx.Tx, principalID, key string) (domain.InvoiceRequest, string, bool, error) {
	var r domain.InvoiceRequest
	var sourceType string
	var profileID string
	err := tx.QueryRow(ctx, `SELECT ir.id,ir.request_no,ir.invoice_user_id,ir.source_instance_id,ir.profile_id,si.source_type,ir.currency,ir.issuer_code,ir.service_item,ir.amount_minor,ir.status,ir.version,ir.submitted_at,ir.updated_at FROM invoice_requests ir JOIN source_instances si ON si.id=ir.source_instance_id WHERE ir.invoice_user_id=$1 AND ir.idempotency_key=$2`, principalID, key).Scan(&r.ID, &r.RequestNo, &r.PrincipalID, &r.SourceInstanceID, &profileID, &sourceType, &r.Currency, &r.IssuerCode, &r.ServiceItem, &r.AmountMinor, &r.Status, &r.Version, &r.SubmittedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, "", false, nil
	}
	if err != nil {
		return r, "", false, err
	}
	r.SourceType = domain.SourceType(sourceType)
	rows, err := tx.Query(ctx, `SELECT ia.funding_lot_id,fl.external_order_id,ia.amount_minor FROM invoice_allocations ia JOIN funding_lots fl ON fl.id=ia.funding_lot_id WHERE ia.invoice_request_id=$1 ORDER BY ia.funding_lot_id`, r.ID)
	if err != nil {
		return r, "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var a domain.Allocation
		if err = rows.Scan(&a.FundingLotID, &a.ExternalOrder, &a.AmountMinor); err != nil {
			return r, "", false, err
		}
		r.Allocations = append(r.Allocations, a)
	}
	return r, profileID, true, rows.Err()
}

func sameSubmission(r domain.InvoiceRequest, in SubmitInput, total int64, allocs []AllocationInput) bool {
	if r.PrincipalID != in.PrincipalID || r.SourceInstanceID != in.SourceInstanceID || r.AmountMinor != total || len(r.Allocations) != len(allocs) {
		return false
	}
	for i, a := range allocs {
		if r.Allocations[i].FundingLotID != a.FundingLotID || r.Allocations[i].AmountMinor != a.AmountMinor {
			return false
		}
	}
	return true
}

func (s *Store) GetRequest(ctx context.Context, principalID, requestID string) (domain.InvoiceRequest, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	defer tx.Rollback(context.Background())
	var r domain.InvoiceRequest
	var sourceType string
	err = tx.QueryRow(ctx, `SELECT ir.id,ir.request_no,ir.invoice_user_id,ir.source_instance_id,si.source_type,ir.currency,ir.issuer_code,ir.service_item,ir.amount_minor,ir.status,ir.version,ir.submitted_at,ir.updated_at FROM invoice_requests ir JOIN source_instances si ON si.id=ir.source_instance_id WHERE ir.id=$1 AND ir.invoice_user_id=$2`, requestID, principalID).Scan(&r.ID, &r.RequestNo, &r.PrincipalID, &r.SourceInstanceID, &sourceType, &r.Currency, &r.IssuerCode, &r.ServiceItem, &r.AmountMinor, &r.Status, &r.Version, &r.SubmittedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, domain.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.SourceType = domain.SourceType(sourceType)
	rows, err := tx.Query(ctx, `SELECT ia.funding_lot_id,fl.external_order_id,ia.amount_minor FROM invoice_allocations ia JOIN funding_lots fl ON fl.id=ia.funding_lot_id WHERE ia.invoice_request_id=$1 ORDER BY ia.funding_lot_id`, r.ID)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var a domain.Allocation
		if err = rows.Scan(&a.FundingLotID, &a.ExternalOrder, &a.AmountMinor); err != nil {
			rows.Close()
			return r, err
		}
		r.Allocations = append(r.Allocations, a)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return r, err
	}
	if err = tx.Commit(ctx); err != nil {
		return r, err
	}
	return r, nil
}
