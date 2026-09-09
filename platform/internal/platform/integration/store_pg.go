package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// uniqueViolation 是 Postgres 唯一键冲突的 SQLSTATE（同 alerts.uniqueViolation）。
const uniqueViolation = "23505"

// Store 是两张登记簿的持久化面。
//
// 环境显式传入、不由参数自称（宪法 15 条）：跨环境读写本就不允许，让调用方
// 指定环境等于把那道边界交给调用方守。
type Store struct {
	pool        *pgxpool.Pool
	environment string
	now         func() time.Time
}

// NewStore 创建仓储。now 为 nil 时用 time.Now。
func NewStore(pool *pgxpool.Pool, environment string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{pool: pool, environment: environment, now: now}
}

// Environment 返回本仓储绑定的环境。
func (s *Store) Environment() string { return s.environment }

func mapWriteError(err error, what string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%s: %w", what, ErrDuplicate)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// --- API 调用方登记簿 ---------------------------------------------------

const apiClientColumns = `id, principal_id, principal_type, display_name, purpose, owner,
	expected_scopes, credential_ref, status, notes, environment,
	created_at, created_by, updated_at, updated_by`

func scanAPIClient(row pgx.Row) (APIClient, error) {
	var c APIClient
	var principalType, status string
	err := row.Scan(&c.ID, &c.PrincipalID, &principalType, &c.DisplayName, &c.Purpose,
		&c.Owner, &c.ExpectedScopes, &c.CredentialRef, &status, &c.Notes, &c.Environment,
		&c.CreatedAt, &c.CreatedBy, &c.UpdatedAt, &c.UpdatedBy)
	if err != nil {
		return APIClient{}, err
	}
	c.PrincipalType = principal.Type(principalType)
	c.Status = ClientStatus(status)
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	if c.ExpectedScopes == nil {
		c.ExpectedScopes = []string{}
	}
	return c, nil
}

// CreateAPIClient 新登记一个调用方。
func (s *Store) CreateAPIClient(ctx context.Context, c APIClient) (APIClient, error) {
	c.Environment = s.environment
	c.PrincipalID = strings.TrimSpace(c.PrincipalID)
	c.DisplayName = strings.TrimSpace(c.DisplayName)
	c.CredentialRef = strings.TrimSpace(c.CredentialRef)
	if err := c.Validate(); err != nil {
		return APIClient{}, err
	}
	now := s.now().UTC()
	c.ID = uuid.New()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.ExpectedScopes == nil {
		c.ExpectedScopes = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO core.api_client(`+apiClientColumns+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		RETURNING `+apiClientColumns,
		c.ID, c.PrincipalID, string(c.PrincipalType), c.DisplayName, c.Purpose, c.Owner,
		c.ExpectedScopes, c.CredentialRef, string(c.Status), c.Notes, c.Environment,
		c.CreatedAt, c.CreatedBy, c.UpdatedAt, c.UpdatedBy)
	created, err := scanAPIClient(row)
	if err != nil {
		return APIClient{}, mapWriteError(err, "登记 API 调用方")
	}
	return created, nil
}

// UpdateAPIClient 整行更新一条调用方登记（前端总是先预填当前值再提交，
// 缺键的字段会被清空——同 finance.upstream_account.set 的整行替换语义）。
func (s *Store) UpdateAPIClient(ctx context.Context, id uuid.UUID, c APIClient) (APIClient, error) {
	c.Environment = s.environment
	c.PrincipalID = strings.TrimSpace(c.PrincipalID)
	c.DisplayName = strings.TrimSpace(c.DisplayName)
	c.CredentialRef = strings.TrimSpace(c.CredentialRef)
	if err := c.Validate(); err != nil {
		return APIClient{}, err
	}
	if c.ExpectedScopes == nil {
		c.ExpectedScopes = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE core.api_client
		   SET principal_id=$3, principal_type=$4, display_name=$5, purpose=$6, owner=$7,
		       expected_scopes=$8, credential_ref=$9, status=$10, notes=$11,
		       updated_at=$12, updated_by=$13
		 WHERE id=$1 AND environment=$2
		RETURNING `+apiClientColumns,
		id, s.environment, c.PrincipalID, string(c.PrincipalType), c.DisplayName, c.Purpose,
		c.Owner, c.ExpectedScopes, c.CredentialRef, string(c.Status), c.Notes,
		s.now().UTC(), c.UpdatedBy)
	updated, err := scanAPIClient(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIClient{}, ErrNotFound
	}
	if err != nil {
		return APIClient{}, mapWriteError(err, "更新 API 调用方登记")
	}
	return updated, nil
}

// SetAPIClientStatus 只改状态（列表页的启用/停用按钮用，不必打开完整表单）。
func (s *Store) SetAPIClientStatus(ctx context.Context, id uuid.UUID, status ClientStatus, by string) (APIClient, error) {
	if _, err := ParseClientStatus(string(status)); err != nil {
		return APIClient{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE core.api_client SET status=$3, updated_at=$4, updated_by=$5
		 WHERE id=$1 AND environment=$2
		RETURNING `+apiClientColumns,
		id, s.environment, string(status), s.now().UTC(), by)
	updated, err := scanAPIClient(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIClient{}, ErrNotFound
	}
	if err != nil {
		return APIClient{}, mapWriteError(err, "更新 API 调用方状态")
	}
	return updated, nil
}

// GetAPIClient 按 id 取一条登记（Action 的 before 镜像用）。
func (s *Store) GetAPIClient(ctx context.Context, id uuid.UUID) (APIClient, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+apiClientColumns+` FROM core.api_client WHERE id=$1 AND environment=$2`,
		id, s.environment)
	c, err := scanAPIClient(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIClient{}, ErrNotFound
	}
	if err != nil {
		return APIClient{}, fmt.Errorf("读取 API 调用方登记: %w", err)
	}
	return c, nil
}

// ListAPIClients 取本环境全部登记，按登记时间倒序。
//
// 不分页：调用方登记簿是几十行量级的表（同 core.server_asset 的判断），
// 给它加游标只会让对账页多一层要维护的翻页逻辑。
func (s *Store) ListAPIClients(ctx context.Context) ([]APIClient, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+apiClientColumns+` FROM core.api_client
		  WHERE environment=$1 ORDER BY created_at DESC, id`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("列出 API 调用方登记: %w", err)
	}
	defer rows.Close()
	out := []APIClient{}
	for rows.Next() {
		c, err := scanAPIClient(rows)
		if err != nil {
			return nil, fmt.Errorf("列出 API 调用方登记: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("列出 API 调用方登记: %w", err)
	}
	return out, nil
}

// --- 自动化规则登记簿 ---------------------------------------------------

const automationRuleColumns = `id, name, description, trigger_kind, trigger_detail,
	target_action_id, target_action_version, status, notes, environment,
	created_at, created_by, updated_at, updated_by`

func scanAutomationRule(row pgx.Row) (AutomationRule, error) {
	var r AutomationRule
	var triggerKind, status string
	err := row.Scan(&r.ID, &r.Name, &r.Description, &triggerKind, &r.TriggerDetail,
		&r.TargetActionID, &r.TargetActionVersion, &status, &r.Notes, &r.Environment,
		&r.CreatedAt, &r.CreatedBy, &r.UpdatedAt, &r.UpdatedBy)
	if err != nil {
		return AutomationRule{}, err
	}
	r.TriggerKind = TriggerKind(triggerKind)
	r.Status = RuleStatus(status)
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, nil
}

// CreateAutomationRule 新登记一条规则。
//
// **它不会让这条规则跑起来**——本仓储没有任何写入触发器、没有 NOTIFY、
// 也没有任何调用方在写入之后去执行目标 Action（doc.go 第 2 条）。
func (s *Store) CreateAutomationRule(ctx context.Context, r AutomationRule) (AutomationRule, error) {
	r.Environment = s.environment
	r.Name = strings.TrimSpace(r.Name)
	r.TargetActionID = strings.TrimSpace(r.TargetActionID)
	r.TargetActionVersion = strings.TrimSpace(r.TargetActionVersion)
	if err := r.Validate(); err != nil {
		return AutomationRule{}, err
	}
	now := s.now().UTC()
	r.ID = uuid.New()
	r.CreatedAt, r.UpdatedAt = now, now
	row := s.pool.QueryRow(ctx, `
		INSERT INTO core.automation_rule(`+automationRuleColumns+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+automationRuleColumns,
		r.ID, r.Name, r.Description, string(r.TriggerKind), r.TriggerDetail,
		r.TargetActionID, r.TargetActionVersion, string(r.Status), r.Notes, r.Environment,
		r.CreatedAt, r.CreatedBy, r.UpdatedAt, r.UpdatedBy)
	created, err := scanAutomationRule(row)
	if err != nil {
		return AutomationRule{}, mapWriteError(err, "登记自动化规则")
	}
	return created, nil
}

// UpdateAutomationRule 整行更新一条规则登记。
func (s *Store) UpdateAutomationRule(ctx context.Context, id uuid.UUID, r AutomationRule) (AutomationRule, error) {
	r.Environment = s.environment
	r.Name = strings.TrimSpace(r.Name)
	r.TargetActionID = strings.TrimSpace(r.TargetActionID)
	r.TargetActionVersion = strings.TrimSpace(r.TargetActionVersion)
	if err := r.Validate(); err != nil {
		return AutomationRule{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE core.automation_rule
		   SET name=$3, description=$4, trigger_kind=$5, trigger_detail=$6,
		       target_action_id=$7, target_action_version=$8, status=$9, notes=$10,
		       updated_at=$11, updated_by=$12
		 WHERE id=$1 AND environment=$2
		RETURNING `+automationRuleColumns,
		id, s.environment, r.Name, r.Description, string(r.TriggerKind), r.TriggerDetail,
		r.TargetActionID, r.TargetActionVersion, string(r.Status), r.Notes,
		s.now().UTC(), r.UpdatedBy)
	updated, err := scanAutomationRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationRule{}, ErrNotFound
	}
	if err != nil {
		return AutomationRule{}, mapWriteError(err, "更新自动化规则登记")
	}
	return updated, nil
}

// SetAutomationRuleStatus 只改登记状态。
//
// 再说一遍：三个取值都不会让这条规则跑起来（types.go 的 RuleStatus 注释）。
func (s *Store) SetAutomationRuleStatus(ctx context.Context, id uuid.UUID, status RuleStatus, by string) (AutomationRule, error) {
	if _, err := ParseRuleStatus(string(status)); err != nil {
		return AutomationRule{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE core.automation_rule SET status=$3, updated_at=$4, updated_by=$5
		 WHERE id=$1 AND environment=$2
		RETURNING `+automationRuleColumns,
		id, s.environment, string(status), s.now().UTC(), by)
	updated, err := scanAutomationRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationRule{}, ErrNotFound
	}
	if err != nil {
		return AutomationRule{}, mapWriteError(err, "更新自动化规则状态")
	}
	return updated, nil
}

// GetAutomationRule 按 id 取一条规则登记。
func (s *Store) GetAutomationRule(ctx context.Context, id uuid.UUID) (AutomationRule, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+automationRuleColumns+` FROM core.automation_rule WHERE id=$1 AND environment=$2`,
		id, s.environment)
	r, err := scanAutomationRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationRule{}, ErrNotFound
	}
	if err != nil {
		return AutomationRule{}, fmt.Errorf("读取自动化规则登记: %w", err)
	}
	return r, nil
}

// ListAutomationRules 取本环境全部规则登记，按登记时间倒序。
func (s *Store) ListAutomationRules(ctx context.Context) ([]AutomationRule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+automationRuleColumns+` FROM core.automation_rule
		  WHERE environment=$1 ORDER BY created_at DESC, id`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("列出自动化规则登记: %w", err)
	}
	defer rows.Close()
	out := []AutomationRule{}
	for rows.Next() {
		r, err := scanAutomationRule(rows)
		if err != nil {
			return nil, fmt.Errorf("列出自动化规则登记: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("列出自动化规则登记: %w", err)
	}
	return out, nil
}
