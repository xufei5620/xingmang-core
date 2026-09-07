# XM-ERRCODE-HTTP-BOUNDARY：把错误码契约钉在 HTTP 边界上

- **status:** implemented，未上线。**纯测试**，一行生产代码都没改。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-0030c 提交），
  基线 `cfd5084`。
- **来源：** `docs/handoffs/slices/XM-KERNEL-ERRCODE0.md` 的 follow_up 第 1 条。

## 为什么

XM-KERNEL-ERRCODE0 修的是一个**真实生产故障**：`Kernel.Execute` 无条件把
Handler 的错误改写成 `EXECUTION_FAILED`，于是 assurance / alerts /
credentials / finance 各包精心设计的 `INVALID_PARAMS` / `CONFLICT` /
`PRECONDITION_FAILED` 全部不可达——真实调用方看到的是 502 加一句
「action xxx 执行失败」。

那一片自己留了 follow_up：修复只钉在 `action` 包的内核单测里，而这条链
**真正要成立的地方是 HTTP 边界**：

```
Handler 的域错误 → Kernel 保留 Code/Message → StatusForCode 映射 → safeMessage 输出
```

四段里任何一段断掉，调用方就拿不到设计好的答案。而 `internal/platform/httpapi`
在这一片之前**一个用真内核的测试都没有**——全是 `fakeExecutor`。那些用例证明的
是「HTTP 层会照搬 executor 给的错误」，**不是**「内核真的会给出那个错误」。

这正是本 session 反复踩到的同一类问题：规则存在 ≠ 调用方走得到它。

## 改了什么

只加了一个测试文件：`internal/platform/httpapi/actions_errorcode_boundary_test.go`。

它装一个**真内核**（真注册表、真 Definition、真 Handler、真 RunStore），
挂进真路由，发真 HTTP 请求。

## 测试（6 组）

| 用例 | 钉住什么 |
|---|---|
| `TestHandlerDomainErrorReachesHTTPUnchanged` | 四种域错误码的 Code、**Message 逐字**、HTTP 状态码、request_id 回传 |
| `TestBareHandlerErrorIsNormalizedAndDoesNotLeak` | 反向的那一半：裸错误归一成 502 EXECUTION_FAILED，且 `pq:` / 约束名 / 内网 IP 一个字都不进响应 |
| `TestKernelPrechecksReachHTTPWithRightStatus` | Handler 之前的三道判定（Schema 400 / 未注册 404 / 缺权限 403），且 403 文案带 scope 名 |
| `TestSuccessPathThroughRealKernel` | 地基：成功路径真的通。没有它，上面几条有可能因为「任何请求都失败」而误判成通过 |
| `TestActionRunRecordsTheDomainCode` | 那条 bug 的**第二个**后果：`ActionRun.ErrorCode` 也被污染成 EXECUTION_FAILED，事后按错误码统计分布时全归一类。响应对了不代表记录也对——两者是内核里的两条不同赋值 |

**文案逐字断言**是刻意的：只断言 Code 的话，内核把 Message 改成通用文案而
保留 Code 的实现照样绿，而那正好是这条设计废掉一半的样子。

### 变异验证

把 XM-KERNEL-ERRCODE0 的修复原样回退（`errors.As` 那一支改成永假），四个域
错误用例与 `ActionRunRecordsTheDomainCode` 一起变红，响应体逐字复现了当初的
故障形态：

```
状态码 502，期望 400：{"error":{"code":"EXECUTION_FAILED",
  "message":"action assurance.probe.declare 执行失败","request_id":"req-boundary"}}
```

`TestBareHandlerErrorIsNormalizedAndDoesNotLeak` 在变异下仍绿——**这是对的**，
它测的是另一半契约（裸错误该被归一），回退修复不影响它。两条一起才完整。

门禁：`go test -p 1 -count=1 ./...` 全绿、`go vet ./...` 退出 0、
`gofmt` 干净、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 无泄漏。前端未改，未跑（上一片刚全绿）。

## 顺带发现（**没有在本片修**）

**全仓从未调用 `slog.SetDefault`。**

`cmd/platform-api/main.go:38` 与 `cmd/platform-worker/main.go:21` 各自建了一个
JSON handler 的 logger 注入进 `Deps.Logger`，但包级的 `slog.Xxx` 调用走的是
**全局默认**——文本格式、写 stderr。

受影响的是 4 个调用点、3 个文件，全在 `httpapi`：

- `response.go:94` —— `WriteError`，**每一条错误响应**都在这里记日志；
- `response.go:82` —— 响应编码失败；
- `health.go`、`cards_balances.go` 各一处。

后果：**最需要被检索的那类日志（错误）与其余日志格式不同、流也不同**。
按 JSON 解析日志的采集会漏掉它们。

没有在本片修，因为它是**生产日志行为的变更**（错误日志从 stderr/文本移到
stdout/JSON），而本片的范围是「补一条边界测试」，一行生产代码都没改。
两种修法各有取舍：

1. `slog.SetDefault(logger)`（两个 main 各一行）——简单、是 Go 的常见做法，
   但从 main 改全局状态；
2. 把 logger 经中间件放进 request context，`WriteError` 从那里取——更正确，
   但要动每一个 `WriteError` 调用点。

建议取 1，单独一片，并在改动说明里写清「错误日志的流与格式会变」。

## follow_ups

- **上面那条 `slog.SetDefault`**（见「顺带发现」）。
- XM-KERNEL-ERRCODE0 的 follow_up 第 2 条仍然开着：建议
  `assurance`/`alerts`/`credentials`/`finance` 四个包各自过一遍自己的
  `domainError` 映射表——那条 bug 此前一直遮蔽着它们的判断，现在这些判断
  **终于生效**，值得各自确认 Code/Message 组合仍是当前想要的行为。本片只
  证明了「链路是通的」，没有替它们审内容。
- 本片的真内核装配（`boundaryRouter`）可以被别的 HTTP 边界测试复用；如果
  以后有第二处需要，把它抽到 `testhelpers_test.go`。现在只有一处，不预先抽。
