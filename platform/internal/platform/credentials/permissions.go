package credentials

// ScopeManage 是登记、轮换、吊销上游凭据所需的权限（L1 动作 + 只读清单）。
//
// **不复用 finance.* 或 registry.* 的任何 scope**：登记簿里的 credential_ref
// 只是引用，而这里的动作会把明文写进 SecretProvider 的文件目录——能看到
// 「这条渠道用哪个引用」的人，不该顺带获得「把这个引用指向另一把 token」的
// 能力。读与写共用一个 scope：清单里只有指纹、版本与可用性，没有值，
// 拆成两个 scope 不增加隔离，只增加授权配置（对照 ui.saved_view.manage）。
const ScopeManage = "credential.manage"

// ScopeConnectorManage 是设置连接器运行配置（fake/real、端点、白名单、
// 凭据引用）所需的权限（L1 动作 + 只读清单）。
//
// 与 ScopeManage 分开：切 real 决定「worker 下一轮去连哪台上游」，
// 粘贴凭据决定「拿什么去连」。两件事的爆炸半径不同，授权面也该分开。
// 默认不进 staff/admin（见 oidcauth.DefaultRoleScopeMap 的 credential-admin）。
const ScopeConnectorManage = "connector.manage"
