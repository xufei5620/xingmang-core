# RELEASE RC104 —— 恢复开票中心可用性，并堵住导致它的复发路径

- status: 候选（未跑 `verify.ps1`，未签名，未传输，未部署）
- branch: `ai/claude/XM-INV-RC104`
- base: `331777a`（RC103，生产在跑的提交）
- worktree: `K:/发票/wt-XM-INV-RC104`
- 合并内容：`ai/claude/XM-INV-DEAD-REQUEUE` → `ai/claude/XM-INV-SER-BUSY`
  → merge `ai/claude/XM-INV-READYZ-DETAIL`

## 为什么发这一版

**这是一次故障恢复发布，不是例行发布。** 2026-09-07 08:32:30 起，生产上
`balances` 与 `usage` 两条流各自带上死信，`EVENTS_DEAD` 是致命就绪原因，于是
`SoloV Sub2API` 这个来源实例判不就绪。后果不止是 readyz 503：

- `ListFundingLots`（`application/service.go:619-625`）要求同一来源实例
  **五条流全就绪**，否则把每张额度标成 `source_unavailable`，用户端显示
  「来源同步不可用」；
- `assertSourceFreshTx` 跑的是**同一个** `evaluateSourceStreamHealth`，
  不就绪直接返回 `ErrSourceUnavailable`，**提交被真的拒绝**。

实测影响面：`funding_lots` 全表 **19 张额度、6 个用户，且全部挂在这一个来源
实例上**——也就是全平台能开票的用户，一个都开不了，持续约 21 小时。

其余七条流的就绪输入（启用、心跳 15~58 秒、版本一致、投影 healthy、水位
5~6 分钟，阈值 5m/15m）逐条核过**全部正常**：这三条死信是唯一原因，
清掉它们既必要也充分。

## 这一版包含什么

| 片 | 作用 | 是否恢复所必需 |
|---|---|---|
| `XM-INV-DEAD-REQUEUE` | 认领绑定修复（那 2 条 usage 靠它才落得下去）+ `ingest-requeue-dead` + `ingest-acknowledge-unreplayable` 两个修复工具 | **是** |
| `XM-INV-SER-BUSY` | 把 40001/40P01 判为瞬时争用，不再消耗尝试预算 | 否，防复发 |
| `XM-INV-READYZ-DETAIL` | readyz 返回是哪一道闸不满足；判死改用 Error 级别日志 | 否，但强烈建议 |

**没有捷径**：那 2 条 usage 必须靠新的认领逻辑才处理得了，而认领跑在
API/worker 里；只传工具镜像不够。把它们按「无法重投」写掉是错的——它们是
真能救回来的用量事实，`ingest-acknowledge-unreplayable` 的护栏也会拒绝。

## 合并冲突与处置

唯一冲突在 `backend/internal/application/source_processor.go`：SER-BUSY 加了
`serializationBusyRetry` + `logTransientRequeue`，READYZ-DETAIL 在同一位置加了
`logSourceEventDead`。**纯增量，语义不冲突，两侧全保留**（HEAD 一侧的函数
闭合括号原本落在两边共用的尾部上下文里，需要补回一个 `}`）。

Go **不会**对「定义了却没被调用的函数」报错，所以另外做了一次调用点核对：
相对两个来源分支，`source_processor.go` 里对这三个日志函数的调用**删除行均为
0**，没有在合并中被吞掉。

迁移：仅 `0031_claim_binding_index.sql`（来自 DEAD-REQUEUE），READYZ-DETAIL
不带迁移，无撞号。

## 部署后的修复动作

三条死信的 event id 取自生产（2026-09-08 读）：

| 流 | 类型 | event_id | 处置 |
|---|---|---|---|
| usage | usage_event | `63872270-e6d8-8223-a3ac-d20ab65242ad` | 重投 |
| usage | usage_event | `bb412266-625b-8485-9aed-5f1b0d9b00d1` | 重投 |
| balances | balance_checkpoint | `fcd2e2c6-98aa-8587-9714-8b5aeea0d566` | 写off |

两条都**先跑 dry run**（不带 `--apply`），确认报告与预期一致再 apply。
`--apply` 需要 `--operator-id=<管理员 UUID>`。命令形状见
`docs/ELIGIBILITY-OPERATIONS.md` 第 530 节与第 664 节。

那条余额检查点**结构上无法重投**：agent 侧的 event id 把载荷哈希折进去了，
任何后续扫描都会产出另一个事件。丢的是一个**已被后续检查点覆盖**的历史对账
证据点——`balances` 水位在这 21 小时里一直是几十秒的新鲜度，说明新检查点始终
在正常落库。而复开账号的证据闸只读**最新**一条 `as_of <= finalized_through`
的证据，所以一个永久缺失的更早检查点不挡复开。

顺序：部署 → 2 条 usage 重投并确认落地 → 1 条写off → 五流就绪、readyz 200 →
通过管理端正常路径解冻相关账号。

## 门禁

后端全量（`INVOICE_TEST_DATABASE_URL` 指向本机 `invoice-test-pg` 的专用库、
八个代理变量 `env -u`）：

- `ai/claude/XM-INV-SER-BUSY`（合并前）：`go build` 0、`go vet ./...` 0、
  `go test -p 1 -count=1 ./...` **退出 0，29 个包 ok**（`postgresstore` 273s）、
  `scripts/check-no-secrets.ps1` 0。
- `ai/claude/XM-INV-RC104`（合并后，专用库 `invoice_test_rc104`）：`go build` 0、`go vet ./internal/application/` 0、`go test -p 1 -count=1 ./...` **退出 0，29 个包 ok**（`postgresstore` 255.9s、`application` 27.4s、`eligibility-repair` 16.9s、`internal/migrate` 8.2s——迁移 0031 在全新库上从零跑通）。
- `scripts/verify.ps1` 完整发布门禁：**尚未运行**。

## 已知的既有欠账（非本次引入）

- `source_sync.go` 第 655 行附近 `claimBindingSelect` 拼接的空格不合 gofmt，
  来自 DEAD-REQUEUE。仓库整体 CRLF，`gofmt -l` 在 Windows 上会列出全部文件，
  需以 LF 副本单独校验才看得出真实差异。
- `MarkSourceEventProcessed` 路径上的 40001 仍未分级（走 `recordIsolated`，
  事件停在 `processing` 至租约过期后重新认领，**能自愈、不判死**）。
  详见 `XM-INV-SER-BUSY.md` 的 not_run 一节。
