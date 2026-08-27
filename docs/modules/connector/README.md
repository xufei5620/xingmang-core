# Connector 模块（含 Sub2API 只读接入）

第三方系统的接入边界（ADR-004、ADR-018）。平台不拥有第三方的业务真相，
只按契约**读**它，并把读到的东西连同新鲜度一起交给看板。

## 代码结构

| 位置 | 职责 |
|---|---|
| `internal/platform/connector/config.go` | 连接配置与闸 1（拒绝可写连接配置） |
| `internal/platform/connector/transport.go` | 闸 2/4：只读方法限制、主机 allowlist、拒绝重定向、强制超时 |
| `internal/platform/connector/types.go` | `ErrorKind` 分类、`Error`、`VersionInfo`、`HealthResult` |
| `connectors/sub2api/contract.go` | Sub2API 只读契约：`ReadClient` 接口、数据形状、`ToObservations` |
| `connectors/sub2api/fake.go` | Fake 实现（演示与联调，**不是**业务真相） |
| `connectors/sub2api/client.go` | 真实只读客户端：凭据、错误纪律、新鲜度、版本探测 |
| `connectors/sub2api/upstream.go` | 上游路由与响应形状的**唯一**定义处 |
| `connectors/sub2api/amount.go` | 金额换算：字符串 → 整数最小货币单位，路径上没有 float |
| `connectors/sub2api/contracttest/suite.go` | 合规判据：任何实现都必须通过 `RunSuite` |
| `internal/platform/jobs/sub2api_sync.go` | 采集任务与 `NewSub2APIClientFactory`（fake/real 接缝） |
| `cmd/platform-worker/sub2api.go` | 凭据 Provider 的装配处：ref → 环境变量的登记表在这里 |

上游改版时要改的是 `upstream.go` 一个文件；`client.go` 里的错误纪律、
新鲜度推导、凭据处理都与上游形状无关。

## 四道只读闸落在哪儿

ADR-018 的四道闸原文同时覆盖数据库通道与 HTTP 通道。Sub2API 走的是
**HTTP 只读 API**，落点如下：

| 闸 | HTTP 通道落点 | 状态 |
|---|---|---|
| 1 拒绝可写连接配置 | `connector.Config.Validate()`：必须 https、必须 CredentialRef、必须非空 allowlist、endpoint 主机必须在自己的 allowlist 内 | ✅ 有测试 |
| 2 强制只读 | `ReadOnlyTransport` 只放行 GET/HEAD，其余方法在 RoundTrip 就被拒，请求**根本不发出** | ✅ 有测试 |
| 3 复核服务端只读 | HTTP 上没有 `SHOW transaction_read_only` 这种东西可查。能做的是主机精确 allowlist + 拒绝重定向（一个 302 就能把请求引到 allowlist 之外）。**「上游拒绝写请求」只能靠发一个只读账号来保证**，平台侧强制不了 | ⚠️ **部分满足**（见下） |
| 4 包内无写路径 | `ReadClient` 接口只有读方法；`client.go` 里只有 GET/HEAD | ✅ 有测试 |

### 闸 3 目前只能部分满足，原因要写清楚

**Sub2API 上游没有只读角色。** 源码里只有 `admin` 与 `user` 两种角色，
而本连接器要读的每一条数据都挂在 `/api/v1/admin/*` 下，必须 admin 权限；
上游的程序化凭据（`x-api-key`）因此是全权限的，能读也能写。

平台能保证的是「平台自己永远不发写请求」——非 GET/HEAD 在传输层就被拒，
请求根本不出网卡。平台保证不了的是「这把凭据在别处被误用」。

缓解措施（专用凭据、可独立吊销、只放在 worker 环境里、定期轮换、
每次解析留审计）写在 RUNBOOK 第 1 步。**推动上游提供只读角色或只读投影
是一条应当留下的 follow-up**，不该随这次交付一起被忘掉。

## 凭据（宪法 7 条 / ADR-014）

- Connector 只认 `CredentialRef`（`secret://<scope>/<name>`），不认环境变量名；
- 明文只存在于 `secrets.SecretValue` 里，只在 `client.go` 的 `authorize()`
  拼 `Authorization` 头那一瞬 `Reveal()`，不进日志、不进错误、不进返回值；
- 每次解析都经 `secrets.NewAudited` 留一条**不含明文**的审计记录；
- 有专门的测试把错误链与日志 dump 全文断言「不含 token 明文」——
  这条纪律是被机器检查的，不是靠自觉。

## 错误纪律（ADR-004）

上游原始错误文本**不透传**。对外只有分类 + 操作名（`Error()` 输出
`"auth: sub2api.users.read"`），响应体一个字都不进错误链——它可能带用户数据，
极端情况下还可能把请求头回显出来。

| 触发 | 分类 |
|---|---|
| 401 / 403 | `auth` |
| 429 | `rate_limited` |
| 5xx、网络不可达、超时、上下文取消 | `unavailable` |
| 404 / 405 / 501 | `not_supported`（这个上游版本没有这条路由） |
| 其余 4xx、JSON 解析失败、响应超大 | `bad_response` |
| 目标不在 allowlist、重定向被拒 | `forbidden_target`（护栏自己的判断，**不被改判**） |
| 非 GET/HEAD | `write_attempt` |

`forbidden_target` 不被改判成 `unavailable` 是有意的：否则「配置把目标写错了」
会伪装成「上游挂了」，运维会去查上游而不是去查配置。

## 金额与时间

- 金额一律 `int64` 最小货币单位 + `Currency`，**换算路径上不出现任何浮点**：
  上游的数字从 JSON 字面量开始就以文本形态传递（`rawAmount`），
  由 `decimalToMinorUnits` 用字符串与整数运算换算，多余精度四舍五入
  （截断的误差带方向，会系统性少算）；
- 币种是显式配置（`WithCurrency`），不是猜的：猜错币种的金额是**看起来对**的错数字；
- 时间库内一律 UTC；业务日的时区在 `upstream.go` 的 `businessDayLocation`
  显式声明，它必须与上游的日切设置一致，否则每天的第一条与最后一条订单会算错边。

## 新鲜度（规格 §9.1）

每个结果都带 `Snapshot{ObservedAt, Watermark, IsPartial}`，没有「只返回值」的路径。

`ObservedAt` 的取值优先级：**上游数据时间戳 > 上游 `Date` 响应头 > 本地接收时刻**。
退到最后一档时，语义已经从「数据什么时候被观测」滑向「我们什么时候问的」——
上游有缓存时两者能差出一个缓存周期。所以这个退化路径在代码里有注释、
在 RUNBOOK 里有验证步骤，不会悄悄发生。

`IsPartial` 表示这次只读到了部分数据（分页被截断、部分渠道读不到）。
看板必须把它显示出来：不完整的数据当完整数据用，比没有数据更危险。

## 测试

```bash
go test ./connectors/... ./internal/platform/connector/
```

| 文件 | 覆盖什么 |
|---|---|
| `client_contract_test.go` | 用 `httptest.NewTLSServer` 起假上游，把 `FakeOptions`（FailWith / Latency / Partial / Unhealthy / UnsupportedVersion / MissingCapabilities）翻译成假上游的行为，然后跑 `contracttest.RunSuite`——**真实客户端与 Fake 必须通过同一套套件**；另有上游形状映射、凭据不泄漏、allowlist、拒绝重定向 |
| `client_guard_test.go` | 包内直证：客户端手里那个 `http.Client` 就是带护栏的那一个（allowlist 之外拒、写方法拒、有超时） |
| `amount_test.go` | 金额换算的边界：浮点脏值、科学计数法、四舍五入、溢出、未登记币种 |

假上游里的路由常量是**故意重复写**的，不从生产代码引用：路由是我们与上游
之间的约定，有人改了生产代码里的路径，测试必须红——引用同一个常量的话，
改哪边测试都绿，等于没测。

## 尚未做的事

- 真实上游**未验证**：路由与响应形状按 `K:/sub2api-src` 的源码与
  `contracts/connectors/sub2api.read.v1.md` 确定，但没有对着真实实例跑过一次。
  验证清单见 RUNBOOK「账号到位后的验证清单」；
- 兼容矩阵 `SupportedUpstreamVersions` 只有一条线，等真实探测值回填；
- 重试与退避没有做在 Connector 里：周期任务本身就是 5 分钟一轮，
  失败会诚实落库并在下一轮自然重试，客户端内再加一层重试只会把
  「一次失败」变成「一次更慢的失败」。
