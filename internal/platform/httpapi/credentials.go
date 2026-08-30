package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// CredentialQuerier 是凭据登记与连接器配置的只读能力（*credentials.Store 满足）。
//
// 写路径（登记 / 轮换 / 吊销 / 切模式）不在这里——它们是 L1 Action，
// 走 POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
type CredentialQuerier interface {
	List(ctx context.Context, environment string) ([]credentials.Metadata, error)
	ListConnectorConfigs(ctx context.Context, environment string) ([]credentials.ConnectorConfig, error)
}

// credentialItem 是一条凭据引用的对外表示。
//
// **逐字段列出，不是 Metadata 的直接序列化**（规格 §18.4）：这一条在本端点上
// 尤其要紧——将来若有人为了排查方便往 Metadata 上加一个字段，直接序列化会让它
// 一路漏到前端。这里没有、也永远不会有值本身；fingerprint 是 sha256 的前 16 位。
type credentialItem struct {
	CredentialRef string `json:"credential_ref"`
	Scope         string `json:"scope"`
	UpdatedAt     string `json:"updated_at"`
	Fingerprint   string `json:"fingerprint"`
	Version       int    `json:"version"`
	Available     bool   `json:"available"`
	Revoked       bool   `json:"revoked"`
}

// expectedCredentialItem 是「平台预期存在的引用」及其就绪状态。
type expectedCredentialItem struct {
	CredentialRef string `json:"credential_ref"`
	Platform      string `json:"platform"`
	Purpose       string `json:"purpose"`
	// Configured = 已登记 且 未吊销 且 文件可读。三者缺一都算没配好。
	Configured bool `json:"configured"`
}

// connectorConfigItem 是一份连接器配置的对外表示。credential_ref 只是引用。
type connectorConfigItem struct {
	Platform        string   `json:"platform"`
	Mode            string   `json:"mode"`
	Endpoint        string   `json:"endpoint"`
	TargetAllowlist []string `json:"target_allowlist"`
	CredentialRef   string   `json:"credential_ref"`
	Version         int      `json:"version"`
	UpdatedAt       string   `json:"updated_at"`
	UpdatedBy       string   `json:"updated_by"`
}

func toCredentialItem(m credentials.Metadata) credentialItem {
	return credentialItem{
		CredentialRef: m.Ref,
		Scope:         m.Scope,
		UpdatedAt:     m.UpdatedAt.UTC().Format(time.RFC3339),
		Fingerprint:   m.Fingerprint,
		Version:       m.Version,
		Available:     m.Available,
		Revoked:       m.Revoked(),
	}
}

func toConnectorConfigItem(c credentials.ConnectorConfig) connectorConfigItem {
	return connectorConfigItem{
		Platform:        c.Platform,
		Mode:            c.Mode,
		Endpoint:        c.Endpoint,
		TargetAllowlist: append([]string{}, c.TargetAllowlist...),
		CredentialRef:   c.CredentialRef,
		Version:         c.Version,
		UpdatedAt:       c.UpdatedAt.UTC().Format(time.RFC3339),
		UpdatedBy:       c.UpdatedBy,
	}
}

func credentialEnvironment(r *http.Request) (string, error) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		return "", action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	env, err := resolveEnvironment(r, p)
	if err != nil {
		return "", err
	}
	return string(env), nil
}

func credentialQueryFailed(err error) error {
	return action.NewError(action.CodeExecutionFailed, "凭据登记读取失败", err)
}

// ListCredentialsHandler 返回当前环境下全部已登记引用的元数据（含已吊销的）。
func ListCredentialsHandler(store CredentialQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		environment, err := credentialEnvironment(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := store.List(r.Context(), environment)
		if err != nil {
			WriteError(w, r, credentialQueryFailed(err))
			return
		}
		out := make([]credentialItem, 0, len(items))
		for _, item := range items {
			out = append(out, toCredentialItem(item))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListExpectedCredentialsHandler 把固定的预期引用清单与登记状态对上，
// 回答运营页的「还缺哪把」。
func ListExpectedCredentialsHandler(store CredentialQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		environment, err := credentialEnvironment(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := store.List(r.Context(), environment)
		if err != nil {
			WriteError(w, r, credentialQueryFailed(err))
			return
		}
		configured := make(map[string]bool, len(items))
		for _, item := range items {
			configured[item.Ref] = !item.Revoked() && item.Available
		}
		expected := credentials.ExpectedRefs()
		out := make([]expectedCredentialItem, 0, len(expected))
		for _, e := range expected {
			out = append(out, expectedCredentialItem{
				CredentialRef: e.Ref, Platform: e.Platform, Purpose: e.Purpose,
				Configured: configured[e.Ref],
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListConnectorConfigsHandler 返回当前环境下各平台的连接器运行配置。
func ListConnectorConfigsHandler(store CredentialQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		environment, err := credentialEnvironment(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := store.ListConnectorConfigs(r.Context(), environment)
		if err != nil {
			WriteError(w, r, credentialQueryFailed(err))
			return
		}
		out := make([]connectorConfigItem, 0, len(items))
		for _, item := range items {
			out = append(out, toConnectorConfigItem(item))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
