# XM-SMS0：接码中心（Stage 1）实施计划

status: done
branch: ai/claude/XM-CARD0-infini-connector
参考：`C:\Users\58439\Desktop\SoloAI接码实现与参考文档-2026-09-05`
（SoloAI desktop 冻结提交 `880101bc` 的交接包，**不是本仓代码**）

产品负责人 2026-09-05 决定：**两家供应商都做**；**验证码落库并推企业微信**，
与卡片 3DS 验证码同一口径。

---

## 一、这不是照抄——四处必须偏离参考实现

参考实现是另一套架构（桌面壳 + 本机 Admin API）。落到星芒，下面四条是宪法
约束不是选择：

| 参考实现 | 星芒 | 依据 |
|---|---|---|
| API key 加密存 `sms_provider_settings.api_key_enc`，带 revision/CAS | **CredentialRef**：`secret://sms62/api-key`、`secret://hero-sms/api-key`，库里不存任何密文 | 宪法 7 |
| 17 个 Admin 路由直接读写 | 写全部走 **Action**（L1；内核对 L2+ 返回 `ADVANCED_CONTROLS_REQUIRED`），读走 Query 端点 | 宪法 2、3 |
| 前端表单填 key | 走既有「密钥引用」页，与 Infini 凭据同一条路 | 宪法 7 |
| 号码 AES-GCM 加密落库 | **明文列 + `sms.reveal` 权限闸** | 见下 |

### 号码为什么不加密

本仓**没有列加密工具**——`internal/store` 下没有 AES-GCM，凭据加密只发生在
SecretProvider 的文件层。而卡面 PAN/CVV 已经是明文列（产品负责人 2026-09-04
的决定，见迁移 000027）。

在 PAN 就躺在隔壁明文列的前提下单独给手机号加一层 AES，抬不高任何一道真实
门槛：能读库的人已经能读卡号。代价却是实打实的——一套密钥管理、轮换、启动
探针，每一样都有自己的故障模式。

所以号码用明文列，由 `sms.reveal` 权限在 API 层把守，与 `card.reveal` 同一条
纪律。**这是一次显式取舍，不是疏忽**；要改成加密就得连 PAN/CVV 一起改，那是
另一个切片。

### revision/CAS 机制不搬

参考实现的 revision 是为了防止"拿一份没验证过的配置去花钱"。星芒里 key 在
SecretProvider，可以在进程不重启的情况下轮换（ADR-014）——revision 号跟不上
它，反而制造一种"版本对得上就是好的"的假确定。

替代物是同一件事的直白版本：Action 执行前检查 `enabled && verified_at 非空`。
key 若已失效，购买会拿到上游的鉴权错误——与 Infini 卡片同一个失败形态，
而那条路径已经有可读文案（P11）。

---

## 二、能复用的：七态账本

参考实现的操作状态机与我给卡片做的幂等台账是**同一个形状**：

```
prepared → submitted → succeeded | failed | unknown
unknown  → reconciled_succeeded | reconciled_failed（人工）
```

卡片那套已经有的、直接搬纪律过来的东西：

- `unknown` 是第三态，**禁止自动重试**（`RetryAllowed()` 只对 failed 为真）
- 幂等键由调用方稳定生成，页面上两步确认时生成一次、重试不变
- 上游成功但本地落库失败 → 台账停在 submitted，靠下一轮恢复转 unknown；
  **不能假设当前 DB 状态已经是 unknown**（参考实现踩过同一个坑，见其 05 章）
- 对账只改本地账本，**绝不向上游重发原动作**

不同之处：卡片按 `card_alias` 信标对账；接码按**上游订单 ID（62）/ activation
ID（Hero）** 对账，两家各有一条只读 import 通道。

---

## 三、两家供应商的实现差异（决定工作量的地方）

| 维度 | 62-US | Hero-SMS |
|---|---|---|
| base | `https://api.62-us.com` | `https://hero-sms.com/api/v1` |
| 鉴权 | `Authorization: Bearer` | `Authorization: ApiKey` |
| 成功判定 | HTTP 2xx **且** 业务 `code=1` | 现代 JSON；cancel/finish 严格 204 |
| 买号 | `POST /api/v1/get`（表单）→ 只回订单 ID | `POST /activations`（JSON）→ 直接回号码 |
| **拿到号码** | **还要再读一次** `GET /order/tokens?order_id=` | 购买响应里就有 |
| 取码 | `GET /api/v1/msg`，token 进 query | `GET /activations/{id}/otp/last` |
| 号码身份 | token 的 SHA-256（token 本身要存） | activation ID（不存 token） |
| 生命周期 | 无 | cancel / finish / replace / reactivate / prolong |

**62 那条"买完还要再读一次"是这个切片最大的不确定源**：订单已经付费成功、
补读 token 失败（哪怕是 4xx），也必须落 unknown 交人工——钱已经花了。

Hero 的 `lookupResource` 最多扫 5 页 × 100 条 **active** activation；已终结的
可能扫不到，那时返回"分页未完成"而不是"不存在"——这两者的处置完全不同。

---

## 四、落地清单

### 后端

1. `connectors/sms62/`、`connectors/herosms/`：各自的固定主机客户端。
   限流 1 请求/秒、总超时 15 秒（参考实现的常量，**不是实测的供应商限额**）。
   写路径**不做应用层重试**——重试可能真的再买一次号。
2. 迁移 000037：`sms.provider_settings`（无 key 列）、`sms.sms_order`、
   `sms.sms_resource`、`sms.sms_operation`、`sms.sms_code`。
3. `internal/platform/sms/`：Service + providerAdapter + 七态账本 + 取码。
4. Action：`sms.number.purchase`、`sms.provider.verify`、
   `sms.operation.resolve`、`sms.resource.action`（仅 Hero）。全部 L1 / HUMAN。
5. 权限：`sms.read` / `sms.purchase` / `sms.manage` / `sms.reveal`，
   挂进 `DefaultRoleScopeMap`（**曾漏过一次，见 XM-CARD0 的角色目录事故**）。
6. 只读端点：供应商能力、库存、号码清单、操作账本、验证码。
7. 验证码落库后推企业微信，复用 `cards.WeComNotifier` 那条通道。

### 前端

8. 「接码」页：供应商状态 / 库存 / 号码清单 / 取码 / 操作账本与 unknown 核对。
   形态跟卡片页一致（左清单 + 右详情），两页并排时不用重新适应。

### 刻意不做（Stage 1 边界，与参考实现一致）

- 自动重试付费、跨供应商 fallback、云端调度
- 常驻后端验证码轮询器（取码由人在页面上发起）
- 远端订单/统计/历史列表（62 的 `GetGoodsDetail`/`ListOrders`、
  Hero 的 `GetStats`/`GetProlongHistory` 客户端方法存在但不暴露）
- 参考实现里那条旧的「邮箱 URL 取码」入口——它不是这个中心的消费者

---

## 五、已知的坑（参考实现踩过，直接抄结论）

1. **62 商品 ID 是三段正整数**，而详情接口只接受两段；不能自己把两段扩成三段。
2. **取码只认唯一候选**：正文里出现多个 4–8 位数字时不猜，返回"未取到"。
3. **Hero 的 fingerprint 不是授权签名**，只是本地预检绑定；上游写体只带
   `duration`，读选项到执行之间的价格变化不受它保护。
4. **数量上限 1–200 是 SoloAI 自己的安全上限**，不能声称是官方最大值。
5. **cancel/finish 返回 204 后资源状态不自动变**——上游怎么说就怎么显示。


---

## 六、落地情况（2026-09-05 完成）

全部实现完毕，**54 条 Go 测试 + 8 条前端测试**，门禁全绿。

### 实现期改掉的五处字段口径

我第一版是按常见命名猜的，逐字对照冻结源码后改掉了五处。**后两条尤其危险
——它们不会报错，只会安静地给出一个看起来正常的错误结果**：

| 猜的 | 实际 | 猜错的后果 |
|---|---|---|
| `order/tokens` 是通用集合 | 结构化响应，带 `total`/`order_num` 自校验 | 丢掉上游给的交叉校验，半份号码被当成全部收下 |
| 号码字段 `phone` | **`number`** | 补读回来号码全是空 |
| Hero offers 是数组 | **service→country 嵌套 map** | 按数组解得到空集合——「库存为空」与「解析错了」长得一样 |
| OTP 是 `code`/`text`/`sender` | **`smsCode`/`smsText`/`phoneFrom`** | 永远取到空码，页面看起来像「还没到」，人会一直等 |
| 档位 `duration` 是标量 | **`{value,unit}` 对象** | 解出 0；按分钟当小时算会让「延长 30」变成三十小时的账单 |

### 与计划的偏差

无。四处宪法偏离照计划执行；号码明文列 + `sms.reveal` 权限闸照计划执行。

一处计划外的收获：**新加的 sms-operator 角色被那条防漂移测试当场拦下**
（前端 `STAFF_ROLE_CATALOG` 没同步）。上一批漏掉两个角色是靠产品负责人截图
发现的，这次它自己响了。

### 上线前要做的四件事

1. **配一个环境变量**：在 `deploy/compose/.env`（服务器上那份，不入库）里写
   `XM_SMS_MODE=real`（或 `fake` 先演示）。这是接码唯一的环境变量，其余全在
   管理后台。**不需要删任何旧变量**——`XM_SMS_PROVIDERS` 从未进过部署配置。

   > 2026-09-06 补：这一条原先是**做不到**的。`XM_SMS_MODE` 从没接进
   > `deploy/compose/launch.yaml`，在 `.env` 里写了也进不了容器，进程读到空串
   > 就当作 off，接码端点整组不挂载，页面显示「当前环境未启用」——而没有任何
   > 一处会报错。同一次检查还翻出另外四个早就存在的同类洞（保障探测两个、
   > 平台支付、reqlog v2 token 映射）。已补透传，并加了门禁
   > `scripts/check-compose-env.py`：进程读的每个字面量环境变量都必须在
   > launch.yaml 里出现，已废弃的（`XM_SMS_PROVIDERS`）则必须**不**出现。
2. **在管理端「密钥引用」页填两条密钥**：
   `secret://sms62/api-key`、`secret://hero-sms/api-key`。
   注意 hero 的 scope 是**连字符**：scope 规则是 `^[a-z0-9][a-z0-9-]{0,63}$`，
   不收下划线，所以供应商 id `hero_sms` 推出来的引用是 `hero-sms`。
3. **在「接码中心」逐家做连接测试，通过后点「启用」**。
   新装环境**默认两家都关着**：一个装好就自动启用的供应商，会在还没人填密钥、
   也没想清楚要不要用它的时候就出现在买号页的可选项里。
4. **授予 sms-operator 角色**给该买号的人——`sms.purchase` 刻意不给 admin。

### 为什么「开哪几家」不是环境变量

产品负责人 2026-09-05 指出这一点，是对的：最初的 `XM_SMS_PROVIDERS` 照抄了
卡片的做法，但两者不是一类东西。「今天开哪几家」是运营随时会改的决定
（换供应商、某家挂了先停掉），落进环境变量意味着每次改都要改服务器配置再重启，
而重启期间整个平台不可用——代价与这个决定的分量完全不匹配。走 Action 还多一样
东西：审计里留下「谁在什么时候把这家关了」，那是出事之后第一个要问的问题。
开关现在在 `sms.provider_status.enabled`（迁移 000038），
Action 是 `sms.provider.set_enabled@1`（权限 `sms.manage`）。
残留的 `XM_SMS_PROVIDERS` 会让启动直接失败而不是被静默忽略——一个还写着
`XM_SMS_PROVIDERS=sms62` 的配置文件会让人确信 hero 已经关掉了。

**`XM_SMS_MODE` 留在环境变量里**，因为它是另一类：它决定这个进程会不会花真钱。
做成后台可改，意味着一次误操作能让开发环境开始买真号，或者让生产悄悄切到替身
而页面看起来一切正常。

开关与验证是**两件独立的事**，不互相触发：连接测试成功不会顺手把这家打开
（运营刻意关掉的那家不该被一次测试复活），失败也不会把它关掉（密钥过期时它该
保持开着并报错，自动关掉会让「谁把它关了」变成查不出答案的问题）。

可选：`secret://sms/notify-webhook` 填企业微信群机器人地址，验证码会推过去；
不填就不推，不影响取码。

### 仍然未验证的部分（诚实的边界）

交接包自己写着两家的**真实买号、收码、生命周期都没验收过**，它只有
「Hero 连接曾成功」这一条历史记录。本仓的实现全部依据那份文档，**没有打过
一次真实上游**。因此：

- 62 的三段商品 ID 到底怎么取得，源码只校验形状、没给业务映射；
- 两家的响应字段名以冻结源码为准，与当前官方服务是否一致未测；
- 限流 1 请求/秒、超时 15 秒是参考实现的常量，不是实测的供应商限额。

**建议的第一步是连接测试**：它只读、不花钱，但会真实打到两家上游，一次就能
验证密钥引用、鉴权头形态、62 的信封解析，以及上游看到的我方出口 IP
（若他们做 IP 白名单，这是唯一能提前发现的方式）。这条通了再买第一个号。

### 刻意没做

自动重试付费、跨供应商 fallback、常驻后端验证码轮询器、远端订单/统计/历史
列表、参考实现里那条旧的「邮箱 URL 取码」入口。理由见第四节。
