# 接码中心接入规范（XM-SMS4）

面向**平台内部的其他功能**（以后可能还有对外的转售前端）：怎么向接码中心要一个
号、怎么取码、怎么释放、超了配额会发生什么、出错时该怎么办。

依据：`docs/adr/ADR-022-接码中心.md`。契约文件在 `contracts/actions/sms.*.json`，
那里是权威；本文是把它们串成一条可执行的路径，并写清**为什么**是这样。

> 首个消费者尚未确定。这份规范与它描述的骨架都已经实现并有测试，但**没有任何
> 具体对接**——先定规范再等消费者，是因为幂等键、配额、回调这些东西一旦有了
> 消费者就改不动了。

## 0. 现在能做什么、不能做什么

| 能力 | 状态 |
|---|---|
| 要号 / 取码 / 释放的 Action 与领域逻辑 | 已实现，有测试 |
| 机器身份（SERVICE）调用这三个 Action | 已放开 |
| 消费者登记与日配额、花费止损线 | 已实现（后台「接入配额」页签） |
| 成本事件与余额对账 | 已实现（后台「成本统计」页签） |
| **生产环境的机器身份接入通道** | **还没有**，见下 |
| **回调投递** | **还没有**，字段已定稿，见第 9 节 |

**生产还没有机器身份的通道。** 生产用 OIDC（`XM_AUTH_MODE=oidc`），而现在的
解析器把每一个通过的身份都标成 `HUMAN`（`internal/platform/oidcauth/resolver.go`）。
开发与预发可以用 dev-header 解析器造一个 `SERVICE` 身份来联调（见第 2 节），
生产不行——那个解析器在生产会直接拒绝启动。

要在生产上真的接一个消费者，需要先决定机器身份走哪条路（OIDC client credentials
还是独立的 API Key），那是身份线的架构决定，不在接码中心这一侧。已写进
`docs/handoffs/slices/XM-SMS-CENTER-roadmap.md` 的「待决」。

## 1. 一次完整的接入长什么样

```
（一次性）产品负责人在后台「接入配额」登记你的 principal ID
    ↓
你的服务拿到身份 → 调 sms.number.request（带自己生成的 request_id）
    ↓
拿到 resource_id → 每 15 秒调一次 sms.code.fetch，直到 received=true
    ↓
用完 → 调 sms.resource.action（finish / cancel，只有 Hero 支持）
```

四步里只有第一步需要人。**第二步花真钱**，其余三步不花。

## 2. 身份与权限

- 身份类型必须是 `SERVICE`。`AI` 与 `SERVER_AGENT` **不允许**——让 AI 会话自己
  买号是另一件事，等产品负责人拍板。
- 需要的 scope：`sms.purchase`（要号）、`sms.read`（读清单）、`sms.reveal`
  （**看完整号码与验证码**）。没有 `sms.reveal` 时接口会照常返回，但号码与码
  这两个字段根本不出现——不是遮蔽，是不返回。
- 联调（开发 / 预发，`XM_AUTH_MODE=dev-header`）：

  ```
  X-Dev-Principal-ID: svc:your-service
  X-Dev-Principal-Type: SERVICE
  X-Dev-Scopes: sms.purchase,sms.read,sms.reveal
  ```

  这三个头**在生产无效**（解析器拒绝在生产装配），别把它们写进生产配置。

所有写操作都经 Action 内核：`POST /api/v1/actions/{action_id}/execute`，
请求体只有 `params`，响应带 `action_run_id`——**把它记进你自己的日志**，出事时
两边对账就靠它。

## 3. 登记：没登记就调不动

机器要号是花真钱，而且没有人在页面前面点确认——一个循环里的 bug 能在十分钟内
买光余额。所以：

- **没有配额行 = 一次都调不动**（错误 `PRECONDITION_FAILED`，文案「该消费者未
  登记配额」）。这与「新装环境两家供应商默认都是关的」是同一条纪律：天生不允许
  花钱，必须显式打开。
- 停用一个消费者立刻生效，不用等它自己停。
- 登记走 `sms.quota.set`，**只有人能配**：机器不该能给自己提额。

`consumer` 就是你的 principal ID，与审计里那一列同源——出事时「谁买的」要对得上。

## 4. 要号：`sms.number.request@1`

```json
{
  "request_id": "由你生成的稳定 ID",
  "service": "go",
  "country": "12",
  "quantity": 1,
  "provider": "hero_sms"
}
```

- `request_id` **由调用方生成**，重试必须带同一个。它是幂等键：同一个
  `request_id` 再来一次是**回放**——先查台账，试过的供应商不再打上游，成功的回
  同一批号码。回放不重复计配额。
- `service` / `country` 是平台无关的描述，由接码中心翻译成各家的入参。`country`
  必须具体，不接受 `*`。`quantity` 1–200。
- `provider` 可选。**不填才是常态**：供应商由路由规则选（后台「路由规则」页签），
  第一家明确失败就回落下一家，**每家最多试一次**。填了就只试那一家、不回落。

返回：

```json
{
  "state": "succeeded",
  "provider": "hero_sms",
  "operation_id": "…",
  "resource_ids": ["…"],
  "rule_id": "…",
  "needs_review": false,
  "attempts": [
    {"provider": "sms62", "state": "failed", "reason": "62 没有匹配「go / 12」的商品", "replayed": false},
    {"provider": "hero_sms", "state": "succeeded", "provider_ref": "…", "replayed": false}
  ]
}
```

`state` 只有三种：

| state | 含义 | 你该做什么 |
|---|---|---|
| `succeeded` | 拿到号了 | 用 `resource_ids` 去取码 |
| `failed` | 全部供应商都明确失败，**没花钱** | 看 `attempts[].reason`；改规则 / 充值 / 换国家之后**用新的 request_id** 再要 |
| `unknown` | **钱可能已经花了** | 别重试。`needs_review=true`，等人到后台核对（`sms.operation.resolve`）；核对完你带同一个 `request_id` 重放就能拿到结果 |

`unknown` 之后不再试下一家，是刻意的：再试就是双倍花钱。

**响应里没有号码**。号码与验证码走 `GET /api/v1/sms/resources`（受 `sms.reveal`
把守），不进 Action 结果——Action 结果会被广泛展示与留存。

## 5. 取码：`sms.code.fetch@1`

```json
{"resource_id": "…"}
```

返回 `{"received": false}` 或 `{"received": true, "code_id": "…", "code": "…"}`。

- **「还没有码」是正常状态，不是错误**：返回 200 与 `received=false`，不要把它
  当失败重试到告警里去。
- 轮询节奏 **15 秒**一次。后台页面就是这个节奏；更快对上游没有意义（短信本身
  也要几秒到几十秒），只会让两边都更容易撞限流。
- 取到码会把号码的统一状态推进到 `code_received`（见第 7 节）。
- 取码**不计入日配额**：它不花钱，把轮询算进配额等于让配额随轮询次数消耗。
- `code` 字段只在你有 `sms.reveal` 时出现。

## 6. 释放与生命周期：`sms.resource.action@1`

```json
{"operation_id": "由你生成", "kind": "finish", "resource_id": "…"}
```

`kind`：`cancel`（取消，Hero 会退款）、`finish`（用完了）、`replace`（换号）、
`prolong` / `reactivate`（延长 / 重激活，**花钱**）。

- **只有 Hero 支持**。62 的号买到就是买到了，没有取消或完成的接口——对 62 调用
  会返回 `PRECONDITION_FAILED` 而不是假装成功。用 `GET /api/v1/sms/providers`
  的 `capabilities` 判断，别按供应商名字硬编码。
- 用完请调 `finish`：它让统计与状态干净，也让后台不再把这个号算进「待收码」。
- `operation_id` 同样是幂等键，重试带同一个。

## 7. 号码的状态机

上游的原始状态两家完全不同（Hero 是 1/2/3/4/6/7/8/10，62 只有一句「正常」），
所以接码中心给出**统一状态**，原话保留在另一列供排查：

```
waiting_code ──取到码──> code_received ──finish──> finished
     │                        │
     ├──cancel──> cancelled <─┘
     └──过了到期时间──> expired（由本地时钟判，两家都不推送过期）
```

读 `GET /api/v1/sms/resources` 时看 `effective_state`：它把「待收码但已经过了
到期时间」算成 `expired`。`state` 是库里存的值，`status` 是上游原话。

普通激活号 20 分钟就过期是常态；租用号按小时计费、可延长，到期前一小时后台会
亮一条内部提醒（不外发）。

## 8. 配额与花费上限

- **日号数**按号数算，不是调用次数：一次要 50 个与 50 次要一个，花的钱一样多。
  超了返回 `PRECONDITION_FAILED`（文案带「今天已要 N 个号，日配额 M」）。
- 失败的请求不计（没花钱）；回放不重复计数。
- **花费上限是止损线，不是预授权**：买之前没人知道这次要花多少（62 连买完都只
  回订单号），所以语义是「今天已经花到上限就不再放行」，最多超出一次请求。
- 上限**按币种各自计**。两家的币种不同且全平台不折算，一个跨币种的总额上限是个
  算不出来的数。
- 配额窗口按 **UTC 自然日**。

## 9. 回调（字段已定稿，**投递尚未实现**）

投递方式（HTTP Webhook 的签名、重试、退避、死信）等另一条线的通知规范定稿后
再适配——现在自己发明一套，等规范来了就是两套。**字段先定死**，这样规范一到
只需要接投递，消费者不用改解析。

```json
{
  "event_id": "uuid，同一事件重投时不变",
  "event_type": "sms.code.received",
  "occurred_at": "2026-09-06T07:00:00Z",
  "environment": "production",
  "request_id": "你当初生成的那个",
  "resource_id": "…",
  "provider": "hero_sms",
  "phone_mask": "1555****1111",
  "state": "code_received"
}
```

事件类型（定稿三种）：`sms.code.received`、`sms.number.expired`、
`sms.request.settled`（要号最终落定，带 `state` 与 `needs_review`）。

**回调里不带验证码，也不带完整号码。** 码与卡面明文同档，把它推到一个 URL 上
就等于把权限闸绕开了；消费者收到回调后用 `sms.code.fetch` 自取——那条路上有
`sms.reveal` 把守，谁取的也留痕。

在投递做出来之前，**轮询是唯一的方式**，而且它已经够用：15 秒一次的取码本来
就是这套东西的常态节奏。

## 10. 错误码

Action 内核的错误码是稳定的，按它分支，别按文案：

| code | 含义 | 你该做什么 |
|---|---|---|
| `INVALID_PARAMS` | 参数不合法（国家写了 `*`、数量越界、日期格式错） | 修参数；重试同样的参数没有意义 |
| `PERMISSION_DENIED` | scope 不够，或身份不合法 | 找人加 scope |
| `PRINCIPAL_TYPE_NOT_ALLOWED` | 用了 `AI` / `SERVER_AGENT` 身份 | 换 `SERVICE` |
| `PRECONDITION_FAILED` | 未登记 / 超配额 / 这家不支持这个动作 / 资源不存在 | 看文案；配额类的等明天或找人提额 |
| `CONFLICT` | 同一个 `request_id` 的要号还在进行中 | 等一会儿用**同一个** `request_id` 重试 |
| `EXECUTION_FAILED` | 上游或内部失败 | 可以用同一个 `request_id` 重试；反复失败就去看后台的操作台账 |
| `ENVIRONMENT_MISMATCH` | 身份的环境与这个进程不符 | 配置问题，找运维 |

**没有「超时」这个错误码。** 上游超时会落成 `unknown`（见第 4 节），那是一个
需要人核对的状态，不是一个可以自动重试的错误。

## 11. 成本

每一次成功的花费都会在成功那一刻记一条成本事件（买号、租用、延长、重激活、
买邮箱、重下单；取消的退款记负数），带供应商、币种、服务、国家与操作 ID。
消费者自己看不到这些——成本在后台「成本统计」页签，按供应商 × 币种 × 服务 × 天
聚合，可导出 CSV，形状就是跨平台财务的输入。

有一类金额上游根本不给（62 买号只回订单号，Hero 的延长不回价格），这些行的金额
是**空**而不是 0，统计里单独计数并写明「合计是下限」。

## 12. 版本与变更

- Action 都是 `@1`。参数是白名单：**多传一个字段会被拒绝**（`INVALID_PARAMS`），
  这是刻意的——参数偷渡是权限绕过的常见入口。
- 破坏性变更会发一个新版本号（`@2`），老版本保留到消费者迁移完。
- 契约文件（`contracts/actions/sms.*.json`）里的 `notes` 是每个决定的理由，
  与本文重复的地方以契约为准。
