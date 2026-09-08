package extapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 是前端应用登记簿与发布记录簿的 PostgreSQL 仓储。
//
// 手写 pgx 而不是 sqlc：与 internal/platform/approval（本仓库最近一次新建
// 带表的模块，迁移 000049）同一个选择。本包只有七条查询，而引入一个新的
// sqlc 输出块会把整仓的 gen/*.go 拖进一次重新生成——XM-SERVER0 的交接文档
// 记过那次顺带发现的既有生成漂移，代价与收益不成比例。
//
// 写方法一律先跑领域 Validate() 再触库：领域校验是第一道闸，数据库 CHECK 是
// 第二道（纵深防御）。所有写入必须由 Action 层调用（ADR-003）。
type Store struct {
	pool *pgxpool.Pool
}

// NewStore 创建仓储。
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// AppWithRelease 是「一个应用 + 它现在跑的那个版本」。
//
// 当前版本**不落库成一列**（不存 current_release_id）：那是一份可以与
// ext_app_release 不一致的副本，而它唯一的来源就是那张表。这里按
// released_at 倒序取第一条现算——列表页最多几十行，代价可以忽略，
// 换来的是「登记了一次发布，列表上的版本立刻就对」这条不需要维护的性质。
type AppWithRelease struct {
	App
	// CurrentRelease 为 nil = 这个应用还没有登记过任何一次发布。
	// **不是**「版本未知」的占位：没登记就是没登记，前端据此显示「未登记」
	// 而不是一个看起来正常的版本号。
	CurrentRelease *Release
}

const appColumns = `id, app_key, display_name, primary_domain, auth_mode,
	owner, status, notes, environment, created_at, updated_at`

const releaseColumns = `id, app_id, version, commit_sha, kind,
	released_at, released_by, notes, created_at`

func wrapNotFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return err
}

func textValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func textPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// scanApp 读一行 App；列顺序必须与 appColumns 一致。
func scanApp(row pgx.Row) (App, error) {
	var (
		a         App
		domain    *string
		authMode  *string
		status    string
		notes     *string
		createdAt time.Time
		updatedAt time.Time
	)
	// 枚举列一律先扫成 string 再转成命名类型：pgx 对命名字符串类型走的是
	// 反射兜底，扫到什么全看驱动的心情，而这几列是判定分支的依据。
	if err := row.Scan(&a.ID, &a.AppKey, &a.DisplayName, &domain, &authMode,
		&a.Owner, &status, &notes, &a.Environment, &createdAt, &updatedAt); err != nil {
		return App{}, err
	}
	a.Status = AppStatus(status)
	a.PrimaryDomain = textValue(domain)
	a.AuthMode = AuthMode(textValue(authMode))
	a.Notes = textValue(notes)
	a.CreatedAt = createdAt.UTC()
	a.UpdatedAt = updatedAt.UTC()
	return a, nil
}

// scanRelease 读一行 Release；列顺序必须与 releaseColumns 一致。
func scanRelease(row pgx.Row) (Release, error) {
	var (
		r          Release
		commit     *string
		kind       string
		notes      *string
		releasedAt time.Time
		createdAt  time.Time
	)
	if err := row.Scan(&r.ID, &r.AppID, &r.Version, &commit, &kind,
		&releasedAt, &r.ReleasedBy, &notes, &createdAt); err != nil {
		return Release{}, err
	}
	r.Kind = ReleaseKind(kind)
	r.CommitSHA = textValue(commit)
	r.Notes = textValue(notes)
	r.ReleasedAt = releasedAt.UTC()
	r.CreatedAt = createdAt.UTC()
	return r, nil
}

// --- App ------------------------------------------------------------------

// CreateApp 登记一个前端站点。
func (s *Store) CreateApp(ctx context.Context, in App) (App, error) {
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	if err := in.Validate(); err != nil {
		return App{}, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO core.ext_app
			(id, app_key, display_name, primary_domain, auth_mode, owner, status, notes, environment)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+appColumns,
		in.ID, in.AppKey, in.DisplayName, textPtr(in.PrimaryDomain), textPtr(string(in.AuthMode)),
		in.Owner, string(in.Status), textPtr(in.Notes), in.Environment)
	a, err := scanApp(row)
	if err != nil {
		return App{}, fmt.Errorf("create ext app: %w", err)
	}
	return a, nil
}

// UpdateApp 整行替换一个已登记的站点。
//
// environment 不在 SET 子句里：一条登记不会「换环境」——那是另一条登记。
// 允许改环境等于允许把一条 staging 的记录悄悄搬进生产台账（宪法 15 条）。
func (s *Store) UpdateApp(ctx context.Context, in App) (App, error) {
	if in.ID == uuid.Nil {
		return App{}, fmt.Errorf("id 不能为空: %w", ErrMissingField)
	}
	if err := in.Validate(); err != nil {
		return App{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE core.ext_app SET
			app_key = $2, display_name = $3, primary_domain = $4, auth_mode = $5,
			owner = $6, status = $7, notes = $8, updated_at = now()
		WHERE id = $1
		RETURNING `+appColumns,
		in.ID, in.AppKey, in.DisplayName, textPtr(in.PrimaryDomain), textPtr(string(in.AuthMode)),
		in.Owner, string(in.Status), textPtr(in.Notes))
	a, err := scanApp(row)
	if err != nil {
		return App{}, wrapNotFound(fmt.Errorf("update ext app: %w", err), "update ext app")
	}
	return a, nil
}

// SetAppStatus 只改状态（供「下线」这类单独的状态迁移使用）。
func (s *Store) SetAppStatus(ctx context.Context, id uuid.UUID, status AppStatus) (App, error) {
	if _, err := ParseAppStatus(string(status)); err != nil {
		return App{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE core.ext_app SET status = $2, updated_at = now()
		WHERE id = $1
		RETURNING `+appColumns, id, string(status))
	a, err := scanApp(row)
	if err != nil {
		return App{}, wrapNotFound(fmt.Errorf("set ext app status: %w", err), "set ext app status")
	}
	return a, nil
}

// GetApp 按 ID 读取一条登记。
func (s *Store) GetApp(ctx context.Context, id uuid.UUID) (App, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM core.ext_app WHERE id = $1`, id)
	a, err := scanApp(row)
	if err != nil {
		return App{}, wrapNotFound(fmt.Errorf("get ext app: %w", err), "get ext app")
	}
	return a, nil
}

// ListAppsByEnvironment 列出某环境下的全部登记，每行带上它当前跑的那个版本。
//
// LATERAL 子查询而不是先查应用再逐个查发布：后者是 N+1，而且两批查询之间
// 插进来一次新的发布登记，会让列表里一部分行是新的、一部分是旧的。
func (s *Store) ListAppsByEnvironment(ctx context.Context, environment string) ([]AppWithRelease, error) {
	if err := requireNonEmpty("environment", environment); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.app_key, a.display_name, a.primary_domain, a.auth_mode,
		       a.owner, a.status, a.notes, a.environment, a.created_at, a.updated_at,
		       r.id, r.app_id, r.version, r.commit_sha, r.kind,
		       r.released_at, r.released_by, r.notes, r.created_at
		FROM core.ext_app a
		LEFT JOIN LATERAL (
			SELECT `+releaseColumns+`
			FROM core.ext_app_release
			WHERE app_id = a.id
			ORDER BY released_at DESC, created_at DESC
			LIMIT 1
		) r ON true
		WHERE a.environment = $1
		ORDER BY a.app_key`, environment)
	if err != nil {
		return nil, fmt.Errorf("list ext apps: %w", err)
	}
	defer rows.Close()

	out := make([]AppWithRelease, 0)
	for rows.Next() {
		var (
			item       AppWithRelease
			domain     *string
			authMode   *string
			appStatus  string
			appNotes   *string
			appCreated time.Time
			appUpdated time.Time

			relID         *uuid.UUID
			relAppID      *uuid.UUID
			relVersion    *string
			relCommit     *string
			relKind       *string
			relReleasedAt *time.Time
			relReleasedBy *string
			relNotes      *string
			relCreated    *time.Time
		)
		if err := rows.Scan(&item.ID, &item.AppKey, &item.DisplayName, &domain, &authMode,
			&item.Owner, &appStatus, &appNotes, &item.Environment, &appCreated, &appUpdated,
			&relID, &relAppID, &relVersion, &relCommit, &relKind,
			&relReleasedAt, &relReleasedBy, &relNotes, &relCreated); err != nil {
			return nil, fmt.Errorf("scan ext app: %w", err)
		}
		item.Status = AppStatus(appStatus)
		item.PrimaryDomain = textValue(domain)
		item.AuthMode = AuthMode(textValue(authMode))
		item.Notes = textValue(appNotes)
		item.CreatedAt = appCreated.UTC()
		item.UpdatedAt = appUpdated.UTC()

		// relID 为 nil = LEFT JOIN 没有匹配 = 这个应用没登记过任何发布。
		// 判据取 id 而不是别的列：其余每一列都可能因为「登记时留空」而是 NULL，
		// 只有主键的 NULL 唯一地表示「这一侧根本没有行」。
		if relID != nil {
			rel := Release{
				ID:         *relID,
				Version:    textValue(relVersion),
				CommitSHA:  textValue(relCommit),
				Kind:       ReleaseKind(textValue(relKind)),
				ReleasedBy: textValue(relReleasedBy),
				Notes:      textValue(relNotes),
			}
			if relAppID != nil {
				rel.AppID = *relAppID
			}
			if relReleasedAt != nil {
				rel.ReleasedAt = relReleasedAt.UTC()
			}
			if relCreated != nil {
				rel.CreatedAt = relCreated.UTC()
			}
			item.CurrentRelease = &rel
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ext apps: %w", err)
	}
	return out, nil
}

// --- Release --------------------------------------------------------------

// CreateRelease 记录一次**已经发生**的发布。
//
// 再说一次（包注释、迁移注释、Action 注释各有一份，因为这一条最容易被读反）：
// 本方法不发布任何东西，它只往登记簿里加一行。
func (s *Store) CreateRelease(ctx context.Context, in Release) (Release, error) {
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	if in.Kind == "" {
		in.Kind = ReleaseDeploy
	}
	if err := in.Validate(); err != nil {
		return Release{}, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO core.ext_app_release
			(id, app_id, version, commit_sha, kind, released_at, released_by, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+releaseColumns,
		in.ID, in.AppID, in.Version, textPtr(in.CommitSHA), string(in.Kind),
		in.ReleasedAt.UTC(), in.ReleasedBy, textPtr(in.Notes))
	r, err := scanRelease(row)
	if err != nil {
		return Release{}, fmt.Errorf("create ext app release: %w", err)
	}
	return r, nil
}

// ListReleasesByEnvironment 列出某环境下全部应用的发布记录（新的在前）。
//
// 环境经父行（core.ext_app）判定，不由发布记录自称——它没有 environment 列，
// 正是为了不给「子行与父行环境不一致」留出可能（同 server.ServiceNote）。
func (s *Store) ListReleasesByEnvironment(ctx context.Context, environment string) ([]Release, error) {
	if err := requireNonEmpty("environment", environment); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.app_id, r.version, r.commit_sha, r.kind,
		       r.released_at, r.released_by, r.notes, r.created_at
		FROM core.ext_app_release r
		JOIN core.ext_app a ON a.id = r.app_id
		WHERE a.environment = $1
		ORDER BY r.released_at DESC, r.created_at DESC`, environment)
	if err != nil {
		return nil, fmt.Errorf("list ext app releases: %w", err)
	}
	defer rows.Close()

	out := make([]Release, 0)
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ext app release: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ext app releases: %w", err)
	}
	return out, nil
}
