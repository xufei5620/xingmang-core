package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// UpstreamVersionAckLister 是「已核对的上游版本」的只读能力
// （*alerts.Store 满足）。
//
// 与 AlertLister / SilenceLister 同一条先例：同一个 Store 上的第三个窄接口。
// 分成三个字段而不是一个宽接口，是为了让每个端点只声明自己真正要的那一个
// 方法——也让测试桩不必为了一个列表实现另外五个方法。
type UpstreamVersionAckLister interface {
	ListUpstreamVersionAcksOrdered(ctx context.Context, environment string) ([]alerts.UpstreamVersionAck, error)
}

// upstreamVersionAckItem 是一条已核对记录的对外投影。
//
// 五个字段全部是**平台自己写下的事实**：metric_key 与 version 在执行时被逐字
// 校验过（version 必须与平台观测到的 value_json.version 相同），source 来自
// 观测，acknowledged_by 来自 Principal，acknowledged_at 来自内核。
//
// **note 有意不在这里。** 它是执行者写的自由文本，除 ≤200 字节外没有任何形态
// 校验——它在形状上装得下凭据，这正是 alerts.upstream_version.acknowledge
// 被永久锁在 L1 的第一条理由（见 alerts/actions.go 的
// acknowledgeUpstreamVersionDef）。那条理由说的是「不能给 note 开一条展示给
// 人看的通道」，而本端点的权限是 alerts.ScopeRead（staff 这个粗粒度角色就有，
// 见 docs/modules/httpapi/PERMISSIONS.md）。把 note 放进来等于把它从
// audit.read 那一档掉到 ops.read 这一档——审计事件的读路径单独要
// audit.ScopeRead，router.go 那一行自己写着「敏感度高于 ops.read」。
//
// 所以「当时那个人说了什么」走 GET /audit/events（audit.read），那是它本来
// 就该在的那一档；这份清单只回答「这条抑制是谁按的、按的是哪个版本」。
type upstreamVersionAckItem struct {
	MetricKey      string `json:"metric_key"`
	Version        string `json:"version"`
	Source         string `json:"source"`
	AcknowledgedBy string `json:"acknowledged_by"`
	AcknowledgedAt string `json:"acknowledged_at"`
}

// ListUpstreamVersionAcksHandler 列出某环境下全部「已核对的上游版本」。
//
// **这个端点存在的理由是：已核对版本是一个抑制器，而抑制器必须看得见。**
// 在它之前，一条 upstream.version.changed 被压住之后，系统里没有任何读路径
// 能回答「哪些版本被标成已核对了、谁标的、什么时候标的」——只有评估器每轮的
// version_ack_suppressed 计数能证明「有东西被压住了」，却说不出是什么。
// 一个看不见的抑制器与一个不留痕的抑制器是同一种病。
//
// 撤销走 Action（alerts.upstream_version.revoke），不在这里开第二条写路径。
// 权限复用 alerts.ScopeRead：这份记录的泄漏面比告警正文更小。
func ListUpstreamVersionAcksHandler(store UpstreamVersionAckLister) http.HandlerFunc {
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
		acks, err := store.ListUpstreamVersionAcksOrdered(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]upstreamVersionAckItem, 0, len(acks))
		for _, a := range acks {
			out = append(out, upstreamVersionAckItem{
				MetricKey:      a.MetricKey,
				Version:        a.Version,
				Source:         a.Source,
				AcknowledgedBy: a.AcknowledgedBy,
				AcknowledgedAt: a.AcknowledgedAt.UTC().Format(time.RFC3339),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": out,
			// 撤销入口写在响应里而不是只写在文档里：读到这份清单的人下一个
			// 问题必然是「点错了怎么办」，而这个 Action ID 是那个问题的答案。
			"revoke_action": alerts.ActionRevokeUpstreamVersion,
		})
	}
}
