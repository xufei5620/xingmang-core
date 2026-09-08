package extapp

// ScopeManage 是登记 / 修改前端应用与登记发布记录所需的权限（三个 L1 动作）。
//
// **新建专属 scope，不复用既有的 .manage**——与 server.ScopeManage 同一条
// 理由，并且是本仓库对「新登记簿」这一类切片最近一次的既定做法（XM-SERVER0）：
// 给它一个专属 scope，「谁能改前端站点登记簿」才是一道可以**单独审定、
// 单独撤销**的授权面，而不是搭在别的能力上顺带获得。
//
// 具体到候选项：
//
//   - `registry.service.manage` 管的是被管系统实例（Sub2API / NewAPI 的
//     Service 行），它连带着 Connector / Connection 的挂载点；把前端站点的
//     写权限并进去，等于「能登记我们自己的前端」与「能改被管系统的接入点」
//     再也拆不开；
//   - `server.manage` 管的是服务器资产、供应商、域名与证书。前端站点跑在
//     那些服务器上，但「谁能改机房与采购台账」和「谁能改站点登记」不是同一
//     个岗位——合并只会让其中一边被迫将就。
//
// 读侧**刻意不新建 scope**：GET /api/v1/ext/apps 与 /ext/apps/releases 复用
// 既有的 registry.ScopeRead（"registry.read"）——能看服务清单与服务器登记簿
// 的人本就该能看「我们自己有哪些前端站点」，三者是「平台管着哪些东西」这同
// 一类知识面，泄漏面相当（同 XM-SERVER0 读侧的取舍）。
const ScopeManage = "extapp.manage"
