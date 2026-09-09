# CR-0005：开票管理端迁入星芒控制台（按平台放在「支付与财务 → 开票」）

> 状态：**已批准的产品需求，第一阶段（嵌入式迁入）授权实施**；第二阶段（原生迁入）仅列路线，另行授权。
> 依据：产品负责人 2026-09-02 指令（"开票系统原本有个管理员页面，既然合并到星芒统一控制平台了，管理员这块也应相应移动到星芒，按平台分别进 newapi、sub2api 的支付与财务下面的开票"）；ACCEPTANCE-LOG 2026-09-02 APPROVED CR-0003 条目已预告本 CR。

## 发起方
产品负责人；由验收线整理成文（平台线与开票线当前均由验收线执行）。

## 接收方
平台线（xingmang-platform 控制台）与开票线（invoice-system）。

## 目标资源
- 开票系统管理端 Web（`https://invoice.solov.cc/admin`）、其 nginx 虚拟主机模板与安全头校验脚本、两个管理列表接口的过滤参数；
- 星芒控制台：Sub2API / NewAPI 的「支付与财务」页签，治理「跨平台财务 → 开票集成」页签，`web-app-config` 运行时配置。

## 背景事实（2026-09-02 两线只读调研结论）
1. 控制台里 Sub2API → 支付与财务 → 开票 的槽位已存在（`PlatformFinancePanel.tsx` 渲染 `PageState kind="unavailable"` 占位，注明等 CR-0002）。NewAPI 的支付与财务只有 2 个子页签，`docs/architecture/ADMIN-IA.md` §8.2 #2 曾裁定"不补开票"。**本 CR 按产品负责人 9/2 指令推翻该裁定**：NewAPI 同样增加「开票」子页签。
2. 平台 Action 内核当前只执行 L0/L1；L2 及以上一律以 `ADVANCED_CONTROLS_REQUIRED` 拒绝（Foundation-B：审批、幂等、步进 MFA、冷却、急停均未建，`internal/platform/approval/` 为空）。开票管理动作（审核/退回、开票确认、PDF 上传、支付候选双人复核、人工调减、解冻、退款结案、系统设置）按 ADR-003 风险表属 L3/L4（表中 L4 的例子就是"开票关键动作"）。**原生重建在今天无法执行任何写操作。**
3. ADR-018 只覆盖平台→开票的只读通道；没有任何 ADR 或 CR 授权写通道。宪法第 20 条要求跨线接口变更走 CR。
4. 开票管理端自带 OIDC 登录、10 分钟 MFA 步进、管理员 IP 白名单、CSRF（精确 Origin）、支付候选双人复核与完整审计，RC67 在生产运行中；共 24 个管理接口、6 个管理页面。申请单、资金批次/支付候选、资格冻结、源健康行都带 `source_instance_id`，可归属到唯一平台；退款案例需经 `funding_lots` 关联；系统设置、管理员 IP 白名单、管理员会话是全局的。
5. 开票安全架构（`docs/SECURITY-ARCHITECTURE.md`）现规定管理页面 `frame-ancestors 'none'`、"敏感管理在顶层页面打开"；nginx 对 `/admin` 与 `/admin/` 强制该 CSP。

## 变更内容（第一阶段：嵌入式迁入）
结论：控制台在每个平台的「支付与财务 → 开票」内以 iframe 承载开票系统管理端；管理端进入"嵌入管理模式"并按该平台过滤；**鉴权、审批、双人复核、审计全部仍由开票系统执行**；平台不新增任何到开票系统的数据通道（不触碰 ADR-018 四道只读闸，不引入写通道，不展示任何开票数字）。

### 开票线（切片 XM-INV-ADMIN-EMBED）
a. 入口：nginx 新增 `location = /embed/admin/sub2api`、`/embed/admin/newapi`、`/embed/admin/global`，同用户嵌入入口一样 `access_log off` + `no-store` + 303 到 `/admin?ui_mode=embedded_admin&platform=<sub2api|newapi>` 或 `/admin?ui_mode=embedded_admin&scope=global`。
b. 嵌入管理模式：隐藏门户外壳（顶栏、页脚、与嵌入无关的导航），保留管理端自身的横向子导航。平台模式只渲染：申请审核（含"发票档案"视图）、资格冻结、退款与红冲、源健康（仅该平台的 5 条流）；NewAPI 平台模式额外渲染支付候选（Sub2API 无此队列，不渲染，不是置灰）。global 模式只渲染系统设置（开票配置 / SMTP / 管理员 IP）与两平台源健康总览。
c. 服务端按平台过滤：`GET /api/v1/admin/invoice-requests` 与 `/eligibility-freezes` 已有 `source_instance_id`；为 `GET /api/v1/admin/payment-candidates` 与 `/refund-cases`（经 `funding_lots` 关联）补同名过滤参数。嵌入管理模式下前端所有列表请求必须带该参数，并隐藏平台/来源选择控件；详情页与文档下载对不属于当前平台的对象显示"不在当前平台"。这是视图约束而非安全边界：员工视角本就可跨平台（CR-0003 分域纪律）。
d. 认证：嵌入时 OIDC 登录与 MFA 步进在**弹出的顶层窗口**完成（Keycloak 页面不可被 iframe 承载，且安全架构要求敏感认证在顶层），完成后 iframe 内会话自动生效（同站 cookie）。收到 `ADMIN_STEP_UP_REQUIRED` 时显示"重新验证"按钮，由用户手势触发弹窗。IP 白名单、CSRF（iframe 内同源请求的 Origin 即 `invoice.solov.cc`）、双人复核、审计全部不变。
e. 安全头：`/admin` 与 `/admin/` 的 CSP 由 `frame-ancestors 'none'` 改为 `frame-ancestors https://console.solov.cc`（唯一批准的第一方来源）；nginx 模板与 `scripts/verify-web-security-headers.ps1` 同步；API 响应安全头不变。`postMessage`（高度同步、导航）仅接受精确配置的父来源，沿用用户嵌入的版本化消息规范。`docs/SECURITY-ARCHITECTURE.md` 修订为："管理认证在顶层窗口完成；管理页面可被唯一批准的第一方控制台来源承载。"
f. 独立的 `https://invoice.solov.cc/admin` 继续可用，作为回退路径。

### 平台线（切片 XM-INVCON0）
g. Sub2API → 支付与财务 → 开票：用嵌入页替换占位。NewAPI → 支付与财务：新增「开票」子页签（`web/packages/ui-admin/src/navigation.ts`，并在 `ADMIN-IA.md` §8.2 #2 记录本 CR 推翻）。治理 → 跨平台财务 → 开票集成：承载 global 模式（设置 + 两平台源健康总览）。
h. 嵌入组件：`ui-admin` 新增 `EmbeddedConsoleFrame`（含 Storybook 故事）：`src = ${invoiceConsoleOrigin}/embed/admin/<sub2api|newapi|global>`；不加 `sandbox`（需要弹窗与同站 cookie）；`allow="clipboard-write"`；仅接受 `invoiceConsoleOrigin` 的 `postMessage`；高度按消息或视口自适应；加载失败时显示 `PageState kind="error"` 并提供"在新窗口打开"链接。
i. 配置：`XM_INVOICE_CONSOLE_ORIGIN`（静态环境变量模式，同 reqlog/CPA 先例）由 `deploy/docker/web-app-config.sh` 写入 `window.__XM_CONFIG__.invoiceConsoleOrigin`；缺省时页签显示 `PageState kind="unavailable"`（"未配置开票控制台来源"），不渲染 iframe。
j. 可见性以现有 `finance.read` 作用域控制；菜单可见不等于授权，真正授权由开票系统执行。
k. 新鲜度：嵌入的是实时管理界面，宪法第 12 条由嵌入应用自身的水位与源健康显示满足；平台侧不展示任何开票数字。

## 明确不变
- 开票资格算法、账本、审批、双人复核与审计全部留在开票系统（ADR-006）；平台不重实现、不缓存、不展示开票数字。
- 不新增平台→开票的任何 HTTP 或数据库通道；ADR-018 四道只读闸、CR-0002 与 XM-0028 只读契约（DRAFT）不变。
- 开票管理端的 OIDC、MFA 步进、IP 白名单、CSRF、双人复核规则不变。
- 用户侧嵌入（`?ui_mode=embedded`）与 CR-0003 的平台隔离不变。
- 员工视角可跨平台（治理页签）；CR-0003 的员工/用户分域纪律不变。

## 影响面
- 系统：invoice-system 的 web、nginx 模板、安全头校验脚本、两个列表接口的过滤参数；xingmang-platform 的 admin-web 导航与页面、`ui-admin` 组件、compose 环境变量与 `web-app-config.sh`。
- 用户：仅运营员工。需在控制台内额外完成一次开票系统 OIDC 登录（与今天直接访问 `invoice.solov.cc/admin` 相同）。
- 数据：无迁移，无新数据流。

## 验证
1. 控制台 Sub2API / NewAPI 开票页签内：登录弹窗 → 列表按平台过滤（Sub2API 页签零 NewAPI 元素，反之亦然）→ 完成一次审核与一次开票确认 + PDF 上传（NewAPI 侧另做一次支付候选复核），审计记录与独立管理端一致。
2. 治理「开票集成」仅显示设置与两平台源健康。
3. `curl -I https://invoice.solov.cc/admin` 的 CSP 为 `frame-ancestors https://console.solov.cc`；非控制台来源框入被拒；`scripts/verify-web-security-headers.ps1` 通过。
4. 两仓库全量门禁绿；开票侧走签名发布仪式（RC）。

## 回滚
- 开票侧：nginx 模板改回 `frame-ancestors 'none'`（即时生效）并回滚 RC 镜像。
- 平台侧：撤回导航/页面提交，占位恢复；`XM_INVOICE_CONSOLE_ORIGIN` 置空即隐藏 iframe。

## 第二阶段（路线，不在本 CR 授权范围）
原生迁入：XM-0028/0029 只读连接器落地（含管理级只读投影与 `invoice.read` 作用域，需先冻结 CR-0002）→ 控制台原生列表/详情/日汇总（带新鲜度）→ Foundation-B（审批、幂等、步进、急停）+ 平台→开票写通道 ADR → 开票管理 Action（L3/L4）→ 退役开票管理端 Web。每一步各立切片；写通道 ADR 与 CR-0002 冻结为前置。

## 确认
- 平台线：验收线，2026-09-02（按产品负责人指令）。
- 开票线：验收线，2026-09-02（按产品负责人指令）。
- 产品负责人：2026-09-02 口头指令；本 CR 为其书面化，"嵌入式第一阶段 + 控制台内需再登录一次开票系统"的取舍已在同日汇报中说明。

## 执行记录
- 2026-09-02 立单；派发切片 XM-INV-ADMIN-EMBED（开票线）与 XM-INVCON0（平台线）。
