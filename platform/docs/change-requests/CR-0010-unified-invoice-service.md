# CR-0010：控制台与开票共用一个 API 进程

状态：2026-09-12 按负责人《06-统一进程-可部署终点》的 A–F 闭合清单重整；本地实现与合成演练获准，生产、推送未获准。

## 已确认的边界

SUB 与 NEW 共用开票应用，登录后的平台隔离继续有效。控制台与开票服务合并为一个 Go HTTP 主进程，管理页面由控制台直接加载 React 组件，不再使用 iframe、postMessage、跨服务登录断言或 Keycloak。客户账号、员工账号和财务数据不合库、不按邮箱合并。财务算法继续仅由开票模块实现。

本决策替代 ADR-016 的保留 Keycloak 决策以及 CR-0005/0006 的 iframe/断言集成方式。ADR-018 的独立采集凭据、Bridge 权限、只读连接和数据访问隔离继续有效；同进程不授予平台 Connector 写开票库的权限。历史迁移和已签发布证据保持原字节。

## 运行与身份契约

- `platform/cmd/platform-api` 是唯一 API 监听入口；直接装配 `invoice-system/backend/service`，不启动子 API 进程、不转发 HTTP 到另一个后端。后台 worker、源采集器和文件扫描隔离进程保留各自职责。
- 平台 API 为 `/api/v1`，开票 API 为同源 `/invoice-api/v1`。组合层只映射路由路径，保留方法、查询、正文和原有安全验证。统一 `/readyz` 分别报告平台、开票来源、开票投影；HTTP 状态取决于平台。开票未运行到的短路检查明确标 not_evaluated，不冒称就绪。`/invoice-api/readyz` 保留开票自己的严格状态。D/E 要求三个模块和完整 invoice_ready 同时通过。
- 生产必须同时为 `ENVIRONMENT=production`、`APP_ENV=production`、`XM_AUTH_MODE=local`、`AUTH_MODE=session`，开票模块不可静默关闭。
- 原生管理页面读取 `/invoice-api/v1/auth/staff-session`。身份由进程内 typed resolver 每请求读取当前平台 HttpOnly 会话；校验员工 UUID、真实角色、`finance.read`，继续强制 MFA 新鲜度、管理员 IP、精确 Origin、CSRF。任何调用方 HTTP 头不能直接注入员工身份。
- 管理员不再另发开票会话。同步 CSRF 值通过每次启动的内存随机密钥与平台会话 token 作域分隔 HMAC 派生，不输出原 token。退出登录、禁用账号或调整角色在后续请求即时生效。
- `INVOICE_STAFF_ORIGIN` 必须是明确的控制台 HTTPS 来源，对既有相同控制台 issuer + 员工 UUID 保持身份。历史 Keycloak tuple 不会自动合并；负责人以只读 crosswalk 核对归属与历史迁移凭据，未连续的身份明确阻断并列入交接，不改历史操作人、外键或加密 AAD。
- 客户仍通过 SUB/NEW 平台登录，使用开票自己的 HttpOnly 会话和 CSRF；`session.platform` 与 source instance 限制保留。普通客户会话不能进入管理 API，旧 OIDC/断言会话不能恢复管理权限。
- 旧授权码、回调、step-up、backchannel、OIDC bearer、统一登录绑定入口及专属配置/依赖删除。平台本地 TOTP 页面和 SUB/NEW 登录保留。

## 六组实现主题与唯一验收定义

基线：原 main `9d430fb284e5e9b91327089ae39cef207c42f482` + 完整审计 `d407875429a7ff9c3b0b5017a4b632db6324eacf`，合并节点 `d0adca1acf56ac717e0a4803652fe94e4ba4b267`。旧功能分支只提取 CR 所需变更，不整体合入；其资金、通知、0033–0038 及 PG 补丁仍冻结。

1. 运行时抽取：独立数据库池、受限角色、原任务/扫描/通知行为；生命周期、路由与分模块就绪。
2. 员工身份解析：进程内可信 resolver、当前会话/角色/finance.read、TOTP 新鲜度、IP、Origin、CSRF 与客户隔离。
3. 原生管理页：复用控制台 Router 与原开票组件，保留财务表单/算法及 SUB/NEW 会话隔离。
4. 统一构建与 compose：monorepo 根上下文、单 API、独立数据/采集/扫描、原官方 PG digest，实际镜像及基础镜像证据。
5. 旧登录链删除：旧 OIDC/断言实现、入口、专用配置和无用依赖退役；历史迁移及来源身份字段保留原字节。
6. 测试与证据：完整七门禁、26 项逐项补验、C1/C2 只读审计、D 合成演练、E 切换/回滚真跑与干跑、F 交接。

每组提交说明实际失败测试与变异结果。仅《06》A–F 是可部署终点；不加功能、不升级依赖、不修基础镜像 CVE。完成 F 后停止，由负责人在服务器运行 D。证据目录 `G:/xingmang/logs/unified-deploy-endpoint-20260912`；不改 main 或 tag、不推送、不部署、不查看密钥内容。
