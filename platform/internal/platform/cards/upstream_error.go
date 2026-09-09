package cards

import (
	"errors"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// upstreamMessages 把连接器的错误分类翻成运营看得懂、且**指向下一步**的话。
//
// 为什么要翻：不翻的话页面上只有「action cards.card.issue 执行失败」，
// 而这句话对四种完全不同的处置一视同仁——改配置、改金额、等一等、别动等人。
// 2026-09-05 上线当天就是对着这句话排查了很久。
//
// 每一条都只说**这一侧能做什么**，不复述上游原文（ADR-004：供应商原文只进
// 服务端日志）。分类本身的依据见 connectors/infini/envelope.go。
var upstreamMessages = map[connector.ErrorKind]string{
	connector.KindAuth: "上游拒绝了认证：请到「设置 → 密钥引用」核对该账号的 API 密钥，" +
		"并确认服务器时钟与标准时间相差不超过 5 分钟。",
	connector.KindIPNotAllowed: "上游拒绝了来源或权限：请确认服务器出口 IP 在该账号的 API 密钥白名单里，" +
		"且密钥勾选了所需权限。",
	connector.KindRateLimited: "上游限流（每个密钥每分钟 600 次）：稍后重试即可，这一笔确定没有生效。",
	connector.KindRejected:    "上游拒绝了这次请求：金额、参数或账户状态不满足条件；这一笔确定没有生效。",
	connector.KindForbiddenTarget: "请求没有发出去：目标主机不在平台的出站白名单里。" +
		"这是平台侧的配置，不是上游的问题。",
	connector.KindMethodNotAllowed: "请求没有发出去：这个动作用了写通道不允许的方法，属于代码缺陷。",
	connector.KindNotSupported:     "这个账号当前的接入模式不支持该操作。",
	// 下面两类是**可能已经生效**的那一侧，措辞必须明确劝阻重试。
	connector.KindUnavailable: "上游没有给出明确答复（超时或服务不可用）：" +
		"这一笔可能已经生效，**请勿重试**，等待对账收敛或到上游后台确认。",
	connector.KindBadResponse: "上游返回了无法解析的内容：" +
		"这一笔可能已经生效，**请勿重试**，请到上游后台确认后再处理。",
}

// explainUpstream 把领域层拿到的错误包装成带可读文案的 Action 错误。
//
// 只包装连接器错误：领域层自己的错误（限额、账号未配置、幂等冲突）本来就
// 是中文且指向明确，再套一层只会把话说远。
func explainUpstream(err error) error {
	if err == nil {
		return nil
	}
	var ce *connector.Error
	if !errors.As(err, &ce) {
		return err
	}
	message, ok := upstreamMessages[connector.KindOf(err)]
	if !ok {
		return err
	}
	// 原始错误进 cause：服务端日志与审计仍能看到完整链路（含上游原文与
	// 响应体开头），对外只有这句可读文案。
	return action.NewError(action.CodeExecutionFailed, message, err)
}
