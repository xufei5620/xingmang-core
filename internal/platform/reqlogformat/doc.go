// Package reqlogformat 定义 reqlog 记录代理的磁盘格式，与两侧共用的纯函数
// 解析逻辑（不做任何 I/O）。
//
// 「两侧」指：
//
//   - cmd/reqlog-recorder ——写侧。以 systemd 服务跑在宿主机，站在宝塔 nginx
//     与 NewAPI/Sub2API 之间做透明反向代理，把请求/响应明文落盘（XM-REQLOG-MERGE
//     从桌面端原型 reqlogger.go 收编而来）；
//   - connectors/reqlog 的 NewFileClient ——读侧。以只读挂载读同一份磁盘数据，
//     实现 ReadClient 契约，供 internal/platform/requestlog 使用。
//
// 两侧必须对 Record/FullRecord 的字段与 JSON 标签、以及"如何从请求/响应正文
// 提取对话轮次、最终回复文本、token 用量"这几件事有**完全一致**的理解——
// 写错一个字段名或解析口径漂移，症状是"能写进去、读出来对不上"，而这类
// bug 只有跨进程集成测试才抓得住。把两者都需要的类型与函数放进同一个包，
// 让"同一份代码"替代"两份必须手动保持同步的实现"（宪法 4 条：同一个业务
// 动作只实现一次）。
//
// 本包不属于任何一侧的私有实现细节，也不对外网络 I/O、不碰数据库、不依赖
// internal/platform 的其它治理组件（Action/Query/审计）——它只是"reqlog 磁盘
// 记录长什么样、怎么从原始请求/响应正文里抽出人能看懂的内容"这件事的唯一
// 权威实现。ADR-011（控制平面与数据平面边界）不适用于本包：reqlog-recorder
// 是既有、独立于「星芒平台控制面」的宿主机数据面组件（在本次收编之前已在
// 生产运行），本包只是给它和只读读它的连接器提供共享的格式代码，不代表
// 平台控制面进入了用户请求路径。
package reqlogformat
