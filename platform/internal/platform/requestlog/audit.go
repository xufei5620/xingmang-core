package requestlog

import (
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

const (
	// EventContentViewed 是「有人看了一条请求的正文」这条事实的事件类型。
	//
	// 它落在审计事件的 action_id 列上，尽管**它不是一个 Action**——那一列的
	// 语义是「这条事件记的是哪一类动作」，读事件是其中一类。规格 §4.4 的字段
	// 集合是为写动作设计的，而审计链本身没有理由只装写动作：一次高敏读取
	// 恰恰是最需要不可篡改记录的那种事。
	EventContentViewed = "request.content.viewed"

	// EventVersion 是本事件类型的版本。摘要字段集合变化时递增，
	// 让读审计的人能分辨「这条事件是按哪一版记的」。
	EventVersion = "1"

	// ResourceTypeRequest 是被查看对象的类型。
	ResourceTypeRequest = "reqlog.request"
)

// contentViewedEvent 把一次正文查看转成审计事件。
//
// 从 Service.Content 里拆出来是为了让「摘要里有没有正文」这条边界规则能被
// **不带数据库**地测到——与 audit.eventFromActionEvent 拆出来的理由相同。
//
// 摘要里放什么，是这个函数唯一真正的决定。原则：记下**足以说清这次披露了
// 什么**的指纹，但不记内容本身。所以有 model / username / 消息条数 / 字节数
// （回答「披露面有多大」），没有一个字的对话文本。
//
// 尤其**不放 token_prefix**：审计事件会经 /api/v1/audit/events 原样回显给
// 持有 audit.read 的人，而 audit.read 与 request.content.read 是两个独立授予
// 的权限。把令牌前缀写进摘要，等于让前者顺带看到后者数据里的一个字段。
func contentViewedEvent(
	now time.Time, in ContentInput, source string, content reqlog.RequestLogContent,
) audit.Event {
	summary := map[string]any{
		"source":    source,
		"record_id": in.ID,
		"model":     content.Summary.Model,
		"username":  content.Summary.Username,
		"occurred_at": content.Summary.OccurredAt.UTC().
			Format(time.RFC3339),
		"upstream_request_id": content.Summary.UpstreamRequestID,
		// 披露面的大小：几条消息、回复多长、原文多大。
		// 这几个数字让「谁在批量翻看内容」这种模式在审计里看得出来
		"message_count":     len(content.Messages),
		"final_reply_bytes": content.FinalReplyBytes,
		"request_bytes":     content.RawRequest.OriginalBytes,
		"response_bytes":    content.RawResponse.OriginalBytes,
	}

	return audit.Event{
		OccurredAt:    now,
		PrincipalID:   in.Principal.ID,
		PrincipalType: in.Principal.Type,
		ActionID:      EventContentViewed,
		ActionVersion: EventVersion,
		// ActionRunID 恒为零值 UUID：读取没有 Action 运行记录可指。
		// 不现编一个随机 UUID——那会让审计页上多出一个点进去什么都没有的
		// action_run 链接。这次查看的关联锚点是 request_id（与访问日志对得上）
		// 与 resource_id（看了哪一条）。
		ActionRunID:  uuid.Nil,
		ResourceType: ResourceTypeRequest,
		// source 拼进 resource_id：两个来源的 id 可能重号，只记 id 的话
		// 「看的是哪一条」这个问题在审计里就答不上来
		ResourceID:  source + "/" + in.ID,
		Environment: in.Principal.Environment,
		Reason:      in.Reason,
		RequestID:   in.RequestID,
		// 纵深防御：摘要里今天没有任何敏感键名，但这条链路是 append-only 的
		// ——一旦有人往摘要里加了带 token 的字段，那条记录就**永远**在链上，
		// 改读 API 只是不再显示它（与 audit.ActionSink 同一条论证）
		AfterSummary: audit.RedactDefault(summary),
		Result:       audit.ResultSucceeded,
	}
}
