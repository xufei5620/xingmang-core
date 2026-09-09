package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/server"
)

// 服务器登记簿（XM-SERVER0）的四个只读查询处理器与它们的最小仓储接口。
//
// **权限复用 registry.ScopeRead**（"registry.read"），不新建读侧 scope：
// 能看服务清单的人本就该能看服务器登记簿——两者都是「平台管着哪些基础设施」
// 这同一类知识面，泄漏面相当（见 internal/platform/server.ScopeManage 的
// 注释，那是写侧新建的专属 scope）。写路径（登记/修改/退役/删除）不在这里
// ——它们是 L1 Action，走 POST /api/v1/actions/{id}/versions/{v}/execute，
// 权限由内核裁决。

// dateItem 把可选日期格式化成 "YYYY-MM-DD"；零值（未登记）输出空串，
// 不编一个假日期——前端据此显示「未登记」而不是一个看起来正常的到期日。
func dateItem(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func timestampItem(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// --- server_asset ------------------------------------------------------

// ServerAssetLister 是服务器资产的只读查询能力（*server.Store 满足）。
type ServerAssetLister interface {
	ListAssetsByEnvironment(ctx context.Context, environment string) ([]server.Asset, error)
}

type serverAssetItem struct {
	ID          string   `json:"id"`
	Hostname    string   `json:"hostname"`
	IPAddresses []string `json:"ip_addresses"`
	Datacenter  string   `json:"datacenter"`
	// SupplierID 空串 = 未登记供应商。
	SupplierID string `json:"supplier_id"`
	VCPU       *int   `json:"vcpu"`
	MemoryGB   *int   `json:"memory_gb"`
	DiskGB     *int   `json:"disk_gb"`
	Purpose    string `json:"purpose"`
	Status     string `json:"status"`
	// MonthlyCostMinorUnits 是整数最小单位字符串（宪法 13 条：金额禁止
	// Float）；nil 序列化为 JSON null = 未登记月付成本，**不是 0**。
	// 用字符串而不是数字：超过 2^53 的金额在前端会被 JSON.parse 静默截断。
	MonthlyCostMinorUnits *string `json:"monthly_cost_minor_units"`
	Currency              string  `json:"currency"`
	BillingCycle          string  `json:"billing_cycle"`
	// ExpiresAt 空串 = 未登记到期日。
	ExpiresAt   string `json:"expires_at"`
	Notes       string `json:"notes"`
	Environment string `json:"environment"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func assetToItem(a server.Asset) serverAssetItem {
	item := serverAssetItem{
		ID:           a.ID.String(),
		Hostname:     a.Hostname,
		IPAddresses:  a.IPAddresses,
		Datacenter:   a.Datacenter,
		VCPU:         a.VCPU,
		MemoryGB:     a.MemoryGB,
		DiskGB:       a.DiskGB,
		Purpose:      a.Purpose,
		Status:       string(a.Status),
		Currency:     a.Currency,
		BillingCycle: string(a.BillingCycle),
		ExpiresAt:    dateItem(a.ExpiresAt),
		Notes:        a.Notes,
		Environment:  a.Environment,
		CreatedAt:    timestampItem(a.CreatedAt),
		UpdatedAt:    timestampItem(a.UpdatedAt),
	}
	if a.IPAddresses == nil {
		item.IPAddresses = []string{}
	}
	if a.SupplierID != uuid.Nil {
		id := a.SupplierID.String()
		item.SupplierID = id
	}
	if a.MonthlyCostMinorUnits != nil {
		s := strconv.FormatInt(*a.MonthlyCostMinorUnits, 10)
		item.MonthlyCostMinorUnits = &s
	}
	return item
}

// ListServerAssetsHandler 列出某环境下的全部服务器资产。
func ListServerAssetsHandler(store ServerAssetLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		assets, err := store.ListAssetsByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]serverAssetItem, 0, len(assets))
		for _, a := range assets {
			out = append(out, assetToItem(a))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// --- server_supplier -----------------------------------------------------

// ServerSupplierLister 是服务器供应商的只读查询能力（*server.Store 满足）。
type ServerSupplierLister interface {
	ListSuppliersByEnvironment(ctx context.Context, environment string) ([]server.Supplier, error)
}

type serverSupplierItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Website     string `json:"website"`
	ConsoleURL  string `json:"console_url"`
	ContactName string `json:"contact_name"`
	ContactInfo string `json:"contact_info"`
	Notes       string `json:"notes"`
	Environment string `json:"environment"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func supplierToItem(s server.Supplier) serverSupplierItem {
	return serverSupplierItem{
		ID:          s.ID.String(),
		Name:        s.Name,
		Website:     s.Website,
		ConsoleURL:  s.ConsoleURL,
		ContactName: s.ContactName,
		ContactInfo: s.ContactInfo,
		Notes:       s.Notes,
		Environment: s.Environment,
		CreatedAt:   timestampItem(s.CreatedAt),
		UpdatedAt:   timestampItem(s.UpdatedAt),
	}
}

// ListServerSuppliersHandler 列出某环境下的全部服务器供应商。
func ListServerSuppliersHandler(store ServerSupplierLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		suppliers, err := store.ListSuppliersByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]serverSupplierItem, 0, len(suppliers))
		for _, s := range suppliers {
			out = append(out, supplierToItem(s))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// --- server_domain ---------------------------------------------------------

// ServerDomainLister 是域名登记的只读查询能力（*server.Store 满足）。
type ServerDomainLister interface {
	ListServerDomainsByEnvironment(ctx context.Context, environment string) ([]server.ServerDomain, error)
}

type serverDomainItem struct {
	ID               string `json:"id"`
	DomainName       string `json:"domain_name"`
	Registrar        string `json:"registrar"`
	DNSProvider      string `json:"dns_provider"`
	ExpiresAt        string `json:"expires_at"`
	CertSource       string `json:"cert_source"`
	CertExpiresAt    string `json:"cert_expires_at"`
	BoundServiceNote string `json:"bound_service_note"`
	Environment      string `json:"environment"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func serverDomainToItem(d server.ServerDomain) serverDomainItem {
	return serverDomainItem{
		ID:               d.ID.String(),
		DomainName:       d.DomainName,
		Registrar:        d.Registrar,
		DNSProvider:      d.DNSProvider,
		ExpiresAt:        dateItem(d.ExpiresAt),
		CertSource:       string(d.CertSource),
		CertExpiresAt:    dateItem(d.CertExpiresAt),
		BoundServiceNote: d.BoundServiceNote,
		Environment:      d.Environment,
		CreatedAt:        timestampItem(d.CreatedAt),
		UpdatedAt:        timestampItem(d.UpdatedAt),
	}
}

// ListServerDomainsHandler 列出某环境下的全部域名登记。
func ListServerDomainsHandler(store ServerDomainLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		domains, err := store.ListServerDomainsByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]serverDomainItem, 0, len(domains))
		for _, d := range domains {
			out = append(out, serverDomainToItem(d))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// --- server_service_note ---------------------------------------------------

// ServerServiceNoteLister 是服务/容器备注的只读查询能力（*server.Store 满足）。
type ServerServiceNoteLister interface {
	ListServiceNotesByEnvironment(ctx context.Context, environment string) ([]server.ServiceNote, error)
}

type serverServiceNoteItem struct {
	ID          string `json:"id"`
	ServerID    string `json:"server_id"`
	ServiceName string `json:"service_name"`
	ServiceKind string `json:"service_kind"`
	Port        *int   `json:"port"`
	Notes       string `json:"notes"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func serviceNoteToItem(n server.ServiceNote) serverServiceNoteItem {
	return serverServiceNoteItem{
		ID:          n.ID.String(),
		ServerID:    n.ServerID.String(),
		ServiceName: n.ServiceName,
		ServiceKind: string(n.ServiceKind),
		Port:        n.Port,
		Notes:       n.Notes,
		CreatedAt:   timestampItem(n.CreatedAt),
		UpdatedAt:   timestampItem(n.UpdatedAt),
	}
}

// ListServerServiceNotesHandler 列出某环境下的全部服务/容器备注。
func ListServerServiceNotesHandler(store ServerServiceNoteLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		notes, err := store.ListServiceNotesByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]serverServiceNoteItem, 0, len(notes))
		for _, n := range notes {
			out = append(out, serviceNoteToItem(n))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
