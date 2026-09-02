# NEWAPI_REAL_APPROVAL

> status：待审批（PENDING）。本文件是可执行的审批单——审阅人只需要按顺序核对
> 每一项、把方括号里的占位符替换成证据目录里的真实值、勾掉 checklist、签字，
> 不需要再做任何额外的工程调查。

- 事件名：`NEWAPI_REAL_APPROVAL`
- 依据：`docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md` §0、
  §10、§12；`docs/superpowers/plans/2026-08-28-platform-user-read-v2.md` Task 5
- 证据来源：`cmd/evidence-capture capture --platform newapi`（用法与安全说明见
  `docs/runbooks/USERS-REAL-APPROVAL.md`）

## 授权范围

| | 内容 |
|---|---|
| **本次批准的** | Task 5：NewAPI real GetUser v2（`connectors/platformusers/newapi_v2.go`，只声明真实证明过的 capability） |
| **明确不授权** | Sub2API、reqlog、DailyUsage、Key metadata 的任何 real 实现；invoice、payment；本片（XM-USERS-REAL-EVIDENCE0）本身不实现 Task 5；任何生产部署、迁移或 Action |

CORE_APPROVAL、SUB2_REAL_APPROVAL 已通过、本模板存在、脱敏证据已采集，**均不
构成本审批**。本审批必须由产品与安全在阅读完下方证据后逐项签字，且批准文本
必须明确写出事件名、目标平台、证据路径与哈希、允许的任务号（设计文档 §0）。

## 前置条件（Task 5 Step 1，缺一不可，任一缺失即停止 Task 5）

- [ ] 产品签字批准（见文末）
- [ ] 安全签字批准（见文末）
- [ ] 下方"证据"一节已填写完整并通过复核
- [ ] 管理员 token 已确认只经 CredentialRef 注入（`cmd/evidence-capture` 本身
      即是这条纪律的证明：见"证据"一节最后一项）

NewAPI 没有 Sub2API 那种 AdminComplianceGuard，`GET /api/status` 甚至不需要
鉴权（`connectors/newapi/upstream.go` 的 `fetchHealth` 注释），所以本审批没有
与之对应的"人工先去后台点一下"前置步骤。

## 证据

由 `cmd/evidence-capture capture --platform newapi ...` 生成到
`docs/evidence/users-real/newapi/<timestamp>/`；本文件只引用路径与哈希，
**不复制其内容**。

| 字段 | 值 |
|---|---|
| 证据目录 | `docs/evidence/users-real/newapi/[填入时间戳目录名]/` |
| 采集时间（UTC） | `[填入]` |
| 上游实例版本（`version.json` 的 `version`） | `[填入]` |
| 采集时的 `quota_per_unit`（`version.json`） | `[填入]`——**运行期可变**，
  这里记录的只是采集那一刻的观测值，不是永久基数，real reader 必须每次
  现读，不得硬编码 |
| 上游 endpoint 主机（`README.md` 的 endpoint host） | `[填入]` |
| 使用的 CredentialRef | `[填入，形如 secret://newapi-prod/read-token]` |
| `users_page.redacted.json` 的 sha256 | `[填入，见 SHA256SUMS]` |
| `user_detail.redacted.json` 的 sha256 | `[填入，见 SHA256SUMS]` |
| `version.json` 的 sha256 | `[填入，见 SHA256SUMS]` |

设计文档 §10 对 NewAPI 列出的证据要求，逐项核对（勾选即代表审阅人已在证据
目录里亲自核实，不是自动为真）：

- [ ] **`/api/user/` 的尾斜杠、分页 envelope、真实脱敏响应**——对照证据目录
      的 `users_page.redacted.json`；envelope 应是
      `{success, message, data:{items,total}}` 形状，`success` 应为 `true`。
- [ ] **soft-delete 形态**——`users_page.redacted.json` 里若样本恰好抽到过
      软删除用户，其 `DeletedAt` 字段应是一个非 `null` 值（不是字符串
      `"null"`），未软删除的用户则是字面 `null`；若这次抽样的 5 条都没有
      软删除用户，需要在 `README.md` 的"字段捕获"一节确认 `DeletedAt` 是否
      至少作为 kept 字段出现过，并在决定里注明"本次证据未观测到实际软删除
      样本"，而不是当作已验证。
- [ ] **`topup`、`log/data`、token metadata 是否携带同一稳定 user_id**——
      **本工具不采集这一项**：`cmd/evidence-capture` 只请求 `/api/user/` 与
      `/api/status` 两个端点（见证据目录 `README.md`"本次请求了什么"一节），
      不触达 `/api/user/topup`、`/api/log/`、`/api/data/`、token 相关端点。
      这一项需要 Task 5 实现者另行核实，本审批**不能**因为拿到了用户列表
      证据就当作这一项也已满足。
- [ ] **ID、created_at、last_login_at、status、quota_per_unit**——对照证据
      目录 `README.md`"字段捕获（脱敏）vs. 丢弃"一节；kept 字段应恰好是
      `id`/`username`/`display_name`/`email`/`status`/`quota`/
      `last_login_at`/`DeletedAt` 八项，与
      `connectors/platformusers/upstream.go` 已核对过的 `newapiUserItem`
      逐字一致。dropped 字段列表里出现陌生名字（比如某个字段名不在
      `github_id`/`discord_id`/`wechat_id`/`telegram_id`/`oidc_id`/
      `linux_do_id`/`remark`/`setting`/`stripe_customer`/`aff_*` 这批已知
      的第三方身份与内部字段之内）是需要额外留意的信号。
- [ ] **PII 字段**——`README.md`"字段捕获"一节的 kept 列表里不应出现
      `github_id`/`discord_id`/`wechat_id`/`telegram_id`/`oidc_id`/
      `linux_do_id`/`remark`/`setting`/`stripe_customer`/`aff_*`
      或任何手机号/地址/税号形状的字段；`email` 应已按
      `platformusers.MaskEmail` 打码（形如 `xx***@域名`）。
- [ ] **GET 写端点黑名单仍生效**——`connectors/newapi/upstream.go` 的
      `writeDisguisedAsGetRoutes`（`/api/user/token`、`/api/user/aff`、
      `/api/user/epay/notify` 等）与 `cmd/evidence-capture` 自己独立维护的
      同一份清单（`cmd/evidence-capture/plan.go` 的
      `knownMockOrWriteRoutes`）机械拒绝这些路径，见该文件同名测试
      `TestMustNotBeMockRouteBlocksKnownBadRoutes`——本工具从未、也无法
      请求这些端点。
- [ ] **管理员 token 只经 CredentialRef 注入、错误正文零透传**——
      `cmd/evidence-capture` 从命令行永不接受明文 token（只接受
      `secret://<scope>/<name>` 引用，见 `--credential-ref` 参数说明），凭据
      经 `internal/platform/secrets` 解析、只在拼请求头的那一瞬使用；上游
      错误响应正文从不进入本工具的错误信息或日志（见
      `cmd/evidence-capture/httpfetch.go` 的 `fetchOne` 实现与注释）。

## 审阅人 checklist

- [ ] 已读 `docs/evidence/users-real/newapi/[timestamp]/README.md` 全文
- [ ] 已核对 `SHA256SUMS`：目录内每个文件重新计算的 sha256 与 `SHA256SUMS`
      里记录的一致
- [ ] 已确认 `users_page.redacted.json` / `user_detail.redacted.json` 里
      不含任何真实邮箱全文、真实用户名、真实 token、真实完整 quota 数字
- [ ] 已核对 `quota_per_unit` 的观测值是否是一个"正常"值（历史经验里这个
      基数常见值是 500000；若观测到明显异常，比如 0 或极端大/小值，需要
      在决定里注明并考虑是否要求重新采集）
- [ ] 已确认本审批**不**依赖 topup/log/data/token 端点的证据（这些超出
      本工具当前采集范围，是本决定的已知限制而非缺陷）
- [ ] 已知悉本审批只授权 Task 5（NewAPI real GetUser），不授权 Sub2API、
      reqlog、DailyUsage、Key metadata、invoice、payment 中任何一项

## 决定

批准或驳回都必须回写本仓库才算正式决定（宪法第 19 条），且文本必须明确写出
事件名、目标平台、证据路径与哈希、允许的任务号（设计文档 §0）——下面这一段
填写后连同本文件一起提交，即是回写记录。格式参考
`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.2 的 `DAILY_USAGE_APPROVAL`/
`KEY_SCOPE_APPROVAL` 条目。

> NEWAPI_REAL_APPROVAL：`[批准/驳回]`——`[理由，须含证据目录路径与
> SHA256SUMS 摘要]`。授权 Task 5（NewAPI real GetUser），不构成对
> Sub2API/reqlog/DailyUsage/Key metadata/invoice/payment 的批准。

签字：

- 产品：________________________　日期：__________
- 安全：________________________　日期：__________

## 本审批如何生效

本审批不是代码里的一个开关——Task 5 的 real reader
（`connectors/platformusers/newapi_v2.go`）今天并不存在（同 Task 4，见
`docs/handoffs/slices/XM-USERS-V2-real-detail.md`）。本审批批准的是"允许开始
实现"：实现该文件的 PR 必须在提交信息或 Handoff 里引用本文件与上表的证据路径
及哈希，由人工合入 `release/v0.1-launch` 时核对一致后才能合入，详见
`docs/runbooks/USERS-REAL-APPROVAL.md`"审批如何生效"一节。
