// Package extapp 是**前端应用登记簿**与**发布记录簿**（XM-EXT-APP，2026-09-08）。
//
// 「应用」的定义由产品负责人当日给出，逐字记在 docs/architecture/ADMIN-IA.md
// §5.4.1：**平台自己纳管的前端站点**——我们自己部署的那些前端（admin-web、
// console 这类）的域名、环境、登录方式、当前发布的版本、负责人与状态。
//
//   - **不是**被管平台（Sub2API / NewAPI）的前端。被管系统实例仍然只在
//     internal/platform/registry 的 core.service 里；
//   - **不是**通用低代码页面搭建器。平台没有页面搭建器，因此「应用与配置」
//     页里的「页面配置」「页面组件」两格保持蓝图态（见 ExtAppPage.tsx）。
//
// 两张表：
//
//   - App     一个前端站点在一个环境里的登记（core.ext_app）
//   - Release 一次**已经发生**的发布（core.ext_app_release）
//
// 铁律：
//
//   - **写路径全部经 Action**（宪法 2 条 / ADR-003）。本包只提供领域类型、
//     校验与仓储；Store 的写方法只对本包的 Action Handler 与集成测试可见。
//   - **本包不发布任何东西**。Release 记录的是「已经发生的一次发布」，
//     发布与回滚本身是 Platform Lifecycle Operation（宪法 2、3 条）——
//     走版本化脚本 + 人工批准，不经 Action 通道，平台没有也不会有发布端点。
//     「记录一次发布」是平台配置写操作，所以它**必须**经 Action；
//     「执行一次发布」不经 Action。两件事共用「发布」两个字，但不是同一件事。
//   - **纯登记，零探测**：不请求站点、不解析域名、不查证书、不读构建产物。
//     status 是登记簿口径不是探活结果，与 internal/platform/server 同一条纪律。
//   - **environment 显式**（宪法 15 条）：写操作的环境取自调用者 Principal，
//     不由参数自称；Release 挂在 App 下，不重复存 environment 列
//     （同 core.server_service_note 挂在 server_asset 下的理由）。
//
// 领域类型（types.go）零 I/O、不依赖数据库；store.go 是本包唯一接触
// PostgreSQL 的文件。
package extapp
