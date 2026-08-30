package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/server/gen"
)

// Store 是服务器登记簿的仓储。
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

// dateValue 把库里的 date 读回 time.Time；NULL → 零值（「未登记」，与
// Asset.ExpiresAt 等字段的零值语义一致）。
func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

// dateArg 把 time.Time 写成库里的 date；零值 → NULL。
func dateArg(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func int32Arg(i *int) *int32 {
	if i == nil {
		return nil
	}
	v := int32(*i)
	return &v
}

func intValue(i *int32) *int {
	if i == nil {
		return nil
	}
	v := int(*i)
	return &v
}

// ipAddressesJSON 把 IP 列表编码成 jsonb 数组；nil/空切片编码成 `[]`——
// 与库层 CHECK（jsonb_typeof = 'array'）一致，绝不写出 NULL 或裸标量。
func ipAddressesJSON(ips []string) ([]byte, error) {
	if ips == nil {
		ips = []string{}
	}
	b, err := json.Marshal(ips)
	if err != nil {
		return nil, fmt.Errorf("编码 ip_addresses: %w", err)
	}
	return b, nil
}

// ipAddressesFromJSON 把库里的 jsonb 数组解回 IP 列表。解不出来是真错误
// （数据形状与库层 CHECK 矛盾），不静默丢弃——那会让运营以为一台服务器
// 没有登记任何 IP，而其实是这一行的数据坏了。
func ipAddressesFromJSON(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 ip_addresses: %w", err)
	}
	return out, nil
}

// --- Asset ---------------------------------------------------------------

func assetFromRow(r gen.CoreServerAsset) (Asset, error) {
	ips, err := ipAddressesFromJSON(r.IpAddresses)
	if err != nil {
		return Asset{}, fmt.Errorf("资产 %s: %w", r.ID, err)
	}
	supplierID := uuid.Nil
	if r.SupplierID != nil {
		supplierID = *r.SupplierID
	}
	return Asset{
		ID:                    r.ID,
		Hostname:              r.Hostname,
		IPAddresses:           ips,
		Datacenter:            textValue(r.Datacenter),
		SupplierID:            supplierID,
		VCPU:                  intValue(r.Vcpu),
		MemoryGB:              intValue(r.MemoryGb),
		DiskGB:                intValue(r.DiskGb),
		Purpose:               textValue(r.Purpose),
		Status:                AssetStatus(r.Status),
		MonthlyCostMinorUnits: r.MonthlyCostMinorUnits,
		Currency:              textValue(r.Currency),
		BillingCycle:          BillingCycle(textValue(r.BillingCycle)),
		ExpiresAt:             dateValue(r.ExpiresAt),
		Notes:                 textValue(r.Notes),
		Environment:           r.Environment,
		CreatedAt:             fromTS(r.CreatedAt),
		UpdatedAt:             fromTS(r.UpdatedAt),
	}, nil
}

func assetsFromRows(rows []gen.CoreServerAsset) ([]Asset, error) {
	out := make([]Asset, 0, len(rows))
	for _, r := range rows {
		a, err := assetFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func supplierIDArg(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	v := id
	return &v
}

// CreateAsset 登记一台服务器资产。
func (s *Store) CreateAsset(ctx context.Context, in Asset) (Asset, error) {
	if in.Status == "" {
		in.Status = AssetActive
	}
	if err := in.Validate(); err != nil {
		return Asset{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	ips, err := ipAddressesJSON(in.IPAddresses)
	if err != nil {
		return Asset{}, err
	}
	row, err := s.q.InsertServerAsset(ctx, gen.InsertServerAssetParams{
		ID:                    in.ID,
		Hostname:              in.Hostname,
		IpAddresses:           ips,
		Datacenter:            textPtr(in.Datacenter),
		SupplierID:            supplierIDArg(in.SupplierID),
		Vcpu:                  int32Arg(in.VCPU),
		MemoryGb:              int32Arg(in.MemoryGB),
		DiskGb:                int32Arg(in.DiskGB),
		Purpose:               textPtr(in.Purpose),
		Status:                string(in.Status),
		MonthlyCostMinorUnits: in.MonthlyCostMinorUnits,
		Currency:              textPtr(in.Currency),
		BillingCycle:          textPtr(string(in.BillingCycle)),
		ExpiresAt:             dateArg(in.ExpiresAt),
		Notes:                 textPtr(in.Notes),
		Environment:           in.Environment,
	})
	if err != nil {
		return Asset{}, fmt.Errorf("insert server asset: %w", err)
	}
	return assetFromRow(row)
}

// UpdateAsset 改资产的可编辑字段（整行替换，与 finance.UpstreamAccount 的
// 编辑语义一致：前端总是先把当前值预填进表单再提交，见 UpstreamAccountDialog）。
//
// environment 不在可编辑范围：身份边界，改它等于把一条生产资产搬进
// staging（宪法 15 条）。要变只能新登记一条。
func (s *Store) UpdateAsset(ctx context.Context, in Asset) (Asset, error) {
	if in.ID == uuid.Nil {
		return Asset{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if err := in.Validate(); err != nil {
		return Asset{}, err
	}
	ips, err := ipAddressesJSON(in.IPAddresses)
	if err != nil {
		return Asset{}, err
	}
	row, err := s.q.UpdateServerAsset(ctx, gen.UpdateServerAssetParams{
		ID:                    in.ID,
		Hostname:              in.Hostname,
		IpAddresses:           ips,
		Datacenter:            textPtr(in.Datacenter),
		SupplierID:            supplierIDArg(in.SupplierID),
		Vcpu:                  int32Arg(in.VCPU),
		MemoryGb:              int32Arg(in.MemoryGB),
		DiskGb:                int32Arg(in.DiskGB),
		Purpose:               textPtr(in.Purpose),
		Status:                string(in.Status),
		MonthlyCostMinorUnits: in.MonthlyCostMinorUnits,
		Currency:              textPtr(in.Currency),
		BillingCycle:          textPtr(string(in.BillingCycle)),
		ExpiresAt:             dateArg(in.ExpiresAt),
		Notes:                 textPtr(in.Notes),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, fmt.Errorf("server asset %s: %w", in.ID, ErrNotFound)
	}
	if err != nil {
		return Asset{}, fmt.Errorf("update server asset: %w", err)
	}
	return assetFromRow(row)
}

// SetAssetStatus 只改状态（供 server.asset.retire@1 使用，同
// finance.Store.SetRechargeRatio 把「能被单独审计检索」的字段单独开一条
// 写路径的理由）。
func (s *Store) SetAssetStatus(ctx context.Context, id uuid.UUID, status AssetStatus) (Asset, error) {
	if id == uuid.Nil {
		return Asset{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if _, err := ParseAssetStatus(string(status)); err != nil {
		return Asset{}, err
	}
	row, err := s.q.SetServerAssetStatus(ctx, gen.SetServerAssetStatusParams{
		ID:     id,
		Status: string(status),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, fmt.Errorf("server asset %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Asset{}, fmt.Errorf("set server asset status: %w", err)
	}
	return assetFromRow(row)
}

// GetAsset 按 ID 取资产；不存在返回 ErrNotFound。
func (s *Store) GetAsset(ctx context.Context, id uuid.UUID) (Asset, error) {
	row, err := s.q.GetServerAsset(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, fmt.Errorf("server asset %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Asset{}, fmt.Errorf("get server asset: %w", err)
	}
	return assetFromRow(row)
}

// ListAssetsByEnvironment 列出某环境下的全部资产。
func (s *Store) ListAssetsByEnvironment(ctx context.Context, environment string) ([]Asset, error) {
	rows, err := s.q.ListServerAssetsByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list server assets: %w", err)
	}
	return assetsFromRows(rows)
}

// --- Supplier --------------------------------------------------------------

func supplierFromRow(r gen.CoreServerSupplier) Supplier {
	return Supplier{
		ID:          r.ID,
		Name:        r.Name,
		Website:     textValue(r.Website),
		ConsoleURL:  textValue(r.ConsoleUrl),
		ContactName: textValue(r.ContactName),
		ContactInfo: textValue(r.ContactInfo),
		Notes:       textValue(r.Notes),
		Environment: r.Environment,
		CreatedAt:   fromTS(r.CreatedAt),
		UpdatedAt:   fromTS(r.UpdatedAt),
	}
}

// CreateSupplier 登记一个服务器供应商。
func (s *Store) CreateSupplier(ctx context.Context, in Supplier) (Supplier, error) {
	if err := in.Validate(); err != nil {
		return Supplier{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	row, err := s.q.InsertServerSupplier(ctx, gen.InsertServerSupplierParams{
		ID:          in.ID,
		Name:        in.Name,
		Website:     textPtr(in.Website),
		ConsoleUrl:  textPtr(in.ConsoleURL),
		ContactName: textPtr(in.ContactName),
		ContactInfo: textPtr(in.ContactInfo),
		Notes:       textPtr(in.Notes),
		Environment: in.Environment,
	})
	if err != nil {
		return Supplier{}, fmt.Errorf("insert server supplier: %w", err)
	}
	return supplierFromRow(row), nil
}

// UpdateSupplier 改供应商的可编辑字段（整行替换）。
func (s *Store) UpdateSupplier(ctx context.Context, in Supplier) (Supplier, error) {
	if in.ID == uuid.Nil {
		return Supplier{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if err := in.Validate(); err != nil {
		return Supplier{}, err
	}
	row, err := s.q.UpdateServerSupplier(ctx, gen.UpdateServerSupplierParams{
		ID:          in.ID,
		Name:        in.Name,
		Website:     textPtr(in.Website),
		ConsoleUrl:  textPtr(in.ConsoleURL),
		ContactName: textPtr(in.ContactName),
		ContactInfo: textPtr(in.ContactInfo),
		Notes:       textPtr(in.Notes),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Supplier{}, fmt.Errorf("server supplier %s: %w", in.ID, ErrNotFound)
	}
	if err != nil {
		return Supplier{}, fmt.Errorf("update server supplier: %w", err)
	}
	return supplierFromRow(row), nil
}

// GetSupplier 按 ID 取供应商；不存在返回 ErrNotFound。
func (s *Store) GetSupplier(ctx context.Context, id uuid.UUID) (Supplier, error) {
	row, err := s.q.GetServerSupplier(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Supplier{}, fmt.Errorf("server supplier %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Supplier{}, fmt.Errorf("get server supplier: %w", err)
	}
	return supplierFromRow(row), nil
}

// ListSuppliersByEnvironment 列出某环境下的全部供应商。
func (s *Store) ListSuppliersByEnvironment(ctx context.Context, environment string) ([]Supplier, error) {
	rows, err := s.q.ListServerSuppliersByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list server suppliers: %w", err)
	}
	out := make([]Supplier, 0, len(rows))
	for _, r := range rows {
		out = append(out, supplierFromRow(r))
	}
	return out, nil
}

// --- ServerDomain ------------------------------------------------------------

func domainFromRow(r gen.CoreServerDomain) ServerDomain {
	return ServerDomain{
		ID:               r.ID,
		DomainName:       r.DomainName,
		Registrar:        textValue(r.Registrar),
		DNSProvider:      textValue(r.DnsProvider),
		ExpiresAt:        dateValue(r.ExpiresAt),
		CertSource:       CertSource(textValue(r.CertSource)),
		CertExpiresAt:    dateValue(r.CertExpiresAt),
		BoundServiceNote: textValue(r.BoundServiceNote),
		Environment:      r.Environment,
		CreatedAt:        fromTS(r.CreatedAt),
		UpdatedAt:        fromTS(r.UpdatedAt),
	}
}

// CreateServerDomain 登记一个域名。
func (s *Store) CreateServerDomain(ctx context.Context, in ServerDomain) (ServerDomain, error) {
	if err := in.Validate(); err != nil {
		return ServerDomain{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	row, err := s.q.InsertServerDomain(ctx, gen.InsertServerDomainParams{
		ID:               in.ID,
		DomainName:       in.DomainName,
		Registrar:        textPtr(in.Registrar),
		DnsProvider:      textPtr(in.DNSProvider),
		ExpiresAt:        dateArg(in.ExpiresAt),
		CertSource:       textPtr(string(in.CertSource)),
		CertExpiresAt:    dateArg(in.CertExpiresAt),
		BoundServiceNote: textPtr(in.BoundServiceNote),
		Environment:      in.Environment,
	})
	if err != nil {
		return ServerDomain{}, fmt.Errorf("insert server domain: %w", err)
	}
	return domainFromRow(row), nil
}

// UpdateServerDomain 改域名的可编辑字段（整行替换）。
func (s *Store) UpdateServerDomain(ctx context.Context, in ServerDomain) (ServerDomain, error) {
	if in.ID == uuid.Nil {
		return ServerDomain{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if err := in.Validate(); err != nil {
		return ServerDomain{}, err
	}
	row, err := s.q.UpdateServerDomain(ctx, gen.UpdateServerDomainParams{
		ID:               in.ID,
		DomainName:       in.DomainName,
		Registrar:        textPtr(in.Registrar),
		DnsProvider:      textPtr(in.DNSProvider),
		ExpiresAt:        dateArg(in.ExpiresAt),
		CertSource:       textPtr(string(in.CertSource)),
		CertExpiresAt:    dateArg(in.CertExpiresAt),
		BoundServiceNote: textPtr(in.BoundServiceNote),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerDomain{}, fmt.Errorf("server domain %s: %w", in.ID, ErrNotFound)
	}
	if err != nil {
		return ServerDomain{}, fmt.Errorf("update server domain: %w", err)
	}
	return domainFromRow(row), nil
}

// GetServerDomain 按 ID 取域名；不存在返回 ErrNotFound。
func (s *Store) GetServerDomain(ctx context.Context, id uuid.UUID) (ServerDomain, error) {
	row, err := s.q.GetServerDomain(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerDomain{}, fmt.Errorf("server domain %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return ServerDomain{}, fmt.Errorf("get server domain: %w", err)
	}
	return domainFromRow(row), nil
}

// ListServerDomainsByEnvironment 列出某环境下的全部域名。
func (s *Store) ListServerDomainsByEnvironment(ctx context.Context, environment string) ([]ServerDomain, error) {
	rows, err := s.q.ListServerDomainsByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list server domains: %w", err)
	}
	out := make([]ServerDomain, 0, len(rows))
	for _, r := range rows {
		out = append(out, domainFromRow(r))
	}
	return out, nil
}

// --- ServiceNote -----------------------------------------------------------

func serviceNoteFromRow(r gen.CoreServerServiceNote) ServiceNote {
	return ServiceNote{
		ID:          r.ID,
		ServerID:    r.ServerID,
		ServiceName: r.ServiceName,
		ServiceKind: ServiceKind(r.ServiceKind),
		Port:        intValue(r.Port),
		Notes:       textValue(r.Notes),
		CreatedAt:   fromTS(r.CreatedAt),
		UpdatedAt:   fromTS(r.UpdatedAt),
	}
}

// CreateServiceNote 登记一条服务/容器备注。
func (s *Store) CreateServiceNote(ctx context.Context, in ServiceNote) (ServiceNote, error) {
	if err := in.Validate(); err != nil {
		return ServiceNote{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	row, err := s.q.InsertServerServiceNote(ctx, gen.InsertServerServiceNoteParams{
		ID:          in.ID,
		ServerID:    in.ServerID,
		ServiceName: in.ServiceName,
		ServiceKind: string(in.ServiceKind),
		Port:        int32Arg(in.Port),
		Notes:       textPtr(in.Notes),
	})
	if err != nil {
		return ServiceNote{}, fmt.Errorf("insert server service note: %w", err)
	}
	return serviceNoteFromRow(row), nil
}

// UpdateServiceNote 改服务备注的可编辑字段（不含 server_id，见迁移注释：
// 换服务器等于把审计事件变成另一件事，要改归属只能删了重登）。
func (s *Store) UpdateServiceNote(ctx context.Context, in ServiceNote) (ServiceNote, error) {
	if in.ID == uuid.Nil {
		return ServiceNote{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if err := in.Validate(); err != nil {
		return ServiceNote{}, err
	}
	row, err := s.q.UpdateServerServiceNote(ctx, gen.UpdateServerServiceNoteParams{
		ID:          in.ID,
		ServiceName: in.ServiceName,
		ServiceKind: string(in.ServiceKind),
		Port:        int32Arg(in.Port),
		Notes:       textPtr(in.Notes),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceNote{}, fmt.Errorf("server service note %s: %w", in.ID, ErrNotFound)
	}
	if err != nil {
		return ServiceNote{}, fmt.Errorf("update server service note: %w", err)
	}
	return serviceNoteFromRow(row), nil
}

// GetServiceNote 按 ID 取服务备注；不存在返回 ErrNotFound。
func (s *Store) GetServiceNote(ctx context.Context, id uuid.UUID) (ServiceNote, error) {
	row, err := s.q.GetServerServiceNote(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceNote{}, fmt.Errorf("server service note %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return ServiceNote{}, fmt.Errorf("get server service note: %w", err)
	}
	return serviceNoteFromRow(row), nil
}

// DeleteServiceNote 删除一条服务备注。
//
// 删不到返回 ErrNotFound 而不是静默成功——同 finance.Store.DeleteTokenMapping
// 的理由：一个「以为删掉了」的错误登记会继续显示在资产详情里。
func (s *Store) DeleteServiceNote(ctx context.Context, id uuid.UUID) error {
	affected, err := s.q.DeleteServerServiceNote(ctx, id)
	if err != nil {
		return fmt.Errorf("delete server service note: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("server service note %s: %w", id, ErrNotFound)
	}
	return nil
}

// ListServiceNotesByServer 列出一台服务器下的全部服务备注。
func (s *Store) ListServiceNotesByServer(ctx context.Context, serverID uuid.UUID) ([]ServiceNote, error) {
	rows, err := s.q.ListServerServiceNotesByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list server service notes by server: %w", err)
	}
	out := make([]ServiceNote, 0, len(rows))
	for _, r := range rows {
		out = append(out, serviceNoteFromRow(r))
	}
	return out, nil
}

// ListServiceNotesByEnvironment 一次取回某环境下全部服务备注（经 JOIN
// server_asset 过滤，同 finance.Store.ListTokenMappingsByEnvironment 的理由：
// 服务备注没有自己的 environment 列）。
func (s *Store) ListServiceNotesByEnvironment(ctx context.Context, environment string) ([]ServiceNote, error) {
	rows, err := s.q.ListServerServiceNotesByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list server service notes by environment: %w", err)
	}
	out := make([]ServiceNote, 0, len(rows))
	for _, r := range rows {
		out = append(out, serviceNoteFromRow(r))
	}
	return out, nil
}
