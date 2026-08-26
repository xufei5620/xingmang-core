// Package connector 提供 Connector 的通用抽象与安全护栏（ADR-004、ADR-018）。
//
// 铁律：
//   - **只读通道的「无写路径」由类型强制，不靠代码评审**：ReadOnlyTransport
//     拒绝任何非 GET/HEAD 请求与任何不在 allowlist 内的主机（ADR-018 闸 4）；
//   - allowlist 为空时**全部拒绝**——fail closed。配置漏填不能变成「放行一切」；
//   - 凭据只经 CredentialRef（ADR-014），Config 不接受任何内联明文；
//   - 第三方错误不原样透传：统一映射为 ErrorKind，原始文本只进服务端日志
//     （ADR-004 铁律、规格 §18.4）。
package connector
