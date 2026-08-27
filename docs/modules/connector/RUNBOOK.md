# Runbook：Sub2API 从 Fake 切到真实只读数据

适用对象：运维 / 值班。前置阅读：本目录 `README.md`、
`contracts/connectors/sub2api.read.v1.md`。

⚠️ 切换前必须知道的一件事：`fake` 模式产出的数字是**演示数据**，
来源标识默认带 `-staging` 后缀就是为了让它一眼可辨。切到 `real` 之后，
看板上同一批指标的 `source` 会变——这正是验证是否切成功的第一个信号。

---

## 第 1 步：申请凭据（在 Sub2API 那一侧做）

⚠️ **必须先知道的坏消息：Sub2API 上游没有只读角色。**

源码里只有 `admin` 与 `user` 两种角色，而本连接器要读的每一条数据
（用户、余额、支付、账号额度）都挂在 `/api/v1/admin/*` 下，必须 admin 权限。
上游的程序化访问凭据（`x-api-key`）因此是**全权限的**——它能读也能写。

这与 ADR-018 闸 3「独立只读账号」正面冲突，而且**不是平台侧能修掉的**：
平台能保证的只有「平台自己永远不发写请求」（非 GET/HEAD 在传输层就被拒，
请求根本不出网卡），保证不了「上游拒绝写请求」。

在上游支持只读角色之前，只能靠这几条把爆炸半径压小：

1. **专用凭据**：为平台单独生成一把 `x-api-key`，绝不复用任何人的个人账号
   或别处在用的 key。轮换、吊销、审计都要能对上「平台采集」这一个用途；
2. **可独立吊销**：这把 key 在上游的管理后台可以单独删掉/重置，
   出事时不必连带影响别的集成；
3. **只放在 worker 的环境里**：不进前端、不进 API 服务、不进任何人的机器；
4. **定期轮换**：上游重新生成一次，改 `.env` 里的 `XM_SUB2API_TOKEN` 重启即可，
   引用与代码都不用动；
5. **留证据**：每次解析凭据都有一条 `secret_access` 审计日志（不含明文），
   出现异常读取时能对时间线。

拿到 key 之后：

- 记下 key 明文，准备填进第 2 步的 `XM_SUB2API_TOKEN`；
- 把这个凭据的**用途与端点**登记进 `docs/inventory/managed-systems.yaml`
  （登记的是用途与端点，**不是** key）；
- 在 Issue 里提一条 follow-up：推动上游增加只读角色，或者为平台开一条
  只读投影。闸 3 在拿到只读凭据之前始终是**部分满足**状态，
  这件事不该随着这次交付一起被忘掉。

## 第 1.5 步：给这个账号做一次合规确认（漏了这步，后面全是 `auth` 失败）

⚠️ **上游 0.1.183 起有一道合规门。** `/api/v1/admin` 组挂了
`AdminComplianceGuard`：凭据对应的 admin 账号没确认过合规声明时，
**所有** admin GET 返 `423 Locked`（正文 `code=ADMIN_COMPLIANCE_ACK_REQUIRED`），
而本连接器要读的 6 条路由全在这个组下面。

- **做什么**：由人对该账号执行一次 `POST /api/v1/admin/compliance/accept`
  （上游后台点一下也行）。只做一次，之后一直有效；
- **查现状**：`GET /api/v1/admin/compliance` —— 这条路由在 guard 的白名单里，
  被门挡住时它自己还是能回答的；
- **平台为什么不能代做**：那是个 POST。只读通道在传输层就拒绝非 GET/HEAD，
  请求根本不出网卡（ADR-018 闸 4），这是护栏在正确工作，不是缺陷；
- **漏做的表现**：切到 `real` 之后这批指标全部 `failed` +
  `last_error_code=auth`，而**不是** `bad_response`。看到 `auth` 先按
  第 1.5 步查一遍，再去怀疑 Token 本身。

## 第 1.6 步：确认这 5 个端点永远不采

上游有 5 个端点 HTTP 200、形状正常，但 handler 里返回的是写死的常量
（源码注释：`Return mock data for now`）：

```
/api/v1/admin/users/:id/usage
/api/v1/admin/dashboard/realtime
/api/v1/admin/redeem-codes/stats
/api/v1/admin/groups/:id/stats
/api/v1/admin/proxies/:id/stats
```

当前 `connectors/sub2api/upstream.go` 在用的 7 条路由**都不在这份名单里**，
所以现在不用做任何事。这一步存在的意义是将来扩采集面时先回头看一眼：
采了假数据会得到一批**永远不动却显示新鲜**的指标——比读不到更糟，
读不到会告警，假数据不会。

## 第 2 步：配置四个变量

在 `deploy/compose/.env`（不入库）里：

```dotenv
XM_SUB2API_ENDPOINT=https://api.solov.cc
XM_SUB2API_TARGET_ALLOWLIST=api.solov.cc
XM_SUB2API_CREDENTIAL_REF=secret://sub2api/readonly-token
XM_SUB2API_TOKEN=<第 1 步拿到的 Token 明文>
```

要点：

- `XM_SUB2API_ENDPOINT` 必须是 **https**，http 会被连接配置直接拒绝；
- `XM_SUB2API_TARGET_ALLOWLIST` 是**精确**主机清单（逗号分隔），
  不做后缀匹配、不做通配。留空不是「放行一切」而是「一个请求都发不出去」；
  endpoint 的主机必须自己也在清单里，否则配置自相矛盾；
- `XM_SUB2API_CREDENTIAL_REF` 是引用不是凭据，可以安全出现在日志与配置里；
- `XM_SUB2API_TOKEN` 是这个引用在 env Provider 下的落点。Connector **不认识**
  这个变量名，它只解析引用——将来换 SOPS/Vault，改的只有
  `cmd/platform-worker/sub2api.go` 里的登记表（ADR-014）。

⚠️ 四项缺任意一项，worker **照常启动**，只是每轮同步往看板写一条
`not_supported` 的失败观测，并在日志里说清缺哪个变量。一个没配好的采集通道
不该把心跳和别的任务一起拖垮。

## 第 3 步：切模式并重启

```bash
# .env 里改成 real
XM_SUB2API_MODE=real

docker compose -f deploy/compose/launch.yaml up -d platform-worker
```

启动日志里先确认这一行（`worker_started`）：

```json
{"event":"worker_started","sub2api_mode":"real","sub2api_source":"...","sub2api_sync_enabled":true}
```

`sub2api_mode` 还是 `fake` 说明变量没进容器——检查是不是改了 `.env`
但没重建容器。

## 第 4 步：验证数据真的换了源

### 4.1 看 `source` 变了

```bash
curl -s localhost:8088/api/v1/metrics | jq '.items[] | {metric_key, source, freshness}'
```

- `source` 应从 `sub2api-staging`（Fake 默认）变成你配的
  `XM_SUB2API_INSTANCE_ID`（未配则仍是默认值——**建议一起改掉**，
  让「这是真实数据」在看板上自证）；
- `freshness.state` 应为 `fresh`；
- `freshness.observed_at` 应在最近一个同步周期内（默认 300s）。

### 4.2 看新鲜度是**真的**在动

隔两个同步周期再查一次，`observed_at` 必须前进。不动只有两种可能：
同步没在跑，或者上游返回的时间戳是个常量——后者会让看板永远显示「新鲜」，
是最危险的一种假信号。

判断依据：

- `observed_at` 前进 + `synced_at` 前进 → 正常；
- `synced_at` 前进但 `observed_at` 不动 → 同步在跑，但上游给的数据没更新，
  或者我们取错了时间戳字段。查 `connectors/sub2api/upstream.go` 里
  该指标的 `observedAt` 取值来源；
- 两者都不动 → 同步没在跑。查 worker 日志里的 `job_completed` 事件。

### 4.3 看失败是**看得见**的

把 `XM_SUB2API_TOKEN` 临时改成一个错值并重启，看板上这批指标应该在下一个
周期变成 `failed` + `last_error_code=auth`，且**保留上一次成功的值与
`observed_at`**（失败不清空历史，staleness 会随时间自然增长）。
验证完记得改回去。

这一步不是可选的：能诚实显示失败，比显示正确数字更重要——
不会失败的看板只是一张漂亮的图。

## 回滚

```dotenv
XM_SUB2API_MODE=fake
```

重启 worker 即可。或者干脆停掉采集（宪法 26 条的停用开关）：

```dotenv
XM_SUB2API_SYNC_ENABLED=false
```

停用之后看板不会假装新鲜：`observed_at` 不再前进，新鲜度自然降级为
「延迟」，而不是一直显示最后那批数字（规格 §9.1）。

## 故障对照表

看板上的 `last_error_code` / 日志里的 `error_code`：

| 分类 | 含义 | 处置 |
|---|---|---|
| `not_supported` | 四个变量没配齐，或上游没有这条路由 | 日志里有「缺少 XM_SUB2API_…」的清单；若变量齐全则是上游版本对不上，看 `Version()` 探测值 |
| `auth` | 凭据解析不出来、上游拒绝了这个凭据（401/403），**或者没做合规确认（423）** | 按这个顺序查：①`secret_access` 事件的 `success` 字段——false 说明 Token 压根没注入进容器；②true 但仍 `auth`，先按**第 1.5 步**确认合规门（`GET /api/v1/admin/compliance`），这是切真实数据时最常见的一次性漏项；③都不是才怀疑 Token 被吊销 |
| `forbidden_target` | 目标主机不在 allowlist，或上游发了重定向 | 查 `XM_SUB2API_TARGET_ALLOWLIST` 与 endpoint 主机是否一致；上游若真的改用重定向，需要人确认新地址后加进 allowlist，**不要**改成跟随重定向 |
| `rate_limited` | 上游限流 | 调大 `XM_SUB2API_SYNC_INTERVAL`；默认 300s 已经很稀疏，触发限流说明上游侧有更严的策略或有别的调用方在共用这个账号 |
| `unavailable` | 网络不通、超时、上游 5xx | 先看上游自己健不健康；连续多轮才需要动作，偶发一轮下一轮就补上了 |
| `bad_response` | 响应格式看不懂、字段缺失、响应超大 | 多半是上游改版。对着 `connectors/sub2api/upstream.go` 核字段名，改完跑 `go test ./connectors/...` |
| `write_attempt` | 只读通道上出现了写请求 | **不要绕过**。这是护栏在报警：代码里有人往只读客户端加了写路径（ADR-018 闸 4） |

## 账号到位后的验证清单

真实凭据到位前，本连接器只对**本地假上游**验证过（契约测试全绿）。
拿到账号后按这份清单逐项确认，每一项都可能推翻一个假设：

- [ ] **版本探测**：跑一次 `Version()`，把 `Detected` 实际值补进
      `docs/inventory/managed-systems.yaml` 的 `detected_version`
      （在此之前那里写的是「未探测」，别把源码核对版本抄上去冒充观测）。
      矩阵 `SupportedUpstreamVersions` 登记的是整条 `0.1` 线，已覆盖已知的
      0.1.133~0.1.183，多半不用动；若 `Supported=false`，先确认是矩阵没登记
      还是上游真的换了大版本；
- [ ] **能力清单**：跑一次 `Capabilities()`，确认返回的能力与
      `contracts/connectors/sub2api.read.v1.md` 的清单对得上；少了哪项就
      记录下来（契约允许子集，但要知道少了什么）；
- [ ] **路由形状**：确认 `upstream.go` 里每条路由都返回 2xx 而不是 404
      ——404 会被归类成 `not_supported`，看起来像「上游版本旧」，
      实际上是路径写错了；
- [ ] **字段名与单位**：把一次真实响应与 `upstream.go` 的结构体逐字段对一遍。
      **重点是金额单位**：上游给的是元还是分、是货币还是配额、
      配额与货币的换算基数是多少。单位错了的数字看起来完全正常；
- [ ] **币种**：确认 `WithCurrency` 配的币种与上游记账币种一致。
      ⚠️ **收入侧和成本侧在上游不是同一个币种概念**：`payment/dashboard` 的
      金额按**币种分桶**（每张订单自带币种，上游默认 `CNY`），而用户余额、
      用量成本、账号额度全线按**美元**记账。本契约只有一个 `Currency` 字段，
      配错时收入会变成 `0 + IsPartial`（不会变成一个错数字，这是有意的）。
      验证方法：随便挑一个**有充值**的日子跑 `DailyOrders()`，
      收入是 0 且 `IsPartial=true` 就说明币种配错了，去上游后台看那天的
      订单币种，把 `WithCurrency` 改成它；
- [ ] **多币种收入**：如果上游同一天存在多个支付币种，`IsPartial` 会一直是
      true，而 `OrderCount` 是**跨币种**的（比收入覆盖的范围大）。
      真出现这种情况要记一条 follow-up：单币种的 `OrderSummary` 表达不了它，
      属于契约 v2 的事，不要在客户端里偷偷合计；
- [ ] **业务日边界**：确认 `businessDayLocation` 与上游的日切时区一致。
      验证方法：取一个已知的昨日数字，与上游后台显示的数字对一遍，
      对不上通常就是差了一个时区的边界；
- [ ] **观测时刻**：确认上游是否在响应里给了数据时间戳。没有的话
      `ObservedAt` 会退到 `Date` 头或本地接收时刻——那时看板显示的
      「新鲜」表达的是「我们刚问过」，不是「数据刚更新」，
      这个差别要让看板的读者知道；
- [ ] **分页与截断**：确认渠道数、用户数是否超过一页。超过而没被标记
      `IsPartial` 的话，看板会把不完整数据当完整数据展示；
- [ ] **限流**：确认上游对这个账号的限流阈值，与 300s 的采集周期比一比；
- [ ] **只读性**：确认上游是否已经支持只读角色。**目前不支持**（见第 1 步），
      所以这一项现在只能确认「这把 key 是平台专用且可独立吊销」。
      **不要**用发一个写请求的方式去试探权限。
