# XM-LOG-INJECTED：错误路径用注入的 logger

- **status:** implemented，未上线。**这是生产日志行为的变更**——见下方
  「上线时会看到什么变化」。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-ERRCODE-HTTP-BOUNDARY
  提交），基线 `d513810`。
- **来源：** `docs/handoffs/slices/XM-ERRCODE-HTTP-BOUNDARY.md` 的「顺带发现」。

## 修的是什么

`cmd/platform-api/main.go` 与 `cmd/platform-worker/main.go` 各建了一个
JSON handler 的 logger 注入进 `Deps.Logger`，访问日志与 panic 恢复都用它。
但 `httpapi` 里有四处走的是**包级 `slog`**，也就是全局默认——而全仓
**从未调用 `slog.SetDefault`**，所以那个默认是标准库的兜底：文本格式、写 stderr。

四处里最要紧的是 `response.go` 的 `WriteError`：**每一条错误响应都在那里记日志。**

后果不是「格式不好看」：**最需要被检索的那类日志与其余日志格式不同、流也不同。**
一个按 JSON 解析 stdout 的采集会整片漏掉它们——而那正是出事时第一个要查的东西。

## 怎么修的

**两层，各修各的那一半。**

### 第一层（真正的修法）：logger 走 request context

新增 `WithLogger` / `LoggerFrom`（`response.go`）与 `Logging` 中间件
（`middleware.go`），`NewRouter` 在 `Recover` **之前**挂上它——panic 恢复时
也该用注入的 logger。

三处有 ctx 的调用点改用 `LoggerFrom(ctx)`：`WriteError`、
`health.go` 的 `readiness_failed`、`cards_balances.go` 的 `card_balances_failed`。

**为什么走 context 而不是给 `WriteError` 加参数**：它有几十个调用点，加参数
是一次纯机械的大改，而且以后每加一个 handler 都要记得传。走 context 之后
**一个调用点都不用动**。

### 第二层（兜底）：两个 main 各加一行 `slog.SetDefault(logger)`

第四处调用点是 `WriteJSON` 的编码失败日志，它**没有 ctx 也没有 logger 参数**
——`WriteJSON(w, status, v)` 有近百个调用点，为一条极少发生的日志改签名不划算。

设了默认之后，那一条（以及日后任何新写的包级 `slog` 调用）也会落到同一个
JSON/stdout 上。

**安全性核对过**：全仓没有任何地方 import 标准 `log` 包，所以这一行不会改变
除 slog 之外的任何输出（`slog.SetDefault` 会连带接管 `log` 包）。

## 上线时会看到什么变化

**错误日志的流与格式会变**：从 stderr / 文本 变成 stdout / JSON，与访问日志
一致。容器里 `docker logs` 两个流都收，所以**不会丢日志**；变的是格式与流。

如果有任何采集/告警规则是按「stderr 上的文本行」匹配的，那条规则要跟着改。
本片没有发现这样的规则，但这是部署时该确认的一件事。

## 测试

`internal/platform/httpapi/logging_test.go`（4 条）：

- `TestWriteErrorUsesInjectedLogger` —— 错误日志进注入的 logger，且
  `request_id` / `error_code` / `method` / `status` 四个字段齐；
- `TestErrorLogGoesToTheSameSinkAsAccessLog` —— **最直白的那条**：一次失败的
  请求，访问日志与错误日志落在同一个 buf、同一种格式。修之前这两条会分家；
- `TestReadinessFailureUsesInjectedLogger` —— 探针那一处（四个调用点里唯一
  在 `/api/v1` 之外的，容易漏）；
- `TestLoggerFromFallsBackToDefault` —— 回落不返回 nil（否则调用方立刻 panic）、
  `WithLogger(nil)` 也回落，**并带对照**：塞了真 logger 就要拿到那一个，
  否则这条回落等于永远生效。

`logLines` 解析失败即失败：一行解析不出 JSON 就说明有人往同一个流里写了
非 JSON——那正是这一片要消灭的情况。

### 变异验证（三项）

| 变异 | 结果 |
|---|---|
| `WriteError` 退回包级 `slog` | 红（2 条） |
| 路由不挂 `Logging` 中间件 | 红（3 条） |
| 探针那处退回包级 `slog` | 红（1 条） |

门禁：`go test -p 1 -count=1 ./...` 全绿、`go vet ./...` 退出 0、
`gofmt` 干净、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 无泄漏。前端未改。

## risks

- **`slog.SetDefault` 是全局状态。** 测试里没有任何一条依赖它（`LoggerFrom`
  的回落测试只断言「非 nil」，不断言是哪一个），所以不会有测试间的耦合。
  但日后若有人写「断言 `slog.Default()` 是某个具体 logger」的测试，要记得
  用 `t.Cleanup` 还原。
- **`Logging` 中间件只挂在 `NewRouter` 上。** 直接构造 handler 的调用路径
  （测试、以及任何绕过路由的用法）拿不到注入值，会回落到默认——这是刻意的，
  「日志记不出来」不该让请求失败。

## follow_ups

- **部署时确认**一遍有没有按「stderr 文本行」匹配的采集或告警规则（见
  「上线时会看到什么变化」）。本片没有发现，但没有发现不等于没有。
- `internal/platform/jobs` 与 `connectors/*` 也各有自己的 logger 注入约定
  （`structuredDefaultLogger()`）。本片只查了 `httpapi`；那两处**没有**发现
  包级 `slog` 调用，但值得在下一次改到它们时顺手复核。
- XM-KERNEL-ERRCODE0 的 follow_up 第 2 条仍开着（四个领域包各自过一遍
  `domainError` 映射表）。
