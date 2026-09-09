package server

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// AssetStatus 是服务器资产的登记状态（拍板：登记簿口径，不是心跳判定——
// 没有 Agent 上报，「在线/离线」这类实时态本包不建模）。
type AssetStatus string

const (
	AssetActive  AssetStatus = "active"
	AssetRetired AssetStatus = "retired"
	AssetPlanned AssetStatus = "planned"
)

// BillingCycle 是月付成本的计费周期；空串 = 未登记。
type BillingCycle string

const (
	BillingMonthly   BillingCycle = "monthly"
	BillingQuarterly BillingCycle = "quarterly"
	BillingYearly    BillingCycle = "yearly"
)

// CertSource 是域名证书的来源；空串 = 未登记。
type CertSource string

const (
	CertACME    CertSource = "acme"
	CertManaged CertSource = "managed"
	CertManual  CertSource = "manual"
)

// ServiceKind 是手工登记的服务类型。
type ServiceKind string

const (
	ServiceContainer ServiceKind = "container"
	ServiceSystemd   ServiceKind = "systemd"
	ServiceProcess   ServiceKind = "process"
)

var (
	// ErrNotFound：登记簿里没有这条记录。
	ErrNotFound = errors.New("server: not found")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("server: missing required field")
	// ErrInvalidFormat：字段格式非法。
	ErrInvalidFormat = errors.New("server: invalid format")
	// ErrInconsistent：字段之间自相矛盾（如金额与币种只填了一个）。
	ErrInconsistent = errors.New("server: inconsistent fields")
)

// urlPattern 与 finance.validateBaseURL 同一条规则：https 且不含用户信息段——
// 只读通道不接受明文凭证藏在 URL 里（ADR-018 闸 1）。
var urlPattern = regexp.MustCompile(`^https://[^@\s]+$`)

// currencyPattern 是 ISO 4217 三位大写字母码，与 finance 包同一条规则。
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Asset 是服务器资产登记簿的一行（ADMIN-IA §2.1「服务器资产」）。
type Asset struct {
	ID uuid.UUID

	Hostname string
	// IPAddresses 允许一台服务器登记多个地址（公网/内网/多网卡）。
	IPAddresses []string

	Datacenter string
	// SupplierID 关联 core.server_supplier；uuid.Nil = 未登记供应商。
	SupplierID uuid.UUID

	// VCPU / MemoryGB / DiskGB 是采购规格，nil = 未登记。
	VCPU     *int
	MemoryGB *int
	DiskGB   *int

	Purpose string
	Status  AssetStatus

	// MonthlyCostMinorUnits 是整数最小单位（宪法 13 条），标度取决于 Currency
	// （money.CurrencyScale）。nil = 未登记月付成本——**不是 0**：0 是「免费/
	// 试用」的合法取值，用指针而不是 0 值把「没填」和「填了 0」分开。
	MonthlyCostMinorUnits *int64
	// Currency 与 MonthlyCostMinorUnits 成对出现：要么都空要么都填。
	Currency     string
	BillingCycle BillingCycle

	// ExpiresAt 是续费/合同到期日，零值 = 未登记。
	ExpiresAt time.Time

	Notes       string
	Environment string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Supplier 是服务器供应商与购买账号登记簿的一行（ADMIN-IA §2.1「供应商与采购」）。
//
// 只存联系方式，不存密码：购买账号密码走 CredentialRef 才是安全的做法
// （宪法 7 条），但本片尚未把凭据字段接进这张表——供应商门户账号今天还是
// 一句备注，见 handoff 的 follow_ups。
type Supplier struct {
	ID uuid.UUID

	Name        string
	Website     string
	ConsoleURL  string
	ContactName string
	ContactInfo string
	Notes       string

	Environment string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ServerDomain 是域名与证书登记簿的一行（ADMIN-IA §2.1「域名与证书」）。
//
// 只做记录：证书健康度不探测，来源与到期日全部手工登记。命名为
// ServerDomain 而不是 Domain：避免与「哪个 environment」这类常见领域词混淆，
// 也对齐生成类型 gen.CoreServerDomain。
type ServerDomain struct {
	ID uuid.UUID

	DomainName    string
	Registrar     string
	DNSProvider   string
	ExpiresAt     time.Time
	CertSource    CertSource
	CertExpiresAt time.Time

	// BoundServiceNote 只是一句备注（哪个服务在用这个域名），不是外键——
	// 服务与容器目前也是手工登记表，两边都没有稳定到能互相引用的主键。
	BoundServiceNote string

	Environment string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ServiceNote 是服务器上手工登记的一条服务/容器（ADMIN-IA §2.1「服务与容器」）。
//
// **不扫描 Docker、不探测端口**：这只是运营手写的「这台服务器上有什么」。
// 挂在 ServerID 下，不重复存 Environment 列——同 finance.TokenMapping 挂在
// UpstreamAccountID 下的理由，环境判定必须先把父行读出来。
type ServiceNote struct {
	ID       uuid.UUID
	ServerID uuid.UUID

	ServiceName string
	ServiceKind ServiceKind
	Port        *int
	Notes       string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ParseAssetStatus 解析资产状态；只接受精确小写值。
func ParseAssetStatus(s string) (AssetStatus, error) {
	switch AssetStatus(s) {
	case AssetActive, AssetRetired, AssetPlanned:
		return AssetStatus(s), nil
	default:
		return "", fmt.Errorf("status %q 须为 active / retired / planned: %w", s, ErrInvalidFormat)
	}
}

// ParseServiceKind 解析服务类型；只接受精确小写值。
func ParseServiceKind(s string) (ServiceKind, error) {
	switch ServiceKind(s) {
	case ServiceContainer, ServiceSystemd, ServiceProcess:
		return ServiceKind(s), nil
	default:
		return "", fmt.Errorf("service_kind %q 须为 container / systemd / process: %w", s, ErrInvalidFormat)
	}
}

// Validate 校验服务器资产的领域不变量，与 000023 迁移的 CHECK 是同一套规则的
// 两处实现（同 finance.UpstreamAccount.Validate 的理由：领域层给出说人话的
// 错误，库层保证任何写入路径都绕不开）。
func (a Asset) Validate() error {
	if strings.TrimSpace(a.Hostname) == "" {
		return fmt.Errorf("hostname: %w", ErrMissingField)
	}
	if _, err := ParseAssetStatus(string(a.Status)); err != nil {
		return err
	}
	if strings.TrimSpace(a.Environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	for _, ip := range a.IPAddresses {
		if net.ParseIP(strings.TrimSpace(ip)) == nil {
			return fmt.Errorf("ip_addresses 里的 %q 不是合法 IP: %w", ip, ErrInvalidFormat)
		}
	}
	if a.VCPU != nil && *a.VCPU <= 0 {
		return fmt.Errorf("vcpu=%d 必须为正: %w", *a.VCPU, ErrInvalidFormat)
	}
	if a.MemoryGB != nil && *a.MemoryGB <= 0 {
		return fmt.Errorf("memory_gb=%d 必须为正: %w", *a.MemoryGB, ErrInvalidFormat)
	}
	if a.DiskGB != nil && *a.DiskGB <= 0 {
		return fmt.Errorf("disk_gb=%d 必须为正: %w", *a.DiskGB, ErrInvalidFormat)
	}
	// 金额与币种成对出现：不允许「有金额没币种」（没法展示）或
	// 「有币种没金额」（币种代表着什么无法解释）。
	if (a.MonthlyCostMinorUnits != nil) != (a.Currency != "") {
		return fmt.Errorf("monthly_cost_minor_units 与 currency 必须同时填写或同时留空: %w", ErrInconsistent)
	}
	if a.Currency != "" {
		if !currencyPattern.MatchString(a.Currency) {
			return fmt.Errorf("currency %q 须为三位大写 ISO 4217 码: %w", a.Currency, ErrInvalidFormat)
		}
		// 币种必须是**已登记**的：最小单位小数位猜错的那 100 倍不会有任何症状
		// （同 finance.UpstreamAccount.Validate 的理由）。
		if _, err := money.CurrencyScale(a.Currency); err != nil {
			return fmt.Errorf("currency %q 未登记最小单位小数位（%v）: %w", a.Currency, err, ErrInvalidFormat)
		}
	}
	if a.MonthlyCostMinorUnits != nil && *a.MonthlyCostMinorUnits < 0 {
		return fmt.Errorf("monthly_cost_minor_units=%d 不能为负: %w", *a.MonthlyCostMinorUnits, ErrInvalidFormat)
	}
	if a.BillingCycle != "" {
		switch a.BillingCycle {
		case BillingMonthly, BillingQuarterly, BillingYearly:
		default:
			return fmt.Errorf("billing_cycle %q 须为 monthly / quarterly / yearly: %w", a.BillingCycle, ErrInvalidFormat)
		}
	}
	return nil
}

// Validate 校验供应商登记的领域不变量。
func (s Supplier) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name: %w", ErrMissingField)
	}
	if strings.TrimSpace(s.Environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	if s.Website != "" && !urlPattern.MatchString(s.Website) {
		return fmt.Errorf("website 必须是 https 且不含 user:pass@ 段: %w", ErrInvalidFormat)
	}
	if s.ConsoleURL != "" && !urlPattern.MatchString(s.ConsoleURL) {
		return fmt.Errorf("console_url 必须是 https 且不含 user:pass@ 段: %w", ErrInvalidFormat)
	}
	return nil
}

// Validate 校验域名登记的领域不变量。
func (d ServerDomain) Validate() error {
	if strings.TrimSpace(d.DomainName) == "" {
		return fmt.Errorf("domain_name: %w", ErrMissingField)
	}
	if strings.TrimSpace(d.Environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	if d.CertSource != "" {
		switch d.CertSource {
		case CertACME, CertManaged, CertManual:
		default:
			return fmt.Errorf("cert_source %q 须为 acme / managed / manual: %w", d.CertSource, ErrInvalidFormat)
		}
	}
	return nil
}

// Validate 校验服务登记的领域不变量。
func (n ServiceNote) Validate() error {
	if n.ServerID == uuid.Nil {
		return fmt.Errorf("server_id: %w", ErrMissingField)
	}
	if strings.TrimSpace(n.ServiceName) == "" {
		return fmt.Errorf("service_name: %w", ErrMissingField)
	}
	if _, err := ParseServiceKind(string(n.ServiceKind)); err != nil {
		return err
	}
	if n.Port != nil && (*n.Port <= 0 || *n.Port > 65535) {
		return fmt.Errorf("port=%d 必须在 1~65535 之间: %w", *n.Port, ErrInvalidFormat)
	}
	return nil
}

// ExpiringWithin 报告一个到期日是否在从 now 起的 within 天内到期（含今天，
// 已过期同样算「需要关注」）。零值到期日（未登记）永远返回 false——
// 「没登记到期日」和「登记了但已经很紧急」不是一回事，不该混进同一个警示。
func ExpiringWithin(expiresAt time.Time, now time.Time, within time.Duration) bool {
	if expiresAt.IsZero() {
		return false
	}
	return !expiresAt.After(now.Add(within))
}
