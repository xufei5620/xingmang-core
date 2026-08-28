package finance

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// Store 是成本登记簿的仓储。
type Store struct {
	q *gen.Queries
}

// NewStore 创建仓储。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{q: gen.New(pool)}
}

func fromTS(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func textPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func textValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ratioToNumeric 把定点倍率转成 pgtype.Numeric。
//
// 走 Int + Exp 而不是 numeric.Scan(字符串)：Ratio 本来就是「分子 + 标度」，
// 这正是 NUMERIC 的内部表示，直接映射既精确又不必经一次文本往返。
// 零值倍率映射成 SQL NULL——「没配倍率」不是「倍率是 0」（§5.1 同款纪律）。
func ratioToNumeric(r money.Ratio) pgtype.Numeric {
	if r.IsZero() {
		return pgtype.Numeric{Valid: false}
	}
	return pgtype.Numeric{
		Int:   big.NewInt(r.Num()),
		Exp:   -r.Scale(),
		Valid: true,
	}
}

// numericToRatio 把库里的 NUMERIC 读回定点倍率。
//
// **读回路径上不经 float**（宪法 13 条）：pgtype.Numeric 已经是精确的
// Int×10^Exp，直接取那两个整数即可。绝不走 Float64Value()——那是把一个
// 精确的十进制数塞进 53 位尾数，1.15 读回来会变成 1.1499999999999999。
// 倍率是除数，尾数错一点点，逐日折算下来就是影子对比里对不上的那一分钱。
//
// NaN 与超出 int64 的分子一律报错而不是退化成某个默认值：一个读不出来的
// 倍率意味着这条渠道算不出成本，那必须让调用方看见（宪法 12 条）。
func numericToRatio(n pgtype.Numeric) (money.Ratio, error) {
	if !n.Valid {
		return money.Ratio{}, nil
	}
	if n.NaN {
		return money.Ratio{}, fmt.Errorf("recharge_ratio 是 NaN: %w", ErrInvalidFormat)
	}
	if n.Int == nil {
		return money.Ratio{}, fmt.Errorf("recharge_ratio 缺少尾数: %w", ErrInvalidFormat)
	}

	num := new(big.Int).Set(n.Int)
	exp := n.Exp
	// 正指数（如 1.5E+2）在 NUMERIC 里合法：把它折进分子，标度归零。
	// 不这么做的话 -exp 会是负标度，Ratio 装不下。
	for exp > 0 {
		num.Mul(num, big.NewInt(10))
		exp--
	}
	if !num.IsInt64() {
		return money.Ratio{}, fmt.Errorf("recharge_ratio 尾数 %s 超出 int64: %w", num, ErrInvalidFormat)
	}
	return money.NewRatio(num.Int64(), -exp)
}

func accountFromRow(r gen.FinanceUpstreamAccount) (UpstreamAccount, error) {
	ratio, err := numericToRatio(r.RechargeRatio)
	if err != nil {
		return UpstreamAccount{}, fmt.Errorf("账号 %s: %w", r.ID, err)
	}
	// 分组倍率走同一条 NUMERIC ↔ Ratio 精确换算（XM-0049）。
	// 它不参与成本，但读回路径上照样不经 float：一个 1.15 变成
	// 1.1499999999999999 的分组倍率在页面上一样是错的。
	groupRate, err := numericToRatio(r.GroupRate)
	if err != nil {
		return UpstreamAccount{}, fmt.Errorf("账号 %s 的 group_rate: %w", r.ID, err)
	}
	return UpstreamAccount{
		ID:              r.ID,
		SystemType:      SystemType(r.SystemType),
		AccessMethod:    AccessMethod(r.AccessMethod),
		UpstreamName:    textValue(r.UpstreamName),
		UpstreamContact: textValue(r.UpstreamContact),
		UpstreamGroup:   textValue(r.UpstreamGroup),
		BaseURL:         textValue(r.BaseUrl),
		CredentialRef:   r.CredentialRef,
		RechargeRatio:   ratio,
		GroupRate:       groupRate,
		Currency:        r.Currency,
		BusinessDayTZ:   r.BusinessDayTz,
		PlatformID:      textValue(r.PlatformID),
		Status:          Status(r.Status),
		Environment:     r.Environment,
		CreatedAt:       fromTS(r.CreatedAt),
		UpdatedAt:       fromTS(r.UpdatedAt),
	}, nil
}

func accountsFromRows(rows []gen.FinanceUpstreamAccount) ([]UpstreamAccount, error) {
	out := make([]UpstreamAccount, 0, len(rows))
	for _, r := range rows {
		a, err := accountFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func mappingFromRow(r gen.FinanceTokenMap) TokenMapping {
	return TokenMapping{
		UpstreamAccountID: r.UpstreamAccountID,
		UpstreamTokenID:   r.UpstreamTokenID,
		OwnAccountID:      r.OwnAccountID,
		CredentialRef:     textValue(r.CredentialRef),
		CreatedAt:         fromTS(r.CreatedAt),
		UpdatedAt:         fromTS(r.UpdatedAt),
	}
}

func mappingsFromRows(rows []gen.FinanceTokenMap) []TokenMapping {
	out := make([]TokenMapping, 0, len(rows))
	for _, r := range rows {
		out = append(out, mappingFromRow(r))
	}
	return out
}

// CreateAccount 登记一个上游账号。
//
// 领域校验在库之前跑一遍：库层 CHECK 会给出 SQLSTATE 23514 与约束名，
// 那对运维不友好；领域层能说清「计量型渠道必须配 recharge_ratio」。
// 两层都留着——见 UpstreamAccount.Validate 的注释。
func (s *Store) CreateAccount(ctx context.Context, in UpstreamAccount) (UpstreamAccount, error) {
	if in.Currency == "" {
		in.Currency = DefaultCurrency
	}
	if in.BusinessDayTZ == "" {
		in.BusinessDayTZ = DefaultBusinessDayTZ
	}
	if in.Status == "" {
		in.Status = StatusActive
	}
	if err := in.Validate(); err != nil {
		return UpstreamAccount{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}

	row, err := s.q.InsertUpstreamAccount(ctx, gen.InsertUpstreamAccountParams{
		ID:              in.ID,
		SystemType:      string(in.SystemType),
		AccessMethod:    string(in.AccessMethod),
		BaseUrl:         textPtr(in.BaseURL),
		CredentialRef:   in.CredentialRef,
		RechargeRatio:   ratioToNumeric(in.RechargeRatio),
		GroupRate:       ratioToNumeric(in.GroupRate),
		Currency:        in.Currency,
		BusinessDayTz:   in.BusinessDayTZ,
		PlatformID:      textPtr(in.PlatformID),
		UpstreamName:    textPtr(in.UpstreamName),
		UpstreamContact: textPtr(in.UpstreamContact),
		UpstreamGroup:   textPtr(in.UpstreamGroup),
		Status:          string(in.Status),
		Environment:     in.Environment,
	})
	if err != nil {
		return UpstreamAccount{}, fmt.Errorf("insert upstream account: %w", err)
	}
	return accountFromRow(row)
}

// UpdateAccount 改登记簿的可编辑字段。
//
// environment 与 access_method 不在可编辑范围（见 db/queries/finance.sql 的注释）：
// 前者是身份边界，后者是成本口径的分叉点。要变只能新登记一条并停用旧的，
// 这样台账里「哪段时间按哪套口径算」才有据可查。
func (s *Store) UpdateAccount(ctx context.Context, in UpstreamAccount) (UpstreamAccount, error) {
	if in.ID == uuid.Nil {
		return UpstreamAccount{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if err := in.Validate(); err != nil {
		return UpstreamAccount{}, err
	}
	row, err := s.q.UpdateUpstreamAccount(ctx, gen.UpdateUpstreamAccountParams{
		ID:              in.ID,
		BaseUrl:         textPtr(in.BaseURL),
		CredentialRef:   in.CredentialRef,
		RechargeRatio:   ratioToNumeric(in.RechargeRatio),
		GroupRate:       ratioToNumeric(in.GroupRate),
		Currency:        in.Currency,
		BusinessDayTz:   in.BusinessDayTZ,
		PlatformID:      textPtr(in.PlatformID),
		UpstreamName:    textPtr(in.UpstreamName),
		UpstreamContact: textPtr(in.UpstreamContact),
		UpstreamGroup:   textPtr(in.UpstreamGroup),
		Status:          string(in.Status),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return UpstreamAccount{}, fmt.Errorf("upstream account %s: %w", in.ID, ErrNotFound)
	}
	if err != nil {
		return UpstreamAccount{}, fmt.Errorf("update upstream account: %w", err)
	}
	return accountFromRow(row)
}

// SetRechargeRatio 只改充值倍率（设计稿 §6.3：倍率修改走 Action + 审计）。
//
// 不追溯历史：已入账的 profit_daily 行各自冻结了当时的 ratio_snapshot
// （§6.3，XM-0037b 实现），所以改倍率只影响此后新算的行。
// 这正是「上游涨价」与「倍率被调整」能被分开的原因。
func (s *Store) SetRechargeRatio(
	ctx context.Context, id uuid.UUID, ratio money.Ratio,
) (UpstreamAccount, error) {
	if id == uuid.Nil {
		return UpstreamAccount{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if ratio.IsZero() || !ratio.IsPositive() {
		return UpstreamAccount{}, fmt.Errorf("recharge_ratio 必须为正: %w", ErrInvalidFormat)
	}
	// 先读一次判接入方式：订阅型不该有倍率（§2.0），库层 CHECK 也会拦，
	// 但那会给出一句约束名，说不清「你其实想改的是 §3.5 的摊销参数」。
	before, err := s.GetAccount(ctx, id)
	if err != nil {
		return UpstreamAccount{}, err
	}
	if before.AccessMethod == AccessSubscriptionAccount {
		return UpstreamAccount{}, fmt.Errorf(
			"订阅型渠道没有 recharge_ratio，成本走 §3.5 摊销: %w", ErrInconsistent)
	}

	row, err := s.q.SetUpstreamAccountRechargeRatio(ctx, gen.SetUpstreamAccountRechargeRatioParams{
		ID:            id,
		RechargeRatio: ratioToNumeric(ratio),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return UpstreamAccount{}, fmt.Errorf("upstream account %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return UpstreamAccount{}, fmt.Errorf("set recharge ratio: %w", err)
	}
	return accountFromRow(row)
}

// GetAccount 按 ID 取登记簿条目；不存在返回 ErrNotFound。
func (s *Store) GetAccount(ctx context.Context, id uuid.UUID) (UpstreamAccount, error) {
	row, err := s.q.GetUpstreamAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpstreamAccount{}, fmt.Errorf("upstream account %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return UpstreamAccount{}, fmt.Errorf("get upstream account: %w", err)
	}
	return accountFromRow(row)
}

// ListAccountsByEnvironment 列出某环境下的全部登记簿条目。
func (s *Store) ListAccountsByEnvironment(
	ctx context.Context, environment string,
) ([]UpstreamAccount, error) {
	rows, err := s.q.ListUpstreamAccountsByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list upstream accounts: %w", err)
	}
	return accountsFromRows(rows)
}

// ListActiveAccountsByAccessMethod 列出某环境下仍在采集的某类账号。
//
// XM-0037b 的 cost_sync 每轮用它取工作清单：接入方式决定走哪套成本算法
// （§2.0），所以任务是按接入方式分别遍历，而不是取全量再在内存里 switch。
func (s *Store) ListActiveAccountsByAccessMethod(
	ctx context.Context, environment string, method AccessMethod,
) ([]UpstreamAccount, error) {
	if _, err := ParseAccessMethod(string(method)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListActiveUpstreamAccountsByAccessMethod(ctx,
		gen.ListActiveUpstreamAccountsByAccessMethodParams{
			Environment:  environment,
			AccessMethod: string(method),
		})
	if err != nil {
		return nil, fmt.Errorf("list active upstream accounts: %w", err)
	}
	return accountsFromRows(rows)
}

// PutTokenMapping 登记或改写一条令牌映射。
func (s *Store) PutTokenMapping(ctx context.Context, in TokenMapping) (TokenMapping, error) {
	if err := in.Validate(); err != nil {
		return TokenMapping{}, err
	}
	row, err := s.q.UpsertTokenMapping(ctx, gen.UpsertTokenMappingParams{
		UpstreamAccountID: in.UpstreamAccountID,
		UpstreamTokenID:   in.UpstreamTokenID,
		OwnAccountID:      in.OwnAccountID,
		CredentialRef:     textPtr(in.CredentialRef),
	})
	if err != nil {
		return TokenMapping{}, fmt.Errorf("upsert token mapping: %w", err)
	}
	return mappingFromRow(row), nil
}

// GetTokenMapping 取一条映射；不存在返回 ErrNotFound。
func (s *Store) GetTokenMapping(
	ctx context.Context, accountID uuid.UUID, tokenID string,
) (TokenMapping, error) {
	row, err := s.q.GetTokenMapping(ctx, gen.GetTokenMappingParams{
		UpstreamAccountID: accountID,
		UpstreamTokenID:   tokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenMapping{}, fmt.Errorf("token mapping %s/%s: %w", accountID, tokenID, ErrNotFound)
	}
	if err != nil {
		return TokenMapping{}, fmt.Errorf("get token mapping: %w", err)
	}
	return mappingFromRow(row), nil
}

// DeleteTokenMapping 删除一条映射。
//
// 删不到返回 ErrNotFound 而不是静默成功：一个「以为删掉了」的错误映射
// 会继续把成本记到别的渠道上，而运营已经不再看它了。
func (s *Store) DeleteTokenMapping(
	ctx context.Context, accountID uuid.UUID, tokenID string,
) error {
	affected, err := s.q.DeleteTokenMapping(ctx, gen.DeleteTokenMappingParams{
		UpstreamAccountID: accountID,
		UpstreamTokenID:   tokenID,
	})
	if err != nil {
		return fmt.Errorf("delete token mapping: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("token mapping %s/%s: %w", accountID, tokenID, ErrNotFound)
	}
	return nil
}

// ListTokenMappingsByAccount 列出一个上游账号下的全部映射。
func (s *Store) ListTokenMappingsByAccount(
	ctx context.Context, accountID uuid.UUID,
) ([]TokenMapping, error) {
	rows, err := s.q.ListTokenMappingsByAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("list token mappings: %w", err)
	}
	return mappingsFromRows(rows), nil
}

// ListTokenMappingsByEnvironment 一次取回某环境下全部映射，按上游账号分组。
//
// 给列表接口用：逐账号查是 N+1，而账号与映射都在几十到几百的量级，
// 一次查回来在内存里分组既省事又快。
func (s *Store) ListTokenMappingsByEnvironment(
	ctx context.Context, environment string,
) (map[uuid.UUID][]TokenMapping, error) {
	rows, err := s.q.ListTokenMappingsByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list token mappings by environment: %w", err)
	}
	out := make(map[uuid.UUID][]TokenMapping)
	for _, r := range rows {
		m := mappingFromRow(r)
		out[m.UpstreamAccountID] = append(out[m.UpstreamAccountID], m)
	}
	return out, nil
}
