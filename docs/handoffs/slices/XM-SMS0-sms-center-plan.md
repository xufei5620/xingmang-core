# XM-SMS0：接码中心（Stage 1）实施计划

status: planning
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
| API key 加密存 `sms_provider_settings.api_key_enc`，带 revision/CAS | **CredentialRef**：`secret://sms/<provider>-key`，库里不存任何密文 | 宪法 7 |
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
