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

供应商原始错误文本只进 `Unwrap` 链与服务端日志，不进对外错误文本（ADR-004）。

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

## 敏感数据

`/v2/cards/reveal` 返回的明文卡号、CVV、有效期由 `RevealedCard` 承载。
该类型的 `String()` 与 `GoString()` 都返回打码文本：一次 `%v` 就足以把卡号
写进日志，而日志会被归档、会被搜索。

**明文不落库、不进日志、不进审计正文**（宪法条款 7）。库里只存上游给的
`mask` 掩码卡号。审计只记「谁在何时 reveal 了哪张卡」。

## 卡片状态取值

`init`、`pending`、`active`、`pending_delete`、`deleted`。文档未列出冻结后的
状态取值——`freeze` 之后 `status` 变成什么**待真实验证**。

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

## 已知的上游权限划分（2026-09-04 实测）

平台用的这把密钥授予的权限是：`order.create`、`order.query`、`payout.read`、
`fund.withdraw`、`card.reveal`、`payout.write`、`card.create`。

值得记一笔：**列表接口 `GET /v2/cards/list` 在没有单独「卡片读」权限的
情况下可以调通**——说明卡片读取要么不需要单独授权，要么被 `card.create`
隐含。别据此假设别的读接口也一样，`/v2/cards/transactions` 尚未实测。

## 破坏性变更

字段或签名口径变化必须发布 v2 并保留 v1 兼容期。
