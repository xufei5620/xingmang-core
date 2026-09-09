package publishing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore 是 Store 的 PostgreSQL 实现。
//
// environment 显式传入、不从参数读（宪法 15 条）：跨环境读写本就不允许，
// 让调用方指定环境等于把那道边界交给调用方守。
type PgStore struct {
	pool        *pgxpool.Pool
	environment string
}

// NewPgStore 创建仓储。
func NewPgStore(pool *pgxpool.Pool, environment string) *PgStore {
	return &PgStore{pool: pool, environment: environment}
}

var _ Store = (*PgStore)(nil)

// pgErrCode 提取 PostgreSQL 错误码；不是 PgError 时返回空串。
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// ---------------------------------------------------------------------------
// 渠道
// ---------------------------------------------------------------------------

const channelColumns = `id, environment, platform, handle, display_name, purpose,
	credential_ref, status, note, created_by, created_at, updated_at`

func scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	var platform, status string
	if err := row.Scan(&c.ID, &c.Environment, &platform, &c.Handle, &c.DisplayName,
		&c.Purpose, &c.CredentialRef, &status, &c.Note, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return Channel{}, err
	}
	c.Platform = Platform(platform)
	c.Status = ChannelStatus(status)
	return c, nil
}

// UpsertChannel 按 (environment, platform, handle) 登记或更新一个渠道账号。
//
// 冲突键用身份三元组而不是 id：运营重复登记同一个账号时，正确的行为是更新
// 那一条，而不是让页面上出现两行同名账号（谁也说不清发布该走哪一条）。
//
// **status 刻意不在 UPDATE 里**：启停走 SetChannelStatus。否则一次「改个用途」
// 的保存会顺手把一个被暂停的渠道恢复成 ACTIVE——而暂停通常正是因为出了事。
func (s *PgStore) UpsertChannel(ctx context.Context, c Channel) (Channel, error) {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO publishing.channel(`+channelColumns+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		ON CONFLICT (environment, platform, handle) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			purpose      = EXCLUDED.purpose,
			credential_ref = EXCLUDED.credential_ref,
			note         = EXCLUDED.note,
			updated_at   = EXCLUDED.updated_at
		RETURNING `+channelColumns,
		c.ID, s.environment, string(c.Platform), c.Handle, c.DisplayName, c.Purpose,
		c.CredentialRef, string(c.Status), c.Note, c.CreatedBy, c.CreatedAt)
	out, err := scanChannel(row)
	if err != nil {
		if pgErrCode(err) == "23514" {
			// CHECK 违例。最可能的一条是 credential_ref 的形状——领域层已经
			// 挡过一次，能走到库层说明有别的路径绕开了它。
			return Channel{}, fmt.Errorf("%w: 渠道字段不合库层约束", ErrInvalidInput)
		}
		return Channel{}, fmt.Errorf("upsert publishing channel: %w", err)
	}
	return out, nil
}

// SetChannelStatus 改渠道状态。
func (s *PgStore) SetChannelStatus(ctx context.Context, id uuid.UUID, status ChannelStatus, at time.Time) (Channel, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE publishing.channel SET status=$3, updated_at=$4
		WHERE id=$1 AND environment=$2
		RETURNING `+channelColumns, id, s.environment, string(status), at)
	out, err := scanChannel(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("set publishing channel status: %w", err)
	}
	return out, nil
}

// GetChannel 按 id 取渠道。
func (s *PgStore) GetChannel(ctx context.Context, id uuid.UUID) (Channel, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+channelColumns+`
		FROM publishing.channel WHERE id=$1 AND environment=$2`, id, s.environment)
	out, err := scanChannel(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("load publishing channel: %w", err)
	}
	return out, nil
}

// ListChannels 列出本环境的渠道。
func (s *PgStore) ListChannels(ctx context.Context, limit int32) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelColumns+`
		FROM publishing.channel WHERE environment=$1
		ORDER BY platform, handle LIMIT $2`, s.environment, ClampListLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list publishing channels: %w", err)
	}
	defer rows.Close()
	out := make([]Channel, 0)
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan publishing channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 草稿与修订
// ---------------------------------------------------------------------------

const draftColumns = `id, environment, title, body, scheduled_at, status,
	current_version, created_by, created_at, updated_at`

func scanDraft(row pgx.Row) (Draft, error) {
	var d Draft
	var status string
	if err := row.Scan(&d.ID, &d.Environment, &d.Title, &d.Body, &d.ScheduledAt,
		&status, &d.CurrentVersion, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return Draft{}, err
	}
	d.Status = DraftStatus(status)
	return d, nil
}

// statusFor 由排期时间决定草稿状态。
//
// 状态不是调用方给的：一个「SCHEDULED 但没有时间」或「DRAFT 却有时间」的草稿
// 会让内容日历显示一条落在未知格里的条目。库层的 CHECK 也表达同一条规则，
// 这里先算好是为了让错误在应用层就说得清。归档不走这条路径（见 ArchiveDraft）。
func statusFor(scheduledAt *time.Time) DraftStatus {
	if scheduledAt != nil {
		return DraftScheduled
	}
	return DraftDraft
}

// SaveDraft 落一版草稿：新建或在既有草稿上推进版本。
//
// 全程一个事务。版本号用 `current_version + 1` 在 UPDATE 语句里自增而不是
// 「先 SELECT 再 +1」：后者在两个人同时保存时会算出同一个版本号，第二条
// INSERT 撞上 draft_revision 的主键——那时草稿已经被改掉了，回滚也说不清
// 谁的正文留下了。
func (s *PgStore) SaveDraft(ctx context.Context, in DraftInput) (Draft, error) {
	if err := ValidateAssetCount(len(in.AssetIDs)); err != nil {
		return Draft{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Draft{}, fmt.Errorf("begin save draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	status := statusFor(in.ScheduledAt)
	var draft Draft
	if in.ID == nil {
		row := tx.QueryRow(ctx, `
			INSERT INTO publishing.draft(`+draftColumns+`)
			VALUES($1,$2,$3,$4,$5,$6,1,$7,$8,$8)
			RETURNING `+draftColumns,
			uuid.New(), s.environment, in.Title, in.Body, in.ScheduledAt,
			string(status), in.Actor, in.Now)
		draft, err = scanDraft(row)
		if err != nil {
			return Draft{}, fmt.Errorf("insert draft: %w", err)
		}
	} else {
		// 归档的草稿不接受新版本：它已经从工作流里退出了，改它等于让一条
		// 「已归档」的内容悄悄复活。要继续用就先建一条新的。
		row := tx.QueryRow(ctx, `
			UPDATE publishing.draft
			SET title=$3, body=$4, scheduled_at=$5, status=$6,
				current_version = current_version + 1, updated_at=$7
			WHERE id=$1 AND environment=$2 AND status <> 'ARCHIVED'
			RETURNING `+draftColumns,
			*in.ID, s.environment, in.Title, in.Body, in.ScheduledAt,
			string(status), in.Now)
		draft, err = scanDraft(row)
		if errors.Is(err, pgx.ErrNoRows) {
			// 不存在与已归档给同一个错误面：调用方要做的事一样（查一下这条
			// 草稿现在是什么状态），而分开两个码只会多一个可以探测 id 的信道。
			return Draft{}, ErrNotFound
		}
		if err != nil {
			return Draft{}, fmt.Errorf("update draft: %w", err)
		}
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO publishing.draft_revision(draft_id, version, title, body,
			scheduled_at, note, created_by, created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		draft.ID, draft.CurrentVersion, draft.Title, draft.Body, draft.ScheduledAt,
		in.Note, in.Actor, in.Now); err != nil {
		return Draft{}, fmt.Errorf("insert draft revision: %w", err)
	}

	// 素材引用整体重建：这一版引用的就是传进来的这些。增量比对会让「去掉一个
	// 素材」变成一次需要前端算差集的操作，而前端算错的症状是素材悄悄留着。
	if _, err = tx.Exec(ctx, `DELETE FROM publishing.draft_asset WHERE draft_id=$1`, draft.ID); err != nil {
		return Draft{}, fmt.Errorf("clear draft assets: %w", err)
	}
	for i, assetID := range in.AssetIDs {
		if _, err = tx.Exec(ctx, `
			INSERT INTO publishing.draft_asset(draft_id, asset_id, position)
			VALUES($1,$2,$3) ON CONFLICT (draft_id, asset_id) DO NOTHING`,
			draft.ID, assetID, i); err != nil {
			if pgErrCode(err) == "23503" {
				return Draft{}, fmt.Errorf("%w: 素材 %s 不存在", ErrInvalidInput, assetID)
			}
			return Draft{}, fmt.Errorf("insert draft asset: %w", err)
		}
		draft.AssetIDs = append(draft.AssetIDs, assetID)
	}

	if err = tx.Commit(ctx); err != nil {
		return Draft{}, fmt.Errorf("commit save draft: %w", err)
	}
	return draft, nil
}

// ArchiveDraft 归档草稿。
//
// 归档而不是删除：草稿可能已经被发布记录引用（publish_record.draft_id 是
// RESTRICT 外键），而「这条内容当时长什么样」是审计事实。
func (s *PgStore) ArchiveDraft(ctx context.Context, id uuid.UUID, at time.Time) (Draft, error) {
	// 归档同时清掉排期：一条已归档却还占着日历格子的草稿会让运营以为它还要发。
	row := s.pool.QueryRow(ctx, `
		UPDATE publishing.draft SET status='ARCHIVED', scheduled_at=NULL, updated_at=$3
		WHERE id=$1 AND environment=$2
		RETURNING `+draftColumns, id, s.environment, at)
	d, err := scanDraft(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, fmt.Errorf("archive draft: %w", err)
	}
	return d, nil
}

// GetDraft 取草稿并填充素材 id。
func (s *PgStore) GetDraft(ctx context.Context, id uuid.UUID) (Draft, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+draftColumns+`
		FROM publishing.draft WHERE id=$1 AND environment=$2`, id, s.environment)
	d, err := scanDraft(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, fmt.Errorf("load draft: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT asset_id FROM publishing.draft_asset WHERE draft_id=$1 ORDER BY position`, id)
	if err != nil {
		return Draft{}, fmt.Errorf("load draft assets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID uuid.UUID
		if err = rows.Scan(&assetID); err != nil {
			return Draft{}, fmt.Errorf("scan draft asset: %w", err)
		}
		d.AssetIDs = append(d.AssetIDs, assetID)
	}
	return d, rows.Err()
}

// ListDrafts 按过滤条件列出草稿。
//
// 排期区间过滤在 SQL 里做而不是取回来再筛：后者会先被 limit 截断，于是一屏
// 历史草稿就能把本月排期的挤掉，而内容日历会显示「这个月没有排期」——那是假话
// （与 httpapi.ListSilencesHandler 里那条注释同一个坑）。
func (s *PgStore) ListDrafts(ctx context.Context, f DraftFilter) ([]Draft, error) {
	args := []any{s.environment, ClampListLimit(f.Limit)}
	where := "environment=$1"
	if f.Status != "" {
		args = append(args, string(f.Status))
		where += fmt.Sprintf(" AND status=$%d", len(args))
	}
	if !f.ScheduledFrom.IsZero() {
		args = append(args, f.ScheduledFrom)
		where += fmt.Sprintf(" AND scheduled_at >= $%d", len(args))
	}
	if !f.ScheduledTo.IsZero() {
		args = append(args, f.ScheduledTo)
		where += fmt.Sprintf(" AND scheduled_at < $%d", len(args))
	}
	rows, err := s.pool.Query(ctx, `SELECT `+draftColumns+`
		FROM publishing.draft WHERE `+where+`
		ORDER BY COALESCE(scheduled_at, updated_at) DESC, id LIMIT $2`, args...)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}
	defer rows.Close()
	out := make([]Draft, 0)
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, fmt.Errorf("scan draft: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListRevisions 列出一条草稿的全部修订，新的在前。
func (s *PgStore) ListRevisions(ctx context.Context, draftID uuid.UUID) ([]Revision, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.draft_id, r.version, r.title, r.body, r.scheduled_at, r.note,
			r.created_by, r.created_at
		FROM publishing.draft_revision r
		JOIN publishing.draft d ON d.id = r.draft_id
		WHERE r.draft_id=$1 AND d.environment=$2
		ORDER BY r.version DESC`, draftID, s.environment)
	if err != nil {
		return nil, fmt.Errorf("list draft revisions: %w", err)
	}
	defer rows.Close()
	out := make([]Revision, 0)
	for rows.Next() {
		var r Revision
		if err = rows.Scan(&r.DraftID, &r.Version, &r.Title, &r.Body,
			&r.ScheduledAt, &r.Note, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan draft revision: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 素材
// ---------------------------------------------------------------------------

const assetColumns = `id, environment, name, kind, uri, note, created_by, created_at, updated_at`

func scanAsset(row pgx.Row) (Asset, error) {
	var a Asset
	var kind string
	if err := row.Scan(&a.ID, &a.Environment, &a.Name, &kind, &a.URI, &a.Note,
		&a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Asset{}, err
	}
	a.Kind = AssetKind(kind)
	return a, nil
}

// UpsertAsset 按 (environment, name) 登记或更新一条素材引用。
func (s *PgStore) UpsertAsset(ctx context.Context, a Asset) (Asset, error) {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO publishing.asset(`+assetColumns+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)
		ON CONFLICT (environment, name) DO UPDATE SET
			kind = EXCLUDED.kind, uri = EXCLUDED.uri,
			note = EXCLUDED.note, updated_at = EXCLUDED.updated_at
		RETURNING `+assetColumns,
		a.ID, s.environment, a.Name, string(a.Kind), a.URI, a.Note, a.CreatedBy, a.CreatedAt)
	out, err := scanAsset(row)
	if err != nil {
		if pgErrCode(err) == "23514" {
			return Asset{}, fmt.Errorf("%w: 素材字段不合库层约束", ErrInvalidInput)
		}
		return Asset{}, fmt.Errorf("upsert publishing asset: %w", err)
	}
	return out, nil
}

// RemoveAsset 删除素材；仍被草稿引用时返回 ErrConflict。
func (s *PgStore) RemoveAsset(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM publishing.asset WHERE id=$1 AND environment=$2`, id, s.environment)
	if err != nil {
		// **23001 才是 ON DELETE RESTRICT 的码**（restrict_violation），
		// 23503 是 foreign_key_violation。只认后者的话，「素材仍被引用」会漏成
		// 一句原始驱动错误——集成用例撞出来过一次。两个都认：不同 PostgreSQL
		// 版本对同一情形报哪一个不值得赌。
		if code := pgErrCode(err); code == "23001" || code == "23503" {
			return fmt.Errorf("%w: 素材仍被草稿引用，先从草稿里移除", ErrConflict)
		}
		return fmt.Errorf("delete publishing asset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAsset 按 id 取素材。
func (s *PgStore) GetAsset(ctx context.Context, id uuid.UUID) (Asset, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+assetColumns+`
		FROM publishing.asset WHERE id=$1 AND environment=$2`, id, s.environment)
	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, fmt.Errorf("load publishing asset: %w", err)
	}
	return a, nil
}

// ListAssets 列出本环境的素材。
func (s *PgStore) ListAssets(ctx context.Context, limit int32) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+assetColumns+`
		FROM publishing.asset WHERE environment=$1
		ORDER BY updated_at DESC, id LIMIT $2`, s.environment, ClampListLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list publishing assets: %w", err)
	}
	defer rows.Close()
	out := make([]Asset, 0)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan publishing asset: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssetsForDraft 列出一条草稿引用的素材，按位置排序。
func (s *PgStore) AssetsForDraft(ctx context.Context, draftID uuid.UUID) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.environment, a.name, a.kind, a.uri, a.note,
			a.created_by, a.created_at, a.updated_at
		FROM publishing.draft_asset da
		JOIN publishing.asset a ON a.id = da.asset_id
		WHERE da.draft_id=$1 AND a.environment=$2
		ORDER BY da.position`, draftID, s.environment)
	if err != nil {
		return nil, fmt.Errorf("list draft assets: %w", err)
	}
	defer rows.Close()
	out := make([]Asset, 0)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan draft asset: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 发布记录
// ---------------------------------------------------------------------------

const recordColumns = `id, environment, draft_id, draft_version, channel_id,
	scheduled_at, requested_by, result, external_ref, delivered_at,
	detail, created_at`

func scanRecord(row pgx.Row) (PublishRecord, error) {
	var r PublishRecord
	var result string
	if err := row.Scan(&r.ID, &r.Environment, &r.DraftID, &r.DraftVersion, &r.ChannelID,
		&r.ScheduledAt, &r.RequestedBy, &result, &r.ExternalRef,
		&r.DeliveredAt, &r.Detail, &r.CreatedAt); err != nil {
		return PublishRecord{}, err
	}
	r.Result = DeliveryResult(result)
	return r, nil
}

// CreatePublishRecord 落一条发布记录。
func (s *PgStore) CreatePublishRecord(ctx context.Context, r PublishRecord) (PublishRecord, error) {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO publishing.publish_record(`+recordColumns+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING `+recordColumns,
		r.ID, s.environment, r.DraftID, r.DraftVersion, r.ChannelID, r.ScheduledAt,
		r.RequestedBy, string(r.Result), r.ExternalRef, r.DeliveredAt,
		r.Detail, r.CreatedAt)
	out, err := scanRecord(row)
	if err != nil {
		if code := pgErrCode(err); code == "23514" || code == "23503" || code == "23001" {
			// 23514 = 迁移 000052 的两道 CHECK（result 闭集 / 未投递不得带
			// 编号与投递时刻）；23503/23001 = 草稿或渠道不存在 / 被 RESTRICT 挡住。
			return PublishRecord{}, fmt.Errorf("%w: 发布记录不合库层约束", ErrInvalidInput)
		}
		return PublishRecord{}, fmt.Errorf("insert publish record: %w", err)
	}
	return out, nil
}

// ListPublishRecords 列出本环境的发布记录，新的在前。
func (s *PgStore) ListPublishRecords(ctx context.Context, limit int32) ([]PublishRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+recordColumns+`
		FROM publishing.publish_record WHERE environment=$1
		ORDER BY created_at DESC, id LIMIT $2`, s.environment, ClampListLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list publish records: %w", err)
	}
	defer rows.Close()
	out := make([]PublishRecord, 0)
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan publish record: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
