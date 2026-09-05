# XM-SMS1：按两家官方文档补齐全部接口

status: done
branch: ai/claude/XM-CARD0-infini-connector
上游文档：
- 62-US：https://api.62-us.com/api-docs（HTML；`/api-docs/openapi.json` 只是 747 字节的
  路径索引，参数与错误码在 HTML 里；2026-09-06 抓取）
- Hero-SMS：https://hero-sms.com/docs/v1/openapi.json?locale=en（OpenAPI 3.1，50 个操作、
  58 个 schema；产品负责人另下载了 `api-cn.json`，与英文版逐操作一致；2026-09-06 抓取）

产品负责人 2026-09-06 要求：「两个接码商家都实现完整的功能，所有的接口都看看，
然后实现所有的功能」。

---

## 一、与 XM-SMS0 的差异清单（对照官方文档得出）

### 62-US（7 个接口）

| 接口 | SMS0 | SMS1 |
|---|---|---|
| `GET /api/v1/info` | 读 `ip` | **改读 `client_ip`**（官方字段名；此前真上游会回空 IP，而空 IP 看起来只是「这家没给」） |
| `GET /api/v1/goods` | 有 | 不变 |
| `GET /api/v1/goods/detail` | 无 | **新增**（两段 ID：平台-国家） |
| `POST /api/v1/get` | 有 | 不变；注释更正——三段 ID 官方语义是**平台-国家-天数**，1–200 现在是官方上限 |
| `GET /api/v1/msg` | 有 | 不变；limit 默认 5 最大 20 是官方值 |
| `GET /api/v1/order/tokens` | 有 | 不变（官方唯一给了响应结构的接口，与实现一致） |
| `GET /api/v1/orders` | 无 | **新增**（page / page_size ≤ 100） |
| 错误码 | 无映射 | **官方 15 个错误码翻成指路的中文**（40304 去加 IP 白名单、42204 去充值……） |

官方只给了 `order/tokens` 的响应结构，其余接口只有参数。所以新增两个接口的
响应按宽容方式解析（取得到就有，取不到就空），**商品 ID 与订单 ID 两个键钉死**。

### Hero-SMS（现代 REST 24 个 + 兼容层 25 个）

现代 REST：SMS0 有 12 个（购买、列表、反查、最近 OTP、取消/完成/换号/重激活/延长
及两个档位读取），SMS1 补齐其余 12 个：OTP 列表、历史、延长历史、非标准时长目录、
统计、`call` 类型的 offers 与购买（官方 `verificationType` 枚举）、收藏（2）、
邮箱接码（7）。

兼容层（`/stubs/handler_api.php?action=`，给老客户软件用的 SMS-Activate 协议）：
**只接现代层没有的 9 个**——余额、国家、服务、运营商、价格、Top 国家（含按等级）、
租用报价（按服务 / 按国家）、租用下单。与现代层重复的 12 个（getNumber、setStatus、
getStatus、getActiveActivations、getHistory、reactivate/prolong 及其 options/history、
finishActivation、cancelActivation、getAllSms）**刻意不接**：同一功能两套实现只会多
一处漂移。

兼容层两处与现代层不同，都写在 `doLegacy`：密钥走 **query 的 `api_key`**（官方
`apiKeyQuery`，会进上游访问日志；本仓的日志与错误文本一律不带 URL）；部分响应是
**裸字符串**（`ACCESS_BALANCE:12.50`），不是 JSON。

官方 schema 印证了 SMS0 按冻结源码改的两处是对的：OTP 是 `smsCode / smsText /
phoneFrom / receivedAt`，时长是 `{unit, value}`。

---

## 二、领域层的形状

- **不扩 `Adapter` 接口**。那个接口只装七态推进要用的主链路；把二十几个单边方法
  塞进去，会让 62 的适配器背上一堆只能返回「不支持」的空实现——而那种空实现最
  容易在页面上变成一个灰按钮。改为两个能力接口 `HeroExtras` / `SMS62Extras`，
  `Service.Hero()` / `SMS62()` 取，装错供应商得 `ErrExtrasNotSupported`。
  两个入口都要求这家**开着且验证过**：这些调用都会真打上游。
- **花钱的三条新链走七态台账**：租用、买邮箱、邮箱重下单。抽了一个 `runLedger`
  ——SMS0 里 Purchase 与 ExecuteAction 各抄一遍的那段纪律，这次多了三条链，每条
  各抄一遍迟早有一条漏掉「落库失败要落 unknown」那一行。
- **批量买邮箱要补读**：官方批量响应里没有每条的 id，要再读一次列表按地址找回来，
  与 62 买号「买完再读一次」同一个形态；补读缺任何一条都落 unknown——钱已经花了。
- 邮箱与手机号是**两种资源**：单开 `sms.sms_email`（迁移 000040）。塞进
  `sms_resource` 会让那张表一半列对手机号没意义、另一半对邮箱没意义。
- 号码资源补五个官方字段（operator / price / verificationType / subtype /
  countryPhoneCode）。**subtype 决定这个号是 20 分钟的激活号还是按小时的租用号**——
  页面上不区分，人会拿一个快过期的激活号去做需要几小时的注册。
- `sms_operation.kind` 的 CHECK 重建，多六种。花钱的列进 CHECK 而不是放开约束：
  一个没在清单里的 kind 意味着有人绕过了台账。

## 三、Action（全部 L1）

| Action | 权限 | 花钱 |
|---|---|---|
| `sms.rent.purchase@1` | sms.purchase | 是（按小时） |
| `sms.email.purchase@1` | sms.purchase | 是 |
| `sms.email.action@1`（cancel / reorder） | sms.manage | reorder 可能 |
| `sms.favorite.set@1` / `sms.favorite.remove@1` | sms.manage | 否 |

`sms.number.purchase@1` 多了 `verification_type`（sms / call）与 `fixed_price`。
call 是语音验证、另一种计费——做成**显式参数**，由页面上一个明确的选项决定，
而不是默认值悄悄决定。

## 四、读端点（全部实时上游，按供应商分路径）

`/sms/hero/{balance, countries, services, operators, prices, top-countries,
custom-durations, rent-offers, rent-count, history, stats, email-domains}`、
`/sms/resources/{id}/{upstream-codes, extend-options, prolong-history}`、
`/sms/emails`、`/sms/emails/{id}?refresh=1`、`/sms/sms62/goods/{id}`、`/sms/sms62/orders`。

共用路径会让页面在 62 上请求一个只有 Hero 有的东西，然后拿到一个含糊的错误。
邮箱的 value 与上游验证码列表里的码由 `sms.reveal` 把守，与本地验证码同一条闸。

## 五、页面

「接码」页分六个页签：号码（原整页）/ 目录与价格（Hero）/ 历史与统计（Hero）/
邮箱接码（Hero）/ 租用（Hero）/ 订单与商品（62）。号码详情里多了上游验证码列表、
延长/重激活（先读档位再确认——它们花钱，不能是一个裸按钮；SMS0 时后端有这两个
动作但页面上没入口）、延长历史、租用标记。Hero 的供应商卡显示余额（币种上游没说，
**不补一个 USD**）。

## 六、验证

- 两家连接器：夹具字段名**逐字来自官方 schema**（`connectors/*/official_test.go`）。
- 领域：租用 / 买邮箱 / 邮箱动作的七态与未决防重（`extras_test.go`）；
  真库集成测试覆盖迁移 000040 的三处结构变化（`store_pg_integration_test.go`）。
- 前端：延长必须先选档位、无 reveal 权限不显示空码、租用小时数为正才能下一步、
  邮箱刷新真的带 refresh 打上游（`SMSExtrasPanels.test.tsx`）。
- 门禁：go vet / go test -p 1 ./... / check-governance / pnpm typecheck / pnpm test 全绿。

## 七、仍然未验证的部分（诚实的边界）

**没有打过一次真实上游。** 62 除 `order/tokens` 外的响应形状官方没给；Hero 的
schema 是官方的，但 `stats` 的格子字段（count / sum）官方也没钉，按常见命名取，
原始对象一并带回页面。第一次连接测试与第一次目录读取会告诉我们哪些猜错了——
那些错都是「显示为空」而不是「报错」，所以上线后第一天要有人对着两家后台核一遍
页面上的数。


---

## 八、上线当天的补正（2026-09-06，第一次真打上游之后）

产品负责人在生产上点了两家的连接测试与两个新页签，暴露出四件事：

1. **取码这条链后端有、入口没有。** `Service.FetchCode` 从 SMS0 起就存在，但没有
   任何 Action 或端点在调它——页面上的「验证码」只读本地库，不管号是谁买的，
   永远是「还没收到码」。产品负责人问「那我们购买的号码接码问题呢」才发现。
   补 `sms.code.fetch@1`（sms.read）：人发起；号码详情打开且还没有码时页面每
   15 秒自动取一次、最多十分钟，也可以手动点。「还没有码」是正常状态，Action
   成功、received=false。
2. **在供应商后台下的单进不了平台。** 62 后台里有五个订单（一单 50 个号），
   而号码页只装通过平台买的号。补 `sms.order.import@1`（sms.manage，只读不购买）：
   62 传订单 ID、Hero 传 activation ID；「订单与商品（62）」每行一个「导入到平台」。
3. **Hero 全量价格表超过连接器 1 MiB 的响应体上限**，被我们自己拒成「协议错误」，
   页面显示「服务内部错误」。兼容层上限改为 16 MiB（仍是上限，只是给目录类接口留够）。
4. **Hero custom-durations 真实响应外面多包了一层 data**（官方 schema 没写）——
   页面上一列 data、一列服务名、一列 0。两种形状都认。

另外 62 的订单列表证实了猜字段名的代价：订单 ID / 数量 / 金额 / 状态数字对了，
「商品」「状态文案」「时间」三列空。响应结构官方没写，所以商品详情与订单列表
现在把上游**真实字段名**（只有名字）带回页面显示成「上游字段：…」，哪列空就
对着它改映射，不必再让人去 F12 里抄。

生产实测的两个好消息：62 的 `/info` 改读 `client_ip` 后拿到真实出口 IP
（38.147.105.28，这就是要进 62 白名单的值）；Hero 余额经兼容层读回来了。
