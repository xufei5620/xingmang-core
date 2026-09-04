# XM-CARD5：卡片管理对照 Infini 全量文档的补全计划

> 状态：PLAN（待另一模型实现）。写于 2026-09-05，作者 Claude（本会话）。
> 读者：下一位实现者。本文自成一体，不依赖本会话的对话记录。

## 0. 先读什么

1. `contracts/connectors/infini.card.v1.md` —— 已实测定案的事实（签名、信封、状态值、alias 回显）。
2. `contracts/connectors/infini/openapi/{card,fund,acquiring,payout}.yaml` —— 官方 OpenAPI 原文（2026-09-05 抓取自 `developer.infini.money/_bundle/@l10n/zh/apis/*.yaml`），**字段与枚举以它为准**。
3. `docs/handoffs/slices/XM-CARD0-infini-connector.md` —— 已上线部分的 handoff，含八条实现期教训。
4. 本文附录 A–D：枚举、错误码、Webhook 载荷、文档间矛盾。

## 1. 现状（已上线生产，release `2653f42`）

| 层 | 已有 |
|---|---|
| 连接器 `connectors/infini/` | 11 个卡端点中 10 个已实现（`delete` 未接）、`status/batch`、`funds/balances`；HMAC 签名实测正确；错误分类含 `auth` / `ip_not_allowed` |
| 领域 `internal/platform/cards/` | 开卡/充值/赎回/冻结/解冻/reveal/用途登记 7 个 Action（全 L1、HUMAN）；幂等台账 + `unknown` 态对账；周期同步（300s）；回调（签名 hex、去重落库、定向刷新） |
| 存储 | 迁移 000026–000030；卡投影（含明文 PAN/CVV/有效期、用途登记、开卡费列已建未写）；流水投影（外币三列已建未写）；回调事件表 |
| 端点 | `/api/v1/cards`、`/cards/{id}/transactions`、`/cards/operations/attention`；公网 `/webhooks/infini/{account}` |
| 页面 | 卡片管理表（账号/卡号明文/持卡人/状态中文/余额/用途/绑定账号/续费/新鲜度/操作列 详情·充值·赎回·锁定|解锁）；详情弹窗 5 页签 |
| 配置 | `XM_CARDS_MODE=real`、两账号 CHRIS/LINFENG、上限 `unlimited`、按账号端点覆盖；凭据走管理端「设置 → 密钥引用」 |

**已实测定案**（不要再验）：签名串格式；回调编码 hex、窗口 300s、密钥按原文做 HMAC；`card.status_change` 信封形状；冻结态 `suspend`；`alias` 在列表与回调里原样回显；开卡费 1 USD 固定；余额 minor units 与控制台一致；回调收到到处理完 < 1s。

## 2. 对照文档逐条核对后的差距（本计划要做的全部）

编号 = 实施顺序。每项含：做什么 / 依据 / 验收 / 测试。

### P1 开卡后当场拉卡面明文
- **做**：`Service.IssueCard` 在 `UpsertCard` 之后，若 `card.Status == "active"` 则 `RevealCard` + `StoreCardSecrets`（best-effort，失败不影响开卡结果，周期同步兜底）。`Store` 接口加 `StoreCardSecrets`（`memStore` 已有）。
- **依据**：card-api `reveal` 需 `active`；实测开卡后立即 `active`，此前要等 3.5 分钟。
- **验收**：开卡成功后列表立刻有卡号。
- **测试**：`issue_test.go` 用 fake 断言开卡后 store 里有 PAN；reveal 失败时开卡仍成功。

### P2 开卡费与实付落库 + 成本列
- **做**：`CardAttribution` 加 `IssueFee`、`IssuePayAmount`（文本）；`IssueCard` 从 `CardApplication.TotalFee/TotalPayAmount` 传入；`PgStore.UpsertCard` 写 `issue_fee_text`/`issue_pay_amount_text`（`COALESCE(NULLIF)` 保留原值）；`CardView`/端点/页面加「开卡费」列。
- **依据**：apply 响应 `total_fee`/`total_pay_amount`（card.yaml `ApplyCardResponse`）；实测 1 USD 固定。
- **测试**：PgStore 集成测试（写入 + 同步不覆盖）；端点测试字段出现。

### P3 两账号可用余额
- **做**：新只读端点 `GET /api/v1/cards/balances` → 对每个账号调 `AccountBalances`（连接器已实现），逐账号返回 `{account, usdt, usdc, usd, error?}`；单账号失败不 500。页面顶部一条余额带。
- **依据**：fund-api `GET /v2/funds/balances`，需 `fund.withdraw`（两把生产密钥都有）。
- **注意**：这是**实时上游调用**不是投影；页面按需刷新，不进周期同步。权限 `card.read`。
- **测试**：httpapi 用 fake 客户端；一个账号报错另一个仍返回。

### P4 同步作业改批量状态
- **做**：`Syncer.refreshTrackedCards` 按账号分组、每 100 张一次 `BatchCardStatus`（连接器已实现），只对**状态变化**的卡再调 `CardStatus` 取余额等全量字段（批量接口只回 `card_id`+`status`）。
- **依据**：card.yaml `/status/batch`，1–100 张。
- **测试**：`sync_test.go` 用 fake 断言 250 张卡 → 3 次批量调用；状态没变的不再单查。

### P5 流水补三字段 + 时间戳修复
- **做**：连接器已解析 `TransactionAmount/TransactionCurrency/SettledAt`（b4625c8）；`PgStore.UpsertTransactions` 写三列（`transaction_amount_text`、`transaction_currency`、`settled_at`），`ListTransactions`/端点/详情页签显示「原始金额（币种）」「结算时间」。
- **依据**：card.yaml `CardTransactionItem`；`settled_at` 为 0 表示未结算。
- **注意**：REST 流水**没有交易 id**（去重键仍是派生的），而 webhook 的 `card.transaction` **有** `transaction_id`——见 P8。
- **测试**：PgStore 集成测试三列往返；`settled_at=0` 存 NULL。

### P6 CVV / 有效期列
- **做**：表格加两列（默认显示，随 `card.reveal` 权限门控，与卡号同一逻辑）；标签写「有效期」不写 MMYY。
- **依据**：reveal 返回 `expiration_mmyy`，文档示例 `1228`，**实测 `11/2031`**（附录 D）。原样显示，不解析。
- **决定记录**：产品负责人 2026-09-05 拍板 CVV 落库，**明知**官方 CARDS.md 写有「Do not persist CVV」——已记入契约。

### P7 删除卡（关停）
- **做**：连接器加 `DeleteCard(ctx, id)` → `POST /v2/cards/delete`，**请求体字段是 `card_id`**（其余端点都是 `id`，唯一例外）；新 Action `cards.card.delete`（L1，`card.manage`，幂等键必填）；状态机接受 `pending_delete → deleted`；页面「关停」按钮 + 二次确认（不可逆）。
- **依据**：card.yaml `/delete`：异步，先 `pending_delete` 再结清余额到 `deleted`。
- **测试**：Action 走真内核 + 审计；fake 状态流转；页面二次确认。

### P8 `card.transaction` 回调的完整语义
- **做**：`ParseWebhookEvent` 解析 `data.transaction_id`、`related_transaction_id`、`type`、`status`、`direction`；回调仍**只触发重读**（不从载荷写金额），但把 `transaction_id` 存进 `webhook_event` 表以便对照。
- **依据**：附录 C。REST 与 webhook 的 type/status **大小写不同**（`Consume/Completed` vs `consume/completed`），匹配时统一小写。
- **测试**：五种示例载荷（authorized / completed+adjustment / failed / reversal / refund）都能解析且触发刷新。

### P9 `card.challenge`：3DS 验证码
- **做**：新表 `cards.card_challenge`（account, card_id, challenge_id UNIQUE, type, **code**, expires_at, received_at）；回调 `card.challenge` 落库；只读端点 `GET /api/v1/cards/{id}/challenges?active=1`；卡片行在有未过期挑战时显示「验证码 123456（3 分钟内有效）」徽章。
- **依据**：附录 C `card.challenge` 载荷含 `"challenge": "123456"`（网页文档）。**注意**：2026-09-05 生产收到的真实 challenge 事件（另一张卡）在后台展示里**没看到 `challenge` 字段**——第一条真实事件到达时先打日志确认字段存在再做页面；若不存在，此项降级为「有挑战待处理」提示。
- **安全**：验证码是敏感数据，按 `card.reveal` 权限门控，过期即不返回；不进审计正文。

### P10 状态枚举与页面
- 权威枚举 6 个：`init / pending / active / suspend / pending_delete / deleted`（card.yaml `/list` 描述）。`src/lib/cardStatus.ts` 加 `pending_delete → 删除中`；未知值仍原样+「未知状态」。

### P11 错误码到用户可读文案
- **做**：连接器解析应用层信封 `code`（非 0），把已知码映射成分类与中文（附录 B）；Action 失败时前端显示分类文案而不是 `EXECUTION_FAILED`。
- **依据**：附录 B；网关拒绝 `{"message":"client request can't be validated"}` 与 `{"message":"ip not in whitelist"}` 已分类。
- **注意**：**卡 API 没有专属错误码表**（8-errorcodes 只有收单/提现/付款三节；card.yaml 无非 200 响应定义）。卡端点的业务失败以 `code`+`message` 原文分类，未知码归 `rejected`。

### P12 页面其余对齐
- 列表 `status` 筛选走上游多值（`status=active,pending`），减少本地过滤。
- 交易页签显示 `type/status` 小写归一后的中文（消费/充值/赎回/退款/冲正/清算；授权中/已完成/失败）。
- 「数据新鲜度」在回调通了之后仍保留——它回答的是「上次谁刷新的」。

### 不做（有据）
- 沙箱账号（产品负责人：真实账号已通；无测试币发放机制，附录 D）。
- 收单/订阅/法币付款/提现（与卡片管理无关；已读文档，附录 B 保留其错误码以备将来）。
- 自动退款（仅收单业务）。

## 3. 部署一次性上线的顺序
P1 → P2 → P5 → P4 → P3 → P6 → P10 → P8 → P9 → P7 → P11 → P12。每项独立提交、带 `Acceptance-Line: claude` trailer、过全部门禁（`go test -p 1 ./...` 需 `XM_TEST_DATABASE_URL`，`bash scripts/check-governance.sh`，admin-web/ui-admin `tsc` + `vitest`）。合并后一次部署：`deploy-local.sh --override-file .../server-prod.yaml`。

## 4. 硬约束（宪法 + 本片已定纪律）
- 所有写走 Action；回调只触发读，绝不从载荷写金额。
- 凭据只经 CredentialRef；`SecretValue` 取值**只能 `Reveal()`**（`String()` 恒返回 `[REDACTED]`，2026-09-05 为此绕了三轮）。
- 金额禁 float；token 金额保持十进制文本；minor units 只用于有已验证标度的币种。
- 新 Action 一律 L1（L2+ 内核拒执行）；权限串必须加进 `DefaultRoleScopeMap`（曾漏过）。
- 新周期任务必须登记 JobManifest 四处（代码注册表、`contracts/jobs/cluster-jobs.v1.json`、`effectiveJobConfig`、`deployed_schedule`）——曾漏过导致 worker 崩溃。
- 新增 compose 变量要过 `scripts/check-compose.py`；配置值从 `.env.example` 逐字抄。
- 新表加进 `store_pg_integration_test.go` 的 TRUNCATE 名单。

## 附录 A：枚举（以 OpenAPI 为准）
- 卡状态：`init, pending, active, suspend, pending_delete, deleted`
- product_id：`1` Lite / `2` Pro / `102` AI
- token_type：`USDT, USDC`（资金 API 另有 `USD`）
- REST 流水 type/status 示例：`Consume` / `Completed`（未穷举）
- Webhook 流水 type：`consume, reversal, refund`；status：`authorized, completed, failed`；direction：`debit, credit, release, none`
- 提现状态：`pending, processing, completed, failed`；付款：`processing, completed, failed`

## 附录 B：错误码（8-errorcodes + OpenAPI）
- 信封：成功 `{"code":0,"message":"","data":{}}`；应用错误 `{"code":N,"message":"...","data":null}`；网关拒绝 `{"message":"client request can't be validated"}`（401）/ `{"message":"ip not in whitelist"}`（403）。**部分业务错误以 HTTP 200 + 非零 code 返回**，客户端必须先看 HTTP 再看 code。
- 通用：`401` 签名/时钟（±300s）；`403` 权限或 IP；`404` 不存在；`500` 内部。
- 收单（40001–40013 / 40401–40402 / 40901–40912 / 46001）；提现（30001, 30002, 30003, 30005, 30007, 30012, 30013, 30022, 30023, 30034, 80016）；内部转账另有 10005, 10030, 20012, 30015；付款（108001–108015，带 `data.error` 与 `data.retryable`）。完整表见 `docs/…/8-errorcodes` 与 `openapi/*.yaml`。
- **卡 API 无专属错误码表**。
- 限流：600 次/分钟/API Key（10-security）。

## 附录 C：Webhook
- 事件：订单 5 种 + `payment.failed`；自动退款 2 种；订阅 2 种；卡片 `card.status_change / card.transaction / card.challenge`。
- 头：`X-Webhook-Signature`（hex HMAC-SHA256）、`X-Webhook-Timestamp`（Unix 秒）、`X-Webhook-Event-Id`、`X-Webhook-Signature-Version: v1`。签名内容 `{timestamp}.{event_id}.{payload}`，用原始请求体。
- 重试：立即、30s×3、60、120、240、480，共 8 次；非 200 即重试。
- 卡片信封：`{id, event, version:1, occurred_at, data:{card:{card_id, alias, last_four, status, currency}, ...}}`；订单/订阅是扁平信封无 `id` → 事件 id 取请求头。
- `card.transaction` 载荷字段：`transaction_id, related_transaction_id, type, status, amount, fee, transaction_amount, currency, transaction_currency, direction, merchant{name}, transaction_at, settled_at, auth_settle_adjustment{authorized_amount, settled_amount, signed_delta, direction, balance_before/after…}, failure{reason}`。同一 `transaction_id` 会多次投递（authorized → completed）。
- `card.challenge`：`challenge_id, challenge_type (authorization_code), challenge (验证码), expires_at`。

## 附录 D：文档与实测/文档间矛盾（实现时以左列为准）
| 以此为准 | 矛盾来源 |
|---|---|
| 冻结态 `suspend`（实测 + OpenAPI） | 网页 card-api 状态段漏了 `suspend` |
| `pending_delete` 存在（OpenAPI + delete 端点） | skill 仓库枚举漏了它 |
| 卡片事件存在（后台 + 网页 webhook 文档） | skill 仓库 WEBHOOKS.md 只有订单/订阅 |
| `expiration_mmyy` 实测 `11/2031` | 文档示例 `1228` |
| 时间戳是 Unix 秒**数字** | 文档示例部分写字符串 |
| `/delete` 请求字段 `card_id` | 其余端点均为 `id`；skill「标识规则」说一律 `id` |
| 开卡费 1 USD 固定（实测） | 文档示例 100→1.00 看似 1% |
| 沙箱：`business-sandbox` / `openapi-sandbox`；无测试币发放机制 | 11-supported-chains 只有链上测试币（Tron Shasta）与收单测试卡 `4000000000000085`，与开卡无关 |
