package adminsettings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) Get(ctx context.Context) (Settings, error) {
	return scanSettings(r.pool.QueryRow(ctx, `SELECT s.issuer_name,s.service_item,s.minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),s.smtp_host,s.smtp_port,s.smtp_from,s.smtp_from_name,s.smtp_starttls,s.smtp_test_recipient,EXISTS(SELECT 1 FROM admin_setting_secrets x WHERE x.setting_id=s.singleton_id),s.admin_cidrs,s.revision,s.updated_by,s.created_at,s.updated_at FROM admin_settings s WHERE singleton_id=1`))
}

func (r *PostgresRepository) Update(ctx context.Context, in UpdateInput, expected int64, actor Actor) (Settings, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback(context.Background())
	if expected == 0 {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('admin_settings.initialize',0))`); err != nil {
			return Settings{}, err
		}
	}
	var before Settings
	before, err = scanSettings(tx.QueryRow(ctx, `SELECT s.issuer_name,s.service_item,s.minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),s.smtp_host,s.smtp_port,s.smtp_from,s.smtp_from_name,s.smtp_starttls,s.smtp_test_recipient,EXISTS(SELECT 1 FROM admin_setting_secrets x WHERE x.setting_id=s.singleton_id),s.admin_cidrs,s.revision,s.updated_by,s.created_at,s.updated_at FROM admin_settings s WHERE singleton_id=1 FOR UPDATE`))
	if errors.Is(err, ErrNotConfigured) {
		if expected != 0 {
			return Settings{}, ErrRevisionConflict
		}
		before, err = scanSettings(tx.QueryRow(ctx, `INSERT INTO admin_settings(singleton_id,issuer_name,service_item,minimum_request_minor,smtp_host,smtp_port,smtp_from,smtp_from_name,smtp_starttls,admin_cidrs,revision,updated_by,smtp_test_recipient) VALUES(1,$1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10,$11) RETURNING issuer_name,service_item,minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),smtp_host,smtp_port,smtp_from,smtp_from_name,smtp_starttls,smtp_test_recipient,FALSE,admin_cidrs,revision,updated_by,created_at,updated_at`, in.IssuerName, FixedServiceItem, in.MinimumRequestMinor, in.SMTPHost, in.SMTPPort, in.SMTPFrom, in.SMTPFromName, in.SMTPStartTLS, in.AdminCIDRs, actor.ID, in.SMTPTestRecipient))
		if isUniqueViolation(err) {
			return Settings{}, ErrRevisionConflict
		}
		if err != nil {
			return Settings{}, err
		}
		if err = audit(ctx, tx, "admin_settings.initialize", Settings{}, before, actor); err != nil {
			return Settings{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return Settings{}, err
		}
		return before, nil
	}
	if err != nil {
		return Settings{}, err
	}
	if before.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	after, err := scanSettings(tx.QueryRow(ctx, `UPDATE admin_settings SET issuer_name=$1,service_item=$2,minimum_request_minor=$3,smtp_host=$4,smtp_port=$5,smtp_from=$6,smtp_from_name=$7,smtp_starttls=$8,admin_cidrs=$9,smtp_test_recipient=$12,revision=revision+1,updated_by=$10,updated_at=now() WHERE singleton_id=1 AND revision=$11 RETURNING issuer_name,service_item,minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),smtp_host,smtp_port,smtp_from,smtp_from_name,smtp_starttls,smtp_test_recipient,EXISTS(SELECT 1 FROM admin_setting_secrets WHERE setting_id=1),admin_cidrs,revision,updated_by,created_at,updated_at`, in.IssuerName, FixedServiceItem, in.MinimumRequestMinor, in.SMTPHost, in.SMTPPort, in.SMTPFrom, in.SMTPFromName, in.SMTPStartTLS, in.AdminCIDRs, actor.ID, expected, in.SMTPTestRecipient))
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrRevisionConflict
	}
	if err != nil {
		return Settings{}, err
	}
	if err = audit(ctx, tx, "admin_settings.update", before, after, actor); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Settings{}, err
	}
	return after, nil
}

func (r *PostgresRepository) UpdateSMTP(ctx context.Context, in UpdateInput, change SMTPSecretChange, envelope SecretEnvelope, expected int64, actor Actor) (Settings, error) {
	if change == SMTPSecretSet && (len(envelope.Ciphertext) == 0 || envelope.KeyVersion == "") {
		return Settings{}, ErrInvalidSettings
	}
	if change != SMTPSecretUnchanged && change != SMTPSecretSet && change != SMTPSecretClear {
		return Settings{}, ErrInvalidSettings
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback(context.Background())
	before, err := scanSettings(tx.QueryRow(ctx, `SELECT s.issuer_name,s.service_item,s.minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),s.smtp_host,s.smtp_port,s.smtp_from,s.smtp_from_name,s.smtp_starttls,s.smtp_test_recipient,EXISTS(SELECT 1 FROM admin_setting_secrets x WHERE x.setting_id=s.singleton_id),s.admin_cidrs,s.revision,s.updated_by,s.created_at,s.updated_at FROM admin_settings s WHERE singleton_id=1 FOR UPDATE`))
	if err != nil {
		return Settings{}, err
	}
	if before.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	switch change {
	case SMTPSecretUnchanged:
	case SMTPSecretSet:
		if _, err = tx.Exec(ctx, `INSERT INTO admin_setting_secrets(setting_id,smtp_secret_ciphertext,key_version,updated_by) VALUES(1,$1,$2,$3) ON CONFLICT(setting_id) DO UPDATE SET smtp_secret_ciphertext=EXCLUDED.smtp_secret_ciphertext,key_version=EXCLUDED.key_version,updated_by=EXCLUDED.updated_by,updated_at=now()`, envelope.Ciphertext, envelope.KeyVersion, actor.ID); err != nil {
			return Settings{}, err
		}
	case SMTPSecretClear:
		if _, err = tx.Exec(ctx, `DELETE FROM admin_setting_secrets WHERE setting_id=1`); err != nil {
			return Settings{}, err
		}
	}
	after, err := scanSettings(tx.QueryRow(ctx, `UPDATE admin_settings SET smtp_host=$1,smtp_port=$2,smtp_from=$3,smtp_from_name=$4,smtp_starttls=$5,smtp_test_recipient=$8,revision=revision+1,updated_by=$6,updated_at=now() WHERE singleton_id=1 AND revision=$7 RETURNING issuer_name,service_item,minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),smtp_host,smtp_port,smtp_from,smtp_from_name,smtp_starttls,smtp_test_recipient,EXISTS(SELECT 1 FROM admin_setting_secrets WHERE setting_id=1),admin_cidrs,revision,updated_by,created_at,updated_at`, in.SMTPHost, in.SMTPPort, in.SMTPFrom, in.SMTPFromName, in.SMTPStartTLS, actor.ID, expected, in.SMTPTestRecipient))
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrRevisionConflict
	}
	if err != nil {
		return Settings{}, err
	}
	if err = audit(ctx, tx, "admin_settings.smtp.update."+string(change), before, after, actor); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Settings{}, err
	}
	return after, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (r *PostgresRepository) StoreSMTPSecret(ctx context.Context, e SecretEnvelope, expected int64, actor Actor) (Settings, error) {
	return r.changeSecret(ctx, e, true, expected, actor)
}
func (r *PostgresRepository) ClearSMTPSecret(ctx context.Context, expected int64, actor Actor) (Settings, error) {
	return r.changeSecret(ctx, SecretEnvelope{}, false, expected, actor)
}
func (r *PostgresRepository) changeSecret(ctx context.Context, e SecretEnvelope, set bool, expected int64, actor Actor) (Settings, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback(context.Background())
	before, err := scanSettings(tx.QueryRow(ctx, `SELECT s.issuer_name,s.service_item,s.minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),s.smtp_host,s.smtp_port,s.smtp_from,s.smtp_from_name,s.smtp_starttls,s.smtp_test_recipient,EXISTS(SELECT 1 FROM admin_setting_secrets x WHERE x.setting_id=s.singleton_id),s.admin_cidrs,s.revision,s.updated_by,s.created_at,s.updated_at FROM admin_settings s WHERE singleton_id=1 FOR UPDATE`))
	if err != nil {
		return Settings{}, err
	}
	if before.Revision != expected {
		return Settings{}, ErrRevisionConflict
	}
	if set {
		_, err = tx.Exec(ctx, `INSERT INTO admin_setting_secrets(setting_id,smtp_secret_ciphertext,key_version,updated_by) VALUES(1,$1,$2,$3) ON CONFLICT(setting_id) DO UPDATE SET smtp_secret_ciphertext=EXCLUDED.smtp_secret_ciphertext,key_version=EXCLUDED.key_version,updated_by=EXCLUDED.updated_by,updated_at=now()`, e.Ciphertext, e.KeyVersion, actor.ID)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM admin_setting_secrets WHERE setting_id=1`)
	}
	if err != nil {
		return Settings{}, err
	}
	after, err := scanSettings(tx.QueryRow(ctx, `UPDATE admin_settings SET revision=revision+1,updated_by=$1,updated_at=now() WHERE singleton_id=1 AND revision=$2 RETURNING issuer_name,service_item,minimum_request_minor,(SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1),(SELECT policy_version FROM invoice_eligibility_policy WHERE singleton_id=1),smtp_host,smtp_port,smtp_from,smtp_from_name,smtp_starttls,smtp_test_recipient,$3::boolean,admin_cidrs,revision,updated_by,created_at,updated_at`, actor.ID, expected, set))
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrRevisionConflict
	}
	if err != nil {
		return Settings{}, err
	}
	action := "admin_settings.smtp_secret.clear"
	if set {
		action = "admin_settings.smtp_secret.set"
	}
	if err = audit(ctx, tx, action, before, after, actor); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Settings{}, err
	}
	return after, nil
}

func (r *PostgresRepository) LoadSMTPSecret(ctx context.Context) (SecretEnvelope, error) {
	var e SecretEnvelope
	err := r.pool.QueryRow(ctx, `SELECT smtp_secret_ciphertext,key_version FROM admin_setting_secrets WHERE setting_id=1`).Scan(&e.Ciphertext, &e.KeyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrSecretMissing
	}
	return e, err
}

type rowScanner interface{ Scan(...any) error }

func scanSettings(row rowScanner) (Settings, error) {
	var s Settings
	var cidrs []*net.IPNet
	err := row.Scan(&s.IssuerName, &s.ServiceItem, &s.MinimumRequestMinor, &s.EligibilityStartAt, &s.EligibilityPolicyVersion, &s.SMTPHost, &s.SMTPPort, &s.SMTPFrom, &s.SMTPFromName, &s.SMTPStartTLS, &s.SMTPTestRecipient, &s.SMTPSecretConfigured, &cidrs, &s.Revision, &s.UpdatedBy, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotConfigured
	}
	if err != nil {
		return s, err
	}
	s.AdminCIDRs = make([]string, 0, len(cidrs))
	for _, network := range cidrs {
		if network == nil {
			return Settings{}, errors.New("admin settings contain a null CIDR")
		}
		s.AdminCIDRs = append(s.AdminCIDRs, network.String())
	}
	return s, err
}
func audit(ctx context.Context, tx pgx.Tx, action string, before, after Settings, actor Actor) error {
	beforeHash := hash(before)
	afterHash := hash(after)
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(id,actor_type,actor_id,action,object_type,object_id,request_id,source_ip_hmac,before_hash,after_hash,reason)VALUES($1,'admin',$2,$3,'admin_settings','1',$4,NULLIF($5,''),$6,$7,$8)`, randomUUID(), actor.ID, action, actor.RequestID, actor.SourceIPHash, beforeHash, afterHash, actor.Reason)
	return err
}
func hash(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	raw := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:])
}

// --- 企业微信通知地址（XM-INV-NOTICE-WEBHOOK-SETTING）---------------------
//
// 独立的单例表，理由见 0028 迁移与 Repository 接口上的注释。写操作各自审计，
// 与 SMTP 口令一样：**审计里只有指纹，没有地址**。

func (r *PostgresRepository) GetNoticeWebhook(ctx context.Context) (NoticeWebhookInfo, error) {
	var info NoticeWebhookInfo
	err := r.pool.QueryRow(ctx, `SELECT fingerprint,updated_by,updated_at
		FROM notice_webhook_setting WHERE singleton_id=1`).
		Scan(&info.Fingerprint, &info.UpdatedBy, &info.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 没配不是错误：通知功能默认关着。
		return NoticeWebhookInfo{}, nil
	}
	if err != nil {
		return NoticeWebhookInfo{}, err
	}
	info.Configured = true
	return info, nil
}

func (r *PostgresRepository) StoreNoticeWebhook(ctx context.Context, envelope SecretEnvelope, fingerprint string, actor Actor) (NoticeWebhookInfo, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return NoticeWebhookInfo{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	before, err := noticeWebhookTx(ctx, tx)
	if err != nil {
		return NoticeWebhookInfo{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO notice_webhook_setting(
			singleton_id,webhook_ciphertext,key_version,fingerprint,updated_by)
		VALUES(1,$1,$2,$3,$4)
		ON CONFLICT (singleton_id) DO UPDATE SET
			webhook_ciphertext=EXCLUDED.webhook_ciphertext,
			key_version=EXCLUDED.key_version,
			fingerprint=EXCLUDED.fingerprint,
			updated_by=EXCLUDED.updated_by,
			updated_at=now()`,
		envelope.Ciphertext, envelope.KeyVersion, fingerprint, actor.ID); err != nil {
		return NoticeWebhookInfo{}, err
	}
	after, err := noticeWebhookTx(ctx, tx)
	if err != nil {
		return NoticeWebhookInfo{}, err
	}
	if err = auditNoticeWebhook(ctx, tx, "admin_settings.notice_webhook.set", before, after, actor); err != nil {
		return NoticeWebhookInfo{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NoticeWebhookInfo{}, err
	}
	return after, nil
}

func (r *PostgresRepository) ClearNoticeWebhook(ctx context.Context, actor Actor) (NoticeWebhookInfo, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return NoticeWebhookInfo{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	before, err := noticeWebhookTx(ctx, tx)
	if err != nil {
		return NoticeWebhookInfo{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM notice_webhook_setting WHERE singleton_id=1`); err != nil {
		return NoticeWebhookInfo{}, err
	}
	if err = auditNoticeWebhook(ctx, tx, "admin_settings.notice_webhook.clear", before, NoticeWebhookInfo{}, actor); err != nil {
		return NoticeWebhookInfo{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NoticeWebhookInfo{}, err
	}
	return NoticeWebhookInfo{}, nil
}

func (r *PostgresRepository) LoadNoticeWebhook(ctx context.Context) (SecretEnvelope, error) {
	var envelope SecretEnvelope
	err := r.pool.QueryRow(ctx, `SELECT webhook_ciphertext,key_version
		FROM notice_webhook_setting WHERE singleton_id=1`).
		Scan(&envelope.Ciphertext, &envelope.KeyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return SecretEnvelope{}, ErrSecretMissing
	}
	return envelope, err
}

func noticeWebhookTx(ctx context.Context, tx pgx.Tx) (NoticeWebhookInfo, error) {
	var info NoticeWebhookInfo
	err := tx.QueryRow(ctx, `SELECT fingerprint,updated_by,updated_at
		FROM notice_webhook_setting WHERE singleton_id=1`).
		Scan(&info.Fingerprint, &info.UpdatedBy, &info.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NoticeWebhookInfo{}, nil
	}
	if err != nil {
		return NoticeWebhookInfo{}, err
	}
	info.Configured = true
	return info, nil
}

// auditNoticeWebhook 记一条审计。**before/after 哈希算的是指纹与配置状态**，
// 不是地址——地址从不进这个函数，也就永远不会进审计表。
func auditNoticeWebhook(ctx context.Context, tx pgx.Tx, action string, before, after NoticeWebhookInfo, actor Actor) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(
			id,actor_type,actor_id,action,object_type,object_id,request_id,source_ip_hmac,before_hash,after_hash)
		VALUES($1,'admin',$2,$3,'notice_webhook_setting','1',$4,$5,$6,$7)`,
		randomUUID(), actor.ID, action, actor.RequestID, actor.SourceIPHash,
		noticeWebhookHash(before), noticeWebhookHash(after))
	return err
}

func noticeWebhookHash(info NoticeWebhookInfo) string {
	if !info.Configured {
		return "not-configured"
	}
	return info.Fingerprint
}
