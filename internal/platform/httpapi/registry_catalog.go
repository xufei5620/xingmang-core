package httpapi

import (
	"context"
	"net/http"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// 资源目录里「连接器」与「连接」两张表的只读查询（XM-READONLY-QUERIES）。
//
// 这两张表的后端一直是全的：表在迁移 000001，仓储在 registry/store.go，
// 三个写 Action（connector.create / connection.create / connection.set_status）
// 在 registry/actions.go 里注册着。缺的只有读侧——于是注册表页面上写着
// 「两张表尚未建」，而库里可能已经有行了。
//
// **权限复用 registry.ScopeRead**，与 /services 同一个：三张表是同一份
// 「平台管着哪些系统、用哪种实现连、连成了几条」的知识面，能看服务清单的人
// 本就该看得见另外两张（同 /servers/* 复用它的理由）。不新建读侧 scope——
// 新增一个 scope 就要同步 STAFF_ROLE_CATALOG 与角色表，换不来任何实际隔离。
//
// 写路径不在这里：connector.create 是 L2、connection.create 是 L3，
// 走 POST /api/v1/actions/{id}/versions/{v}/execute 并经审批中心裁决。

// --- connector ------------------------------------------------------------

// ConnectorLister 是连接器目录的只读查询能力（*registry.Store 满足）。
type ConnectorLister interface {
	ListConnectors(ctx context.Context) ([]registry.Connector, error)
}

type connectorItem struct {
	ID                   string   `json:"id"`
	Key                  string   `json:"key"`
	Version              string   `json:"version"`
	ContractVersion      string   `json:"contract_version"`
	ConnectionSchemaPath string   `json:"connection_schema_path"`
	TargetAllowlist      []string `json:"target_allowlist"`
	// ReadCapabilities / WriteCapabilities 分两列而不是一列加个 is_write 标记：
	// ADR-004 的约束是按集合下的（写集合非空的连接必须配 Kill Switch），
	// 合成一列之后界面上就看不出这条约束落在哪儿了。
	ReadCapabilities  []string `json:"read_capabilities"`
	WriteCapabilities []string `json:"write_capabilities"`
	// SupportedUpstreamVersions 空数组 = 没有声明兼容版本，不是「兼容所有版本」。
	SupportedUpstreamVersions []string `json:"supported_upstream_versions"`
	CompatibilityTestPath     string   `json:"compatibility_test_path"`
	CreatedAt                 string   `json:"created_at"`
	UpdatedAt                 string   `json:"updated_at"`
}

func connectorToItem(c registry.Connector) connectorItem {
	return connectorItem{
		ID:                        c.ID.String(),
		Key:                       c.Key,
		Version:                   c.Version,
		ContractVersion:           c.ContractVersion,
		ConnectionSchemaPath:      c.ConnectionSchemaPath,
		TargetAllowlist:           stringsOrEmpty(c.TargetAllowlist),
		ReadCapabilities:          capabilityStrings(c.ReadCapabilities),
		WriteCapabilities:         capabilityStrings(c.WriteCapabilities),
		SupportedUpstreamVersions: stringsOrEmpty(c.SupportedUpstreamVersions),
		CompatibilityTestPath:     c.CompatibilityTestPath,
		CreatedAt:                 timestampItem(c.CreatedAt),
		UpdatedAt:                 timestampItem(c.UpdatedAt),
	}
}

// ListConnectorsHandler 列出全部已登记的连接器类型版本。
//
// **不按环境过滤**，与 /services、/connections 都不同：core.connector 没有
// environment 列（迁移 000001）——它是「平台有哪几种连接实现」的类型目录，
// 全平台一份。这里仍然要求身份（RequirePrincipal 在路由组上），只是不拿
// 环境去筛：编一个筛不存在的隔离，比不筛更容易让人误以为已经隔离了。
func ListConnectorsHandler(store ConnectorLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		items, err := store.ListConnectors(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]connectorItem, 0, len(items))
		for _, c := range items {
			out = append(out, connectorToItem(c))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// --- connection -----------------------------------------------------------

// ConnectionLister 是连接的只读查询能力（*registry.Store 满足）。
type ConnectionLister interface {
	ListConnectionsByEnvironment(ctx context.Context, env registry.Environment) ([]registry.Connection, error)
}

type connectionItem struct {
	ID          string `json:"id"`
	ConnectorID string `json:"connector_id"`
	ServiceID   string `json:"service_id"`
	Environment string `json:"environment"`
	// CredentialRef 是**引用**不是凭据（ADR-014：secret://<scope>/<name>）。
	// 出这一列是刻意的，理由与它进审计链一样（registry/actions.go 的
	// connectionSummary）：「这条连接绑了哪个凭据」是事故复盘要问的第一个
	// 问题。值永远不经过任何端点，只有 SecretProvider 碰得到（宪法 7 条）。
	CredentialRef       string   `json:"credential_ref"`
	TargetAllowlist     []string `json:"target_allowlist"`
	GrantedCapabilities []string `json:"granted_capabilities"`
	// KillSwitch 空串 = 未配置。含写能力的连接必须配（ADR-004），领域层
	// 会拒绝登记；空串出现在这里意味着这是一条纯读连接。
	KillSwitch              string `json:"kill_switch"`
	Status                  string `json:"status"`
	DetectedUpstreamVersion string `json:"detected_upstream_version"`
	VersionFingerprint      string `json:"version_fingerprint"`
	// LastVerifiedAt 为 null 表示**从未核验过**，与「核验过但很久以前」
	// 不是一回事——前端据此显示「未核验」而不是一个看起来正常的旧时刻
	// （同 serviceItem.ObservedAt 的纪律）。
	LastVerifiedAt *string `json:"last_verified_at"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

func connectionToItem(c registry.Connection) connectionItem {
	item := connectionItem{
		ID:                      c.ID.String(),
		ConnectorID:             c.ConnectorID.String(),
		ServiceID:               c.ServiceID.String(),
		Environment:             string(c.Environment),
		CredentialRef:           c.CredentialRef,
		TargetAllowlist:         stringsOrEmpty(c.TargetAllowlist),
		GrantedCapabilities:     capabilityStrings(c.GrantedCapabilities),
		KillSwitch:              c.KillSwitch,
		Status:                  string(c.Status),
		DetectedUpstreamVersion: c.DetectedUpstreamVersion,
		VersionFingerprint:      c.VersionFingerprint,
		CreatedAt:               timestampItem(c.CreatedAt),
		UpdatedAt:               timestampItem(c.UpdatedAt),
	}
	if c.LastVerifiedAt != nil {
		ts := timestampItem(*c.LastVerifiedAt)
		item.LastVerifiedAt = &ts
	}
	return item
}

// ListConnectionsHandler 列出某环境下的全部连接。
//
// 环境范围由 resolveEnvironment 决定：不传用调用者自己的，传了必须一致
// （规格 §20.5：生产权限不继承）。连接带着 credential_ref 与已授能力，
// 跨环境读取必须是显式授予的能力而不是一个查询参数。
func ListConnectionsHandler(store ConnectionLister) http.HandlerFunc {
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
		items, err := store.ListConnectionsByEnvironment(r.Context(), env)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]connectionItem, 0, len(items))
		for _, c := range items {
			out = append(out, connectionToItem(c))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// --- 共用小工具 -------------------------------------------------------------

// capabilityStrings 把领域的 Capability 列表摊成字符串列表。
// nil 输出空数组而不是 JSON null：前端对「没有声明任何能力」要能直接
// `.length` 判空，多一个 null 分支只会多一处漏判。
func capabilityStrings(cs []registry.Capability) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, string(c))
	}
	return out
}

// stringsOrEmpty 同上，用于 []string 列。
func stringsOrEmpty(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
