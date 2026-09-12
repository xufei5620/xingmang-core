# 切换只读冒烟与隔离预检写流程

本轮 E 已按负责人授权改为 [SEED.md](SEED.md) 的五项公开验收，不需要真人凭据、不创建认证会话，结果不声称真人登录通过。本文件保留 `smoke.py` 作为 D 的认证与写流程实现：隔离播种后执行十二步，包括提交、审核、上传和双方下载；不修改生产余额、抬头或邮箱证明。

## 配置

下方 `xingmang.unified.smoke/v1` 是播种后引擎生成的 D 认证配置。初始 D/E 公开输入使用 `xingmang.unified.public-smoke/v1`，只有 mode、origins、connect_to、可选 ca_file；不得要求负责人提供以下真人凭据。旧独立 `smoke.py` CLI 仍校验其旧 schema，不能拿其结果替代本轮 E 五项验收。

origins.admin 与 origins.user 填实际配置的 HTTPS origin，例如 https://console.example.com，不带路径、查询、凭据或通配符。允许默认 443。默认使用系统 CA；私有 CA 可用 ca_file 指定公开证书。禁止 TLS 跳过验证、自动重定向和环境 HTTP 代理。

隔离预检可通过 connect_to 把两个 origin 的 TCP 连接限定到本机 TLS 监听器：

```json
{"connect_to":{"admin":{"address":"127.0.0.1","port":18443},"user":{"address":"127.0.0.1","port":18444}}}
```

只替换 TCP 目的地，URL、Host、Origin、TLS SNI 和证书域名验证仍采用配置的真实域名。监听器必须使用匹配域名的有效证书；不以 HTTP loopback 端口冒充 HTTPS，不修改公网 DNS 或生产 nginx。预检不得把请求送往公网 origin。

- credentials.staff：已注册 TOTP、已完成首改密的专用账户 username,password_file,totp_file。
- credentials.sub2api：现有测试账户 identifier,password_file,source_id,existing_profile_id,expected_email_file。
- credentials.newapi：另一个现有测试账户 identifier,password_file,source_id。
- expected.required_staff_role：实际配置的管理员角色；默认 admin。E 不要求正余额或创建充值。
- 仅预检配置必须指定整数 `expected.request_amount_minor` 与 `expected.minimum_available_minor`，且后者不少于前者。该账户必须在备份中已有足额、已消费、已核验的 wallet 资金和已验证抬头；不足即失败，不临时改资金或证明。

凭据仅通过绝对常规文件路径交给运行时消费，不在配置或证据中写入密码、TOTP、邮箱证明内容。两个来源必须有可区分的已有资金记录；SUB 抬头的 owner、已验证状态和接收邮箱必须匹配现有证明。

## 固定步骤与结果

顺序为 readiness.before → sub.login → new.login → source.isolation → staff.totp → staff.page → legacy.rejected → requests.read → readiness.after → sessions.revoked。

来源隔离核对两套真实登录、资金列表及客户读取管理员接口被拒绝。管理端经过真实 password/TOTP，读取页面 shell 和管理员列表。旧登录路由固定检查，不能由配置删减。读取现有申请时核来源归属与跨用户拒绝；空申请列表保持为空。这里不宣称发票写流程或浏览器 DOM 已通过验收。

登录一旦尝试，成功、失败和中断路径都执行会话清理。正常 logout 必须返回 JSON 200 且 `ok=true`；仅客户 logout 的 JSON 401 `AUTH_REQUIRED` 可表示会话此前已失效。随后重放登出前的 cookie，客户 `/invoice-api/v1/auth/session` 或员工 `/invoice-api/v1/auth/staff-session` 必须返回 JSON 200、`authenticated=false` 且无 error。403、普通错误、缺字段或非布尔值不证明撤销。最后清空内存 cookie；不能仅凭浏览器清 cookie 声称撤销成功。清理失败使整轮失败。

结果包含模式、origin、公开配置的规范化 SHA256、financial_writes_permitted=false、步骤/HTTP 的 UTC 和退出码。请求 body、cookie、凭据和原始异常文本不进入日志。引擎核对这些绑定及完整固定步骤。0 为全部通过，1 为探测/执行失败，2 为输入错误；中断保留 FAIL 证据。

## E 在停旧栈前的实际预检

顺序为 preflight → precheck_new → snapshot → stop_old → 原迁移/权限 → 新栈检查 → 切换/冒烟。precheck_new 重新执行一次 D 冻结副本恢复、原始迁移/11 闩检查和完整十二步预检，不能复用旧 PASS 代替本次执行。

## N-1：仅隔离副本的两步写入

`restore.rehearse` 在恢复、源状态与健康核对完成后调用 `preview_smoke.run_verified`，重新核验候选镜像/执行源码、全部 18 个冻结容器、数据卷与资源 owner、只读原始输入、隔离网络，以及与线上项目不同的项目名。两个预检 HTTPS origin 都必须显式钉到环回 TLS；端口不得复用任一在线 TLS 监听。部署描述仍保留线上 candidate，实际 D 项目与预检端口由 rehearsal 字段一起选择。

只有上述核验成功才在进程内创建写能力；没有 CLI 写开关，生产模式或配置中的 `preview`/`financial_writes_permitted` 字段不能授权写入。普通 Client 的出口写禁令保留。能力绑定本次公开配置，仅允许创建一个新申请及该返回 UUID 的四个精确动作路径。

在 `requests.read` 后、`readiness.after` 前执行：

1. `sub.submit`：SUB 提交申请，核对来源、金额、分配、状态和版本；NEW 读取该申请必须被拒绝。
2. `staff.approve-upload-download`：真实员工会话依次审批、开始开具、确认；上传合成 PDF 经实际 ClamAV/qpdf 与加密存储链，要求 scan_status=clean；客户与员工下载字节 SHA 都等于上传原件；其他客户下载必须被拒绝。

写能力只用于冻结副本。成功、失败及中断仍撤销三组会话，D finally 清理新数据卷及临时身份。预检结果为 `actual-frozen-preview-smoke`，十二步必须全通过并标明 `financial_writes_permitted=true` 与冻结 owner/项目/卷证明；E 仍必须是十步、`financial_writes_permitted=false`。不把单元模拟或只读结果当作写链实证。

预检的项目名与新旧在线项目均不同；数据卷必须新建且受原有所有权检查；原卷不作为可写副本；网络保持原有隔离规则。证据、临时解密身份和清理记录位于本次 candidate-precheck 子目录。清理不完整或镜像/HEAD 不匹配即拒绝；此前旧栈保持运行，不切流量。预检容量、独立 loopback TLS 入口和已签名备份由负责人提前准备，不能在生产预检中临时扩大权限。

数据库/权限完成后，先启动 unified 中的 API、ingest 及其余常驻服务，再启动 sources；两边都已发起启动后才等待原严格健康检查，最后仍核完整 inventory 和全部 readiness 闩。合法恢复资料的源心跳可能已过时，不能先等 API 的源流闩通过才启动负责刷新它的采集器。没有放宽健康规则，最终等待非零仍按原失败清理/回滚路径处理。

作业在执行前整组核对归属：三个 migrate/permissions 固定属于当次 candidate unified，冻结 verification_jobs 只能属于当次冻结 candidate；旧回滚权限作业只属于 previous。冻结项目不得重用旧在线项目名，错误的最后一条作业也会在第一条执行前拒绝。该校验先于冻结 driver/数据恢复，不以执行后的 ledger 相同代替隔离。

单元/变异结果仅证明代码边界。最终 HEAD 的本地 D/E 原始证据及服务器待执行事项统一见 F 交接单。

E 配置中的 rehearsal.host_preflight 必须提供独立预检副本的完整主机/网络/端口/环境清单，不沿用在线候选的项目名和端口。该项仍经过原 preflight 的全部校验。
