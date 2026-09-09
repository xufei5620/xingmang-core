package server

// ScopeManage 是登记/修改服务器资产、供应商、域名与服务备注所需的权限
// （L1 动作）。
//
// **不复用 registry.read 或任何既有 .manage scope**：这批 Action 写的是
// 独立的四张登记表，与既有 Service/Connector/Connection 的注册表无关；
// 给它一个专属 scope，「谁能改服务器登记簿」才是一道可以单独审定的授权面，
// 而不是搭在别的能力上顺带获得。
//
// 读侧**刻意不新建 scope**：GET /api/v1/servers/* 复用既有的
// registry.ScopeRead（"registry.read"，能看服务清单的人本就该能看服务器
// 登记簿——两者都是「平台管着哪些基础设施」这同一类知识面，泄漏面相当）。
const ScopeManage = "server.manage"
