package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 六个 Action 的稳定 ID。
const (
	// ActionAssetSet 登记或修改一台服务器资产。
	ActionAssetSet = "server.asset.set"
	// ActionAssetRetire 单独把资产标记为已退役（供列表页的一键退役按钮使用，
	// 不必打开完整编辑表单重新提交整行）。
	ActionAssetRetire = "server.asset.retire"
	// ActionSupplierSet 登记或修改一个服务器供应商。
	ActionSupplierSet = "server.supplier.set"
	// ActionDomainSet 登记或修改一个域名。
	ActionDomainSet = "server.domain.set"
	// ActionServiceNoteSet 登记或修改一条服务/容器备注。
	ActionServiceNoteSet = "server.service_note.set"
	// ActionServiceNoteRemove 删除一条服务/容器备注。
	ActionServiceNoteRemove = "server.service_note.remove"

	actionVersion = "1"
)

// 审计里的资源类型。
const (
	resourceAsset       = "server.asset"
	resourceSupplier    = "server.supplier"
	resourceDomain      = "server.domain"
	resourceServiceNote = "server.service_note"
)

// allEnvironments 是这批 Action 允许执行的环境（同 finance 包的理由：
// 显式列举而不是「除了生产都行」，生产权限不从测试继承，宪法 15 条）。
var allEnvironments = []string{"development", "staging", "production"}

// humanOnly：登记簿写操作只允许人类身份——这批 Action 决定「这台服务器
// 归哪个供应商、月付多少、什么时候到期」，不该让任何机器身份有能力改这些
// （ADR-009 红线：AI 不拥有生产后门）。
var humanOnly = []principal.Type{principal.TypeHuman}

var (
	assetStatusEnum  = []string{string(AssetActive), string(AssetRetired), string(AssetPlanned)}
	billingCycleEnum = []string{string(BillingMonthly), string(BillingQuarterly), string(BillingYearly)}
	certSourceEnum   = []string{string(CertACME), string(CertManaged), string(CertManual)}
	serviceKindEnum  = []string{string(ServiceContainer), string(ServiceSystemd), string(ServiceProcess)}
)

// RegisterActions 把服务器登记簿的写操作注册为 Action（ADR-003：写操作唯一入口）。
func RegisterActions(reg *action.Registry, store *Store) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{assetSetDef(), assetSetHandler(store)},
		{assetRetireDef(), assetRetireHandler(store)},
		{supplierSetDef(), supplierSetHandler(store)},
		{domainSetDef(), domainSetHandler(store)},
		{serviceNoteSetDef(), serviceNoteSetHandler(store)},
		{serviceNoteRemoveDef(), serviceNoteRemoveHandler(store)},
	}
	for _, d := range defs {
		if err := reg.Register(d.def, d.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", d.def.ID, err)
		}
	}
	return nil
}

func requireStore(store *Store) error {
	if store == nil {
		return fmt.Errorf("server store 未绑定：本注册表实例仅用于声明登记")
	}
	return nil
}

func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

// requireSameEnvironment 是跨环境闸门（宪法 15 条）：内核只校验「这个 Action
// 允许在你的环境执行」，它不认识资源——一个 staging 身份完全可能拿着生产
// 资产的 UUID 打过来，这道判定必须在读到资源之后。
func requireSameEnvironment(p principal.Principal, resourceEnv string) error {
	if resourceEnv != p.Environment {
		return action.NewError(action.CodePermissionDenied,
			"不允许跨环境操作服务器登记簿：调用者身份属于 "+p.Environment, nil)
	}
	return nil
}

func defaultIfBlank(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// domainError 把领域错误映射成 Action 错误码（同 finance.domainError 的理由：
// 不做这层映射的话，「hostname 不能为空」会以 INTERNAL 返回，
// 调用方看到的是「服务器内部错误」而不是缺了哪个参数）。
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrMissingField),
		errors.Is(err, ErrInvalidFormat),
		errors.Is(err, ErrInconsistent),
		errors.Is(err, money.ErrFormat),
		errors.Is(err, money.ErrUnknownCurrency):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	default:
		return err
	}
}

// minorAmountPattern 只接受非负纯整数——与 finance 包同一条纪律
// （见 internal/platform/finance/subscription_actions.go 的
// minorAmountPattern）：静默把 "99.90" 当成 9990 个最小单位，
// 会让金额差出一个数量级且不报错。
var minorAmountPattern = regexp.MustCompile(`^[0-9]{1,19}$`)

// optionalMinorParam 解析可选的整数最小单位金额；缺省或空白返回 nil
// （= 未登记，与 Asset.MonthlyCostMinorUnits 的指针语义一致——0 是「免费」
// 的合法取值，不能用它兼职表示「没填」）。
func optionalMinorParam(params map[string]any, name string) (*int64, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return nil, nil
	}
	if !minorAmountPattern.MatchString(raw) {
		return nil, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 必须是**纯整数**的最小单位金额（按币种自身的最小单位，"+
				"如 USD/CNY 的分：¥99.90 写作 \"9990\"）：带小数点的写法会被当成"+
				"最小单位数值本身，金额会差一到两个数量级", name, raw), nil)
	}
	v, err := money.ParseMinorUnits(raw, 0)
	if err != nil {
		return nil, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 解析失败: %v", name, raw, err), err)
	}
	return &v, nil
}

// optionalIntParam 取一个可选整数参数；字段缺省返回 nil
// （区分「没填」与「填了 0」——vCPU/内存/磁盘/端口都不该把 0 当成合法配置，
// 但「没登记」与「登记成 0」在语义上仍是两件事，统一用指针更诚实）。
func optionalIntParam(params map[string]any, name string) *int {
	if _, present := params[name]; !present {
		return nil
	}
	v := action.IntParam(params, name)
	return &v
}

// optionalUUIDParam 解析可选的 UUID 参数；空白返回 uuid.Nil。
func optionalUUIDParam(params map[string]any, name string) (uuid.UUID, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, action.NewError(action.CodeInvalidParams, name+" 不是合法 UUID", err)
	}
	return id, nil
}

// dateLayout 是登记簿收 / 显示日期的唯一形态：自然日，不带时区
// （宪法 14 条：到期日是业务日期不是时间点）。
const dateLayout = "2006-01-02"

// optionalDateParam 解析可选的 YYYY-MM-DD 日期；空白返回零值（= 未登记）。
func optionalDateParam(params map[string]any, name string) (time.Time, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		return time.Time{}, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 必须形如 2006-01-02", name, raw), err)
	}
	return t, nil
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dateLayout)
}

// --- server.asset.set（L1）---------------------------------------------

// assetSetDef 声明登记 / 修改服务器资产的动作。
//
// L1：修改低风险平台配置（ADR-003 风险等级表）。它不触碰任何第三方系统、
// 不改变任何运行中的服务，写错了改回即可——这正是「服务器只做记录」的
// 直接推论：登记簿本身没有任何执行副作用。
//
// 不含 environment 参数：环境取自调用者身份，不由参数自称（宪法 15 条）。
func assetSetDef() action.Definition {
	return action.Definition{
		ID:         ActionAssetSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			// asset_id 留空 = 新登记，填了 = 改这一条——与
			// finance.upstream_account.set 同一条区分新建/修改的做法。
			{Name: "asset_id", Type: action.FieldString},
			{Name: "hostname", Type: action.FieldString, Required: true},
			{Name: "ip_addresses", Type: action.FieldStringSlice},
			{Name: "datacenter", Type: action.FieldString},
			{Name: "supplier_id", Type: action.FieldString},
			{Name: "vcpu", Type: action.FieldInt},
			{Name: "memory_gb", Type: action.FieldInt},
			{Name: "disk_gb", Type: action.FieldInt},
			{Name: "purpose", Type: action.FieldString},
			{Name: "status", Type: action.FieldString, Enum: assetStatusEnum},
			// 定点整数字符串，不是数字字段——JSON 数字一路解成 float64，
			// 金额绝不能走这条路径（宪法 13 条）。
			{Name: "monthly_cost_minor_units", Type: action.FieldString},
			{Name: "currency", Type: action.FieldString},
			{Name: "billing_cycle", Type: action.FieldString, Enum: billingCycleEnum},
			{Name: "expires_at", Type: action.FieldString},
			{Name: "notes", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func assetSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		status, err := ParseAssetStatus(defaultIfBlank(action.StringParam(params, "status"), string(AssetActive)))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		supplierID, err := optionalUUIDParam(params, "supplier_id")
		if err != nil {
			return nil, err
		}
		cost, err := optionalMinorParam(params, "monthly_cost_minor_units")
		if err != nil {
			return nil, err
		}
		expiresAt, err := optionalDateParam(params, "expires_at")
		if err != nil {
			return nil, err
		}

		desired := Asset{
			Hostname:              strings.TrimSpace(action.StringParam(params, "hostname")),
			IPAddresses:           action.StringSliceParam(params, "ip_addresses"),
			Datacenter:            strings.TrimSpace(action.StringParam(params, "datacenter")),
			SupplierID:            supplierID,
			VCPU:                  optionalIntParam(params, "vcpu"),
			MemoryGB:              optionalIntParam(params, "memory_gb"),
			DiskGB:                optionalIntParam(params, "disk_gb"),
			Purpose:               strings.TrimSpace(action.StringParam(params, "purpose")),
			Status:                status,
			MonthlyCostMinorUnits: cost,
			Currency:              strings.ToUpper(strings.TrimSpace(action.StringParam(params, "currency"))),
			BillingCycle:          BillingCycle(strings.TrimSpace(action.StringParam(params, "billing_cycle"))),
			ExpiresAt:             expiresAt,
			Notes:                 strings.TrimSpace(action.StringParam(params, "notes")),
			Environment:           p.Environment,
		}

		idText := strings.TrimSpace(action.StringParam(params, "asset_id"))
		if idText == "" {
			created, err := store.CreateAsset(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			action.RecordResource(ctx, resourceAsset, created.ID.String())
			action.RecordAfter(ctx, assetSummary(created))
			return created, nil
		}

		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "asset_id 不是合法 UUID", err)
		}
		before, err := store.GetAsset(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}

		desired.ID = id
		action.RecordResource(ctx, resourceAsset, id.String())
		action.RecordBefore(ctx, assetSummary(before))

		updated, err := store.UpdateAsset(ctx, desired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, assetSummary(updated))
		return updated, nil
	}
}

// assetSummary 是进审计链的资产摘要；未配置的字段不写键，同
// finance.accountSummary 的理由——「没有」与「值是空字符串/0」在审计上
// 是两件不同的事。
func assetSummary(a Asset) map[string]any {
	m := map[string]any{
		"hostname":    a.Hostname,
		"status":      string(a.Status),
		"environment": a.Environment,
	}
	if len(a.IPAddresses) > 0 {
		m["ip_addresses"] = a.IPAddresses
	}
	if a.Datacenter != "" {
		m["datacenter"] = a.Datacenter
	}
	if a.SupplierID != uuid.Nil {
		m["supplier_id"] = a.SupplierID.String()
	}
	if a.VCPU != nil {
		m["vcpu"] = *a.VCPU
	}
	if a.MemoryGB != nil {
		m["memory_gb"] = *a.MemoryGB
	}
	if a.DiskGB != nil {
		m["disk_gb"] = *a.DiskGB
	}
	if a.Purpose != "" {
		m["purpose"] = a.Purpose
	}
	if a.MonthlyCostMinorUnits != nil {
		m["monthly_cost_minor_units"] = *a.MonthlyCostMinorUnits
		m["currency"] = a.Currency
	}
	if a.BillingCycle != "" {
		m["billing_cycle"] = string(a.BillingCycle)
	}
	if !a.ExpiresAt.IsZero() {
		m["expires_at"] = formatDate(a.ExpiresAt)
	}
	if a.Notes != "" {
		m["notes"] = a.Notes
	}
	return m
}

// --- server.asset.retire（L1）------------------------------------------

// assetRetireDef 声明单独把资产标记为已退役的动作。
//
// 与 assetSetDef 分开，同 finance.rechargeRatioSetDef 分开的理由：这是一个
// **能被单独审计检索**的状态迁移（「这台服务器什么时候退役、谁操作的」不该
// 靠比对一次整行更新的 before/after 才答得出），也让列表页能提供一个不必
// 打开完整编辑表单的一键退役按钮。
//
// reason 必填：退役会让这台资产从「使用中」台数与月度成本合计里消失，
// 事后复盘的第一个问题就是「当时为什么退役」。
func assetRetireDef() action.Definition {
	return action.Definition{
		ID:         ActionAssetRetire,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "asset_id", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func assetRetireHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(strings.TrimSpace(action.StringParam(params, "asset_id")))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "asset_id 不是合法 UUID", err)
		}
		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}

		before, err := store.GetAsset(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}

		action.RecordResource(ctx, resourceAsset, id.String())
		action.RecordBefore(ctx, assetSummary(before))
		action.RecordReason(ctx, reason)

		after, err := store.SetAssetStatus(ctx, id, AssetRetired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, assetSummary(after))
		return after, nil
	}
}

// --- server.supplier.set（L1）-------------------------------------------

func supplierSetDef() action.Definition {
	return action.Definition{
		ID:         ActionSupplierSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "supplier_id", Type: action.FieldString},
			{Name: "name", Type: action.FieldString, Required: true},
			{Name: "website", Type: action.FieldString},
			{Name: "console_url", Type: action.FieldString},
			{Name: "contact_name", Type: action.FieldString},
			{Name: "contact_info", Type: action.FieldString},
			{Name: "notes", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func supplierSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		desired := Supplier{
			Name:        strings.TrimSpace(action.StringParam(params, "name")),
			Website:     strings.TrimSpace(action.StringParam(params, "website")),
			ConsoleURL:  strings.TrimSpace(action.StringParam(params, "console_url")),
			ContactName: strings.TrimSpace(action.StringParam(params, "contact_name")),
			ContactInfo: strings.TrimSpace(action.StringParam(params, "contact_info")),
			Notes:       strings.TrimSpace(action.StringParam(params, "notes")),
			Environment: p.Environment,
		}

		idText := strings.TrimSpace(action.StringParam(params, "supplier_id"))
		if idText == "" {
			created, err := store.CreateSupplier(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			action.RecordResource(ctx, resourceSupplier, created.ID.String())
			action.RecordAfter(ctx, supplierSummary(created))
			return created, nil
		}

		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "supplier_id 不是合法 UUID", err)
		}
		before, err := store.GetSupplier(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}

		desired.ID = id
		action.RecordResource(ctx, resourceSupplier, id.String())
		action.RecordBefore(ctx, supplierSummary(before))

		updated, err := store.UpdateSupplier(ctx, desired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, supplierSummary(updated))
		return updated, nil
	}
}

func supplierSummary(s Supplier) map[string]any {
	m := map[string]any{"name": s.Name, "environment": s.Environment}
	if s.Website != "" {
		m["website"] = s.Website
	}
	if s.ConsoleURL != "" {
		m["console_url"] = s.ConsoleURL
	}
	if s.ContactName != "" {
		m["contact_name"] = s.ContactName
	}
	if s.ContactInfo != "" {
		m["contact_info"] = s.ContactInfo
	}
	if s.Notes != "" {
		m["notes"] = s.Notes
	}
	return m
}

// --- server.domain.set（L1）----------------------------------------------

func domainSetDef() action.Definition {
	return action.Definition{
		ID:         ActionDomainSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "domain_id", Type: action.FieldString},
			{Name: "domain_name", Type: action.FieldString, Required: true},
			{Name: "registrar", Type: action.FieldString},
			{Name: "dns_provider", Type: action.FieldString},
			{Name: "expires_at", Type: action.FieldString},
			{Name: "cert_source", Type: action.FieldString, Enum: certSourceEnum},
			{Name: "cert_expires_at", Type: action.FieldString},
			{Name: "bound_service_note", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func domainSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		expiresAt, err := optionalDateParam(params, "expires_at")
		if err != nil {
			return nil, err
		}
		certExpiresAt, err := optionalDateParam(params, "cert_expires_at")
		if err != nil {
			return nil, err
		}

		desired := ServerDomain{
			DomainName:       strings.TrimSpace(action.StringParam(params, "domain_name")),
			Registrar:        strings.TrimSpace(action.StringParam(params, "registrar")),
			DNSProvider:      strings.TrimSpace(action.StringParam(params, "dns_provider")),
			ExpiresAt:        expiresAt,
			CertSource:       CertSource(strings.TrimSpace(action.StringParam(params, "cert_source"))),
			CertExpiresAt:    certExpiresAt,
			BoundServiceNote: strings.TrimSpace(action.StringParam(params, "bound_service_note")),
			Environment:      p.Environment,
		}

		idText := strings.TrimSpace(action.StringParam(params, "domain_id"))
		if idText == "" {
			created, err := store.CreateServerDomain(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			action.RecordResource(ctx, resourceDomain, created.ID.String())
			action.RecordAfter(ctx, domainSummary(created))
			return created, nil
		}

		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "domain_id 不是合法 UUID", err)
		}
		before, err := store.GetServerDomain(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireSameEnvironment(p, before.Environment); err != nil {
			return nil, err
		}

		desired.ID = id
		action.RecordResource(ctx, resourceDomain, id.String())
		action.RecordBefore(ctx, domainSummary(before))

		updated, err := store.UpdateServerDomain(ctx, desired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, domainSummary(updated))
		return updated, nil
	}
}

func domainSummary(d ServerDomain) map[string]any {
	m := map[string]any{"domain_name": d.DomainName, "environment": d.Environment}
	if d.Registrar != "" {
		m["registrar"] = d.Registrar
	}
	if d.DNSProvider != "" {
		m["dns_provider"] = d.DNSProvider
	}
	if !d.ExpiresAt.IsZero() {
		m["expires_at"] = formatDate(d.ExpiresAt)
	}
	if d.CertSource != "" {
		m["cert_source"] = string(d.CertSource)
	}
	if !d.CertExpiresAt.IsZero() {
		m["cert_expires_at"] = formatDate(d.CertExpiresAt)
	}
	if d.BoundServiceNote != "" {
		m["bound_service_note"] = d.BoundServiceNote
	}
	return m
}

// --- server.service_note.set（L1）----------------------------------------

// serviceNoteSetDef 声明登记 / 修改服务备注的动作。
//
// server_id 在 Schema 层不标 Required：它只在**新登记**时需要（Handler 里
// 显式校验），修改时这张表不允许换归属（同迁移里 UpdateServerServiceNote
// 的注释：换服务器等于把审计事件变成另一件事，要改归属只能删了重登）。
func serviceNoteSetDef() action.Definition {
	return action.Definition{
		ID:         ActionServiceNoteSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service_note_id", Type: action.FieldString},
			{Name: "server_id", Type: action.FieldString},
			{Name: "service_name", Type: action.FieldString, Required: true},
			{Name: "service_kind", Type: action.FieldString, Required: true, Enum: serviceKindEnum},
			{Name: "port", Type: action.FieldInt},
			{Name: "notes", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func serviceNoteSetHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}

		serviceName := strings.TrimSpace(action.StringParam(params, "service_name"))
		kind, err := ParseServiceKind(action.StringParam(params, "service_kind"))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		port := optionalIntParam(params, "port")
		notes := strings.TrimSpace(action.StringParam(params, "notes"))

		idText := strings.TrimSpace(action.StringParam(params, "service_note_id"))
		if idText == "" {
			serverIDText := strings.TrimSpace(action.StringParam(params, "server_id"))
			if serverIDText == "" {
				return nil, action.NewError(action.CodeInvalidParams, "新登记服务备注必须带 server_id", nil)
			}
			asset, err := resolveOwningAsset(ctx, store, serverIDText)
			if err != nil {
				return nil, err
			}
			created, err := store.CreateServiceNote(ctx, ServiceNote{
				ServerID:    asset.ID,
				ServiceName: serviceName,
				ServiceKind: kind,
				Port:        port,
				Notes:       notes,
			})
			if err != nil {
				return nil, domainError(err)
			}
			action.RecordResource(ctx, resourceServiceNote, created.ID.String())
			action.RecordAfter(ctx, serviceNoteSummary(created))
			return created, nil
		}

		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "service_note_id 不是合法 UUID", err)
		}
		before, err := store.GetServiceNote(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireOwningAssetSameEnvironment(ctx, store, before.ServerID); err != nil {
			return nil, err
		}

		updated, err := store.UpdateServiceNote(ctx, ServiceNote{
			ID:          id,
			ServerID:    before.ServerID,
			ServiceName: serviceName,
			ServiceKind: kind,
			Port:        port,
			Notes:       notes,
		})
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceServiceNote, id.String())
		action.RecordBefore(ctx, serviceNoteSummary(before))
		action.RecordAfter(ctx, serviceNoteSummary(updated))
		return updated, nil
	}
}

func serviceNoteSummary(n ServiceNote) map[string]any {
	m := map[string]any{
		"server_id":    n.ServerID.String(),
		"service_name": n.ServiceName,
		"service_kind": string(n.ServiceKind),
	}
	if n.Port != nil {
		m["port"] = *n.Port
	}
	if n.Notes != "" {
		m["notes"] = n.Notes
	}
	return m
}

// --- server.service_note.remove（L1）-------------------------------------

// serviceNoteRemoveDef 声明删除服务备注的动作。
//
// 必须存在：一条写错的服务登记会一直显示在资产详情里，没有删除路径时唯一的
// 补救是直接改库——那正是宪法 2 条要挡的绕过（同 finance.tokenMapRemoveDef）。
func serviceNoteRemoveDef() action.Definition {
	return action.Definition{
		ID:         ActionServiceNoteRemove,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service_note_id", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func serviceNoteRemoveHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		id, err := uuid.Parse(strings.TrimSpace(action.StringParam(params, "service_note_id")))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "service_note_id 不是合法 UUID", err)
		}
		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}

		before, err := store.GetServiceNote(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		if err := requireOwningAssetSameEnvironment(ctx, store, before.ServerID); err != nil {
			return nil, err
		}

		action.RecordResource(ctx, resourceServiceNote, id.String())
		action.RecordBefore(ctx, serviceNoteSummary(before))
		action.RecordReason(ctx, reason)

		if err := store.DeleteServiceNote(ctx, id); err != nil {
			return nil, domainError(err)
		}
		return map[string]any{"service_note_id": id.String(), "deleted": true}, nil
	}
}

// resolveOwningAsset 解析 server_id 并落实跨环境闸门（同
// finance.resolveOwningAccount：服务备注本身没有 environment 列，环境判定
// 必须先把父行——服务器资产——读出来）。
func resolveOwningAsset(ctx context.Context, store *Store, serverIDText string) (Asset, error) {
	p, err := callerPrincipal(ctx)
	if err != nil {
		return Asset{}, err
	}
	id, err := uuid.Parse(serverIDText)
	if err != nil {
		return Asset{}, action.NewError(action.CodeInvalidParams, "server_id 不是合法 UUID", err)
	}
	asset, err := store.GetAsset(ctx, id)
	if err != nil {
		return Asset{}, domainError(err)
	}
	if err := requireSameEnvironment(p, asset.Environment); err != nil {
		return Asset{}, err
	}
	return asset, nil
}

// requireOwningAssetSameEnvironment 是 resolveOwningAsset 的变体：调用方已经
// 有一个 server_id（从已存在的服务备注读出来），只需要落实环境闸门。
func requireOwningAssetSameEnvironment(ctx context.Context, store *Store, serverID uuid.UUID) error {
	p, err := callerPrincipal(ctx)
	if err != nil {
		return err
	}
	asset, err := store.GetAsset(ctx, serverID)
	if err != nil {
		return domainError(err)
	}
	return requireSameEnvironment(p, asset.Environment)
}
