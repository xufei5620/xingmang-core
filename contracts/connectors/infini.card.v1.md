# infini.card.v1（草案）——Infini 卡服务：开卡、卡面、资金与流水

> **状态：草案（DRAFT），已完成第一轮真实端点验证（2026-09-04，只读接口）。
> 签名口径、Date 格式、IP 白名单、`card_alias` 回显、时间格式、`mask` 掩码
> 形态六项已确认；余下四项要等第一次真实开卡（见文末验证清单）。**
>
> **验证打脸了文档一处**：时间字段是 Unix 秒时间戳**数字**，不是字符串。
> 第一版按字符串解析，一调就报 `cannot unmarshal number into ... string`。
> 那次失败是设计要的结果——宁可报错，也不默默落一个 1970 年的零值时间。

| 项 | 值 |
|---|---|
| 状态 | 草案（DRAFT），未冻结 |
| 覆盖平台 | `infini` |
| Base URL | `https://openapi.infini.money` |
| Connector 能力 | `infini.cards.read`、`infini.cards.write`、`infini.cards.reveal` |
| 实现 | `connectors/infini/`（`client.go`、`signing.go`、`envelope.go`、`endpoints.go`、`card.go`、`fake.go`） |
| 合规判据 | `connectors/infini/*_test.go` |
| 传输通道 | `connector.NewVendorWriteClient`（**不是**只读通道，见下） |
| 任务 | XM-CARD0（连接器与写通道第一片） |

## 这是仓库第一个含写操作的连接器契约

其余 14 份连接器契约都叫 `*.read.v*`，因为它们服务的是 NewAPI/Sub2API 这类
**平台不拥有其业务真相**的上游，走 ADR-018 的四道只读闸。

Infini 不属于那一类：平台是它的**客户**，调用的是它公开的写接口。宪法条款 5
禁止的是「直写第三方原始表」——那说的是数据库，不是供应商 API。

因此本连接器走并列的第二条通道 `VendorWriteTransport`，配对等的闸：

| 只读通道（ADR-018） | 供应商写通道（本契约） |
|---|---|
| 方法白名单 GET/HEAD | 方法白名单 GET/HEAD/**POST** |
| 主机 allowlist 完全相等 | 同左 |
| allowlist 为空则拒绝一切 | 同左 |
| 拒绝重定向 | 同左（写请求被 302 引走等于把带签名的请求体送给第三方） |
| 强制超时 | 同左 |
| 包内无写路径 | 写路径**必须**经 Action（领域层保证，非传输层） |

最后一行是两条通道最实质的差别：只读通道靠类型消灭写路径，写通道消灭不掉，
只能靠「所有写操作走 Action」这条上层纪律 + 审计。

## 认证

HMAC-SHA256，三个头：

```
Date:          RFC1123 GMT，服务端只容忍 ±300 秒偏差
Digest:        SHA-256={base64}，仅在有请求体时出现，**不参与签名**
Authorization: Signature keyId="{keyId}",algorithm="hmac-sha256",
               headers="@request-target date",signature="{base64}"
```

待签名串（末尾那个换行是必须的，少一个就是 401）：

```
{keyId}\n{METHOD} {path}\ndate: {GMT}\n
```

`path` 含查询串。请求体不参与签名——这一条反直觉，已由
`TestNewRequestBodyDoesNotAffectSignature` 钉住。

凭据经 CredentialRef 注入。**引用由账号 id 推出，不单独配置**：
`secret://infini-<账号id小写>/api-key-id` 与 `.../api-secret`。

**值在管理端「人员与权限 → 密钥引用」页填写与轮换**（`credential.secret.upsert`
/ `credential.secret.rotate` 两个既有 Action），那一页写进去的就是
SecretProvider 读的文件——不用登服务器手写，也不用改环境变量。

keyId 与 secret 分成两个引用是为了让它们能各自轮换。keyId 是公开半边
（每次请求都明文放在 `Authorization` 头里），secret 才是真正的秘密。

`card.create` 与 `card.reveal` 两项权限在上游侧**要求 IP 白名单**，平台出口 IP
必须固定。

## 能力与端点

| 能力 | 端点 | 方法 | 说明 |
|---|---|---|---|
| `infini.cards.read` | `/v2/cards/list` | GET | 支持 `status`、`card_alias`、`page`、`page_size` |
| `infini.cards.read` | `/v2/cards/status` | GET | 单卡状态，也是开卡异步流程的轮询入口 |
| `infini.cards.read` | `/v2/cards/transactions` | GET | 流水，分页 |
| `infini.cards.write` | `/v2/cards/apply` | POST | 开卡，**异步**：返回申请单，非可用卡 |
| `infini.cards.write` | `/v2/cards/top-up` | POST | 充值 |
| `infini.cards.write` | `/v2/cards/redeem` | POST | 赎回 |
| `infini.cards.write` | `/v2/cards/freeze` / `/unfreeze` | POST | 冻结与解冻 |
| `infini.cards.reveal` | `/v2/cards/reveal` | POST | 明文卡号、CVV、有效期 |

## 多账号（内部运营维度）

平台**同时管理两个 Infini 账号**（产品负责人 2026-09-04 拍板「两个都长期用，
并列管理」）。账号是**内部运营维度**：只体现在管理后端，以后开放外部用户时
不暴露给他们——那一侧的归属维度是投影表的 `owner_ref`。两者正交，一个回答
「这张卡的钱从哪个资金池出」，一个回答「这张卡归谁用」。

贯穿的位置：

| 层 | 体现 |
|---|---|
| 三张表 | 都有 `account` 列；卡与流水按 `(environment, account, …)` 唯一 |
| 操作台账 | 记 `account`；**但幂等键仍是环境内全局唯一**，不按账号分 |
| 限额 | 按账号各配一份——两个账号的资金是分开的 |
| 对账 | **只在操作自己的账号里按 alias 查**，绝不跨账号认卡 |
| Action | 六个都有必填的 `account` 参数，枚举由运行时配置生成 |
| 端点 | 列表可按 `account` 过滤，响应带 `accounts` 清单供表单填下拉 |

**没有默认账号，也没有单账号的隐式回落。** 未配置的账号一律报
`ErrUnknownAccount`：回落到「第一个账号」会把钱花到调用方没打算用的账号上，
而且事后从台账里看不出这是一次回落。

幂等键不按账号分的理由：键标识的是「哪一笔业务操作」，允许同键在两个账号上
各来一次等于让去重失效。

## 幂等语义（写操作必读）

**上游没有幂等键。** `/v2/cards/apply` 的请求参数里没有 `request_id`、
`client_order_id` 之类的去重字段。这意味着「请求发出去但没收到回复」时
**重试可能真的开出第二张卡、真的扣第二笔钱**。

本契约的应对：把平台自己的幂等键写进 `card_alias`，超时后用
`ListCards(Alias=...)` 精确匹配判定卡到底开没开成。

**这条路径依赖一个尚未验证的假设**：`/v2/cards/apply` 接受并回显
`card_alias`。文档把它列在卡片对象里、也列在列表接口的过滤参数里，但没有
明写申请时可以设置。若上游忽略该字段，幂等要退回「持卡人 + 时间窗 + 金额」
的模糊匹配，可靠性显著下降。**这是第一片真实验证的头号项目。**

其余写操作的幂等性：`freeze`/`unfreeze` 天然幂等（重复冻结不产生新效果）；
`top-up`/`redeem` **不幂等**，与开卡同等对待。

## 错误分类

| 上游情形 | `connector.ErrorKind` | 调用方处置 |
|---|---|---|
| HTTP 401/403 | `auth` | 查 IP 白名单与时钟偏差，**不要重试** |
| HTTP 429 | `rate_limited` | 退避重试 |
| HTTP 5xx、网络不可达 | `unavailable` | 退避重试；写操作先进不确定态 |
| 信封 `code != 0` | `rejected` | 上游收下了但拒绝，重试无意义 |
| `data.success == false` | `rejected` | 同上（信封成功不等于操作成功） |
| 非 JSON、超大响应 | `bad_response` | 告警，人工看 |
| **平台侧前置拒绝**（如批量超过 100 张） | `rejected` | 改**我们自己的**调用，与上游无关 |

### 上游原文的两层可见性（XM-CARD-VISIBILITY 修订）

- **上游的 HTTP 状态码、业务码与脱敏后的 `message`** 进 `connector.Error.Detail`，
  因而进对外错误文本、作业结果与结构化日志。
- **原始响应体**只进 `Unwrap` 链与服务端日志，不进对外文本。
- 脱敏规则见 `connectors/infini/redact.go`：像凭据的字段（token/secret/key/
  password/authorization/signature/digest/cvv…）**整个值去掉**；12~19 位数字串
  （允许组间空格或连字符）**打成 `****` + 末 4 位**。分隔符类含全角冒号与
  全角引号——本仓库的上游文案按约定用全角标点，只认 ASCII 冒号的掩码
  在这个仓库里已经漏过一次。

这是对此前写法的一次**有意放宽**，理由必须记下来：ADR-004 的铁律原文是
「第三方错误不得原样透传给**用户**」，而实现此前执行成了「不透传给**调用方**」
——严过 ADR 一档。代价是 2026-09-08 的告警风暴里，一条打了 1724 次的失败在
日志与后台上都只写着 `rejected: infini POST /v2/cards/status/batch`，三天答不出
为什么。放宽的边界有两条硬约束：对外文本只带**码 + 脱敏后的 message**，绝不带
原始响应体；给最终用户看的那一层仍由领域层翻成中文指引
（`internal/platform/cards/upstream_error.go`），一个字的上游原文都不到那里。

**平台侧前置拒绝要与上游拒绝可辨。** 两者的 `Kind` 都是 `rejected`、`Op` 也逐字
相同，此前对外文本一模一样，运维分不出是谁说的「不」。现在本地那一侧的 Detail
以「平台侧前置拒绝：」开头。

401 单独成类的实际理由：这条通道上 401 最可能的成因是 IP 白名单没生效或
本机时钟偏差超 ±300 秒，与「网络不通」的排查方向完全不同。

## 金额

卡余额与交易金额换算成**整数最小货币单位**，经 `money.CurrencyScale` +
`money.ParseMinorUnits`，全程不落 float（宪法条款 13）。币种不认识时报错，
**不猜 2 位**。

申请单的三个金额（`total_top_up_amount`、`total_fee`、`total_pay_amount`）与
充值/赎回返回的 `card_balance` **保持文本形态不换算**：它们的单位是 token
（USDT/USDC），而 `money.CurrencyScale` 只登记了法币。宁可不换算，也不猜
2 位或 6 位——猜错的那 10000 倍不会有任何症状。

## 敏感数据（口径已于 2026-09-04 变更）

`/v2/cards/reveal` 返回的明文卡号、CVV、有效期由 `RevealedCard` 承载。
该类型的 `String()` 与 `GoString()` 仍然返回打码文本：一次 `%v` 就足以把
卡号写进日志，而日志会被归档、会被搜索。**明文仍然不进日志、不进错误文本。**

**但明文现在落库**——产品负责人在看过三个选项与各自代价后明确选择
「明文直接落库」，换取「打开页面直接看到卡号」的使用体验（迁移 000027）。
原设计是只存掩码、明文只在 reveal 的一次性响应里出现。

由此产生的三个后果，记录在案：

1. **平台是持卡数据存储方。** 数据库备份、导出、DB 角色权限、故障 dump
   全部进入敏感范围——这些地方此前不含卡面数据。
2. **「谁在何时看了哪张卡」的审计链失效。** 卡号成为普通读字段后，
   看一眼不再经过 Action、不再产生审计记录。
3. **明文靠 reveal 拉取且只拉一次。** 上游列表接口只给 mask；同步作业在卡
   首次变成 active 时调一次 `/v2/cards/reveal` 存下来，之后不再调（卡号在
   卡的生命周期内不变）。这让 `card.reveal` 权限与 IP 白名单成了同步链路的
   硬依赖。

**唯一保留的闸**：读端点只把明文回给持有 `card.reveal` 权限的调用方，
只有 `card.read` 的仍然只看到 `mask`。表本身不加密——把密钥放在同一个系统里
的加密层只会给出虚假的安全感。

## 平台自有字段（上游一个都不知道）

投影表里有几列不来自上游，也永远不会被同步覆盖，它们回答的是上游答不出的问题：

| 列 | 含义 | 谁写 |
| --- | --- | --- |
| `owner_ref` | 这张卡归谁用 | 开卡时传入 |
| `user_email` | 开卡时指定的企业成员邮箱（上游只回 `user_id`） | 开卡时传入 |
| `bound_account` / `bound_account_kind` | 这张卡绑在哪个外部服务账号上，以及那个标识是邮箱/用户名/手机号 | `cards.card.usage.set` |
| `service_name` | 订的什么服务 | `cards.card.usage.set` |
| `next_renewal_on` | 下次续费日期（`date`，可空） | `cards.card.usage.set` |
| `usage_note` | 备注 | `cards.card.usage.set` |

三条约束：

1. **同步作业绝不能覆盖这些列。** UPSERT 里 `owner_ref` 与 `user_email` 用
   `COALESCE(NULLIF(...))` 保留原值，其余四列根本不在 UPSERT 的 SET 列表里。
   同步每 5 分钟一轮，一旦覆盖，运营刚填的登记最多活五分钟——有集成测试守着。
2. **续费日期由人填，不从流水推断。** 试用转正、年付转月付、涨价都会让推断
   悄悄错掉，而错了的提醒比没有提醒更糟：人会信它。
3. **续费风险由服务端判定**（`none` / `soon` / `unfunded` / `overdue`，
   `soon` 的窗口是 7 天），前端只翻译成文案。两处各算一遍迟早分叉，
   分叉的那一边会把「续不上」显示成正常。`unfunded`（续费日临近且卡上没钱）
   是这一列真正的用处——订阅掉了通常没人提前发现。

`bound_account_kind` 是显式枚举而不是从字面猜：有些服务的账号是用户名或手机号，
猜错会让运营按错的方式去找账号。

开卡表单的「企业成员邮箱」下拉，候选来自 `SELECT DISTINCT user_email`（按环境
隔离）。**上游没有成员列表接口**，所以候选只能是「平台自己开过卡用过的」——
第一次开卡时它是空的，因此用 `datalist` 而不是 `select`：不能把没用过的新成员
挡在外面。

## 产品 id 与费用（2026-09-04 查文档）

`POST /v2/cards/apply` 的 `product_id` 是整数，文档给出三个取值：

| product_id | 产品 |
| --- | --- |
| 1 | Infini Lite |
| 2 | Infini Pro |
| 102 | Infini AI |

**没有产品列表端点**——文档里不存在按接口查产品与定价的方式，这三个值只出现在
apply 的参数说明里。因此连接器不实现产品发现，`product_id` 由调用方传入；
这也意味着**上面这张表是硬编码的知识，会随上游改动而过期**，加新产品时只能
靠人去看文档。

费用：apply 的响应里带 `total_top_up_amount` / `total_fee` / `total_pay_amount`。
文档的示例是充值 100.00 收 1.00（1%），但**没有明确写费率，也没有写最低充值额**——
两者都待真实验证。注意费用是从 apply 的**响应**里才知道的，请求前无从预估，
所以「先小额试一张」是唯一能确定成本的办法。

充值额可以通过 `redeem` 拿回来，**手续费拿不回来**。这决定了第一次真实开卡的
风险敞口≈手续费，而不是充值额。

## 卡片状态取值（2026-09-05 实测定案）

权威枚举来自官方参考 infini-skill/references/CARDS.md：

`init` → `pending` → `active`；冻结后为 **`suspend`**（2026-09-05 对真实卡冻结实测，
回调与状态接口均返回 `suspend`）；`deleted`。

网页文档的示例里出现过 `pending_delete`，不在官方枚举里，也未实测到——
两处不一致，按官方枚举处理；页面对未知取值**原样显示并标记**，不静默归类。

## 回调（webhook，2026-09-05 生产实测定案）

Infini 后台的 Webhook 设置里确有卡片事件三种：`card.status_change`、
`card.transaction`、`card.challenge`。官方 skill 仓库的 WEBHOOKS.md 只写了订单与
订阅事件，**是它落后了**，以网页文档与后台为准。

实测到的 `card.status_change` 信封（逐字）：

```json
{"data":{"card":{"alias":"xm-e91ba7f121a23c63","card_id":"1bbf365c-…",
"currency":"USD","last_four":"9228","status":"suspend"}},
"event":"card.status_change","id":"44133c19-…","occurred_at":1788549962,"version":1}
```

- `alias` 原样回显——幂等信标在回调侧同样成立。
- 键按字母序输出（`data` / `event` / `id` / `occurred_at` / `version`）。
- 订单/订阅事件的信封是**扁平的、没有 `id` 字段**；事件 id 一律取请求头
  `X-Webhook-Event-Id`，载荷里的 `id` 只是卡片信封才有。

签名：`hex(HMAC-SHA256(secret, "{timestamp}.{event_id}.{payload}"))`，容忍窗口
300 秒——编码为 **hex** 已实测（错误分类能区分「不是 hex」与「不匹配」，
真实事件走的是后者）。密钥用 base64url 字母表（含 `_`），44 字符解出 32 字节，
**按原文字符串**做 HMAC 密钥，不解码。

回调只当触发器：验签 → 按事件 id 去重 → 定向重读该卡（`Syncer.RefreshCard`）→
标记完成。金额与状态一律以主动读取为准，不从载荷里写。处理失败回 5xx 让上游
按其策略重试（最多 8 次），且**只在成功后**标记完成，否则重试会被当成重复丢掉。

实测延迟：收到到处理完 < 1 秒（`received_at` 21:14:25.70 → `processed_at` 21:14:26.57）。

## 真实实例验证清单

第一轮（只读，零花费）已于 2026-09-04 完成，验证工具是
`connectors/infini/live_test.go`（`XM_INFINI_LIVE_CHECK=1` 才跑）。

**已确认：**

1. ✅ **签名口径** —— `GET /v2/cards/list` 返回 200。签名串、
   `headers="@request-target date"`、Date 的 RFC1123 GMT 三者都对。
2. ✅ **`card_alias` 被回显** —— 返回的卡都带 alias 字段，幂等对账的
   前提成立。（**开卡时能否设置**仍未验证，见下。）
3. ✅ **时间格式** —— 是 **Unix 秒时间戳数字**（如 `1786095722`），
   文档给的字符串示例不准。`rawTimestamp` 两种形态都认，
   零值与负数一律报错。
4. ✅ **`mask` 是掩码形态** —— 形如 `441357******7843`，不是完整卡号。
   投影表存它是安全的。
5. ✅ **IP 白名单** —— 绑定 IP 生效，直连出口通过。
6. ⚠️ **金额单位** —— `available_balance` 是十进制字符串（如 `"0.49"`），
   按 USD scale 2 换算成 49 分。**口径看起来正确，但仍需对着上游后台
   核一眼那张卡的真实余额**——这是唯一一个错了不会报错的字段。

**仍待验证（要等第一次真实开卡，会花钱）：**

7. ⬜ **`card_alias` 能否在 `/v2/cards/apply` 时设置** —— 整个幂等方案
   建立在这个假设上。若上游忽略该字段，对账要退回
   「持卡人 + 时间窗 + 金额」的模糊匹配。**这是最后一个高风险未知。**
8. ⬜ **申请单 `status` 的完整取值**与终态判定条件（异步开卡的轮询出口）。
9. ⬜ **冻结后的 `status` 取值**（文档未列）。
10. ⬜ **限流阈值** —— 文档未提及，用于定同步周期。
11. ⬜ **`POST /v2/cards/status/batch` 的 `card_ids` 到底是哪个 id 空间**
    —— OpenAPI 把它描述成「Internal ORGANIZATION_CARD primary key ids」
    （`contracts/connectors/infini/openapi/card.yaml`），而我们传的是
    `GET /v2/cards/list` 回来的 `id`。两者若不是同一个空间，批量查状态就会
    对每个账号稳定地失败，而单查照常成功——这与 2026-09-08 生产上观察到的
    形状一致（见 `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md`）。
    该端点要求的 `card.create` 权限我们**确实持有**，所以「权限不足」不是
    显然的解释。**这一条只是假设，不是结论**：XM-CARD-VISIBILITY 让上游的
    业务码与 message 进了日志与告警，第一次看到原话时按那句话判定，
    不要拿这条假设去反推。

## 已知的上游权限划分（2026-09-04 实测）

平台用的这把密钥授予的权限是：`order.create`、`order.query`、`payout.read`、
`fund.withdraw`、`card.reveal`、`payout.write`、`card.create`。

值得记一笔：**列表接口 `GET /v2/cards/list` 在没有单独「卡片读」权限的
情况下可以调通**——说明卡片读取要么不需要单独授权，要么被 `card.create`
隐含。别据此假设别的读接口也一样，`/v2/cards/transactions` 尚未实测。

## 破坏性变更

字段或签名口径变化必须发布 v2 并保留 v1 兼容期。
