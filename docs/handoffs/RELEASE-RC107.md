# RELEASE RC107 —— 死信只冻本账号（L1）+ 用户端余额按上游口径显示

- status: 候选（源码门禁 `verify.ps1` 待跑；未签名，未传输，未部署）
- branch: `ai/claude/XM-INV-RC107`
- base: RC106 生产提交 `7efece3e`（2026-09-09 04:20Z 已部署）+ RC106 分支后续文档 `faadf87`
- worktree: `K:/发票/wt-XM-INV-RC107`
- 内容：
  - `ai/claude/XM-INV-DEAD-CONTAINMENT`（L1）：`61f5ca5` → `4d468d6` → `b39a123` → `80d5774` → `f8386f8`（合 RC106）→ `df97427` → `59d22ec` → `1112322` → `139ee4e`
  - `ai/claude/XM-INV-UNIT-DISPLAY`：`a159309` → `9535864` → `f5498f4` → `8436e19` → `f61f92d` → `dba423b`，合并提交 `aeb3dc2`
  - `0dea130`（发布身份 RC106→RC107，**先于打 tag**）→ 本文
- 切片交接单：`docs/handoffs/XM-INV-DEAD-CONTAINMENT.md`、`docs/handoffs/XM-INV-UNIT-DISPLAY.md`
- **不含** L2（`ai/claude/XM-INV-OBSERVED-AT-PER-BINDING`，迁移 0033）：0033 打上之后不带它的构建起不来，发布当下没有可回滚的目标构建，留 RC108 先做回滚演练。

## 为什么发

1. **L1**：RC104/RC105 两次事故的根因之一——一条事实死信就让同来源全体客户 `source_unavailable`、readyz 503。
   L1 让死信只冻结**本账号**（开放冻结「兜住」该事件），流级 fail-closed 只对**无人认领**的死信生效；解冻关卡装到全部五扇门，
   任何修复工具都不能在事件仍死信时把冻结放出去；结转证明在账号带着被兜住的失败检查点时**持有**而不是造假；
   四个健康面（readyz、管理端就绪面板、修复工具、验证脚本）同一份「兜住」判据。迁移 `0032`：`eligibility_freezes` 上一条
   开放冻结 × 修订哈希的部分索引（索引，不建表，无需 permissions 重放）。
2. **单位显示**：负责人 09-09 要求用户端「切点前旧余额」「赠送 / 返利 / 管理员额度」两格不再显示 `30,179,629,498 SUB2_BALANCE_1E8`
   这类后台单位，改成上游口径的余额数（`301.80 · SoloV API 余额`），原始单位放进 title 供对账。换算表进
   `contracts/invoice-eligibility-wire.v1.json`（`SUB2_BALANCE_1E8` ÷ 1e8、`NEWAPI_QUOTA` ÷ 500000，四舍五入 2 位），
   后端发现闸保证代码里出现的单位码不多不少都在契约里。
3. 顺带：管理端来源健康页的就绪原因 `ECONOMIC_RESCAN_ACTIVE` 今天没有中文文案（显示「未识别的安全阻断原因」），补上并给这一族加两侧对拍闸；
   RC106 资格状态闸与新单位码闸共用透传判据，扫描范围改为全仓发现。

## 对抗复审与变异（摘要，明细在两份切片交接单）

- L1：两轮对抗复审（数据完整性 / 复发与运维）→ 修复 → 终审 → 两轮补闸；发现型闸的判据从「SQL 拼法清单」改成
  「字面量同时含列名与 'dead' 必须来自渲染器或同一拼接链」，解冻关卡从短语匹配改成「表名 + status 赋值」分开匹配；
  三条静态规则证明不了的边界（绑定参数、SQL 注释/短路、引号标识符）写在判据旁边。共 48 条变异。前端三处补 9 条测试。
- 单位显示：一轮对抗复审（pass）→ 两条 major 处置（扫描根全仓发现 + 覆盖探针；跨包选择器按解析结果拒绝）→ 复核 pass →
  两轮同款洞清理（资格状态闸共用判据、范围发现）。复审用独立实现算了 34 组换算全等。共 30 条变异。

## 门禁（全部实测，UTC）

| 门禁 | 开始 | 结束 | 耗时 | 结果 |
|---|---|---|---|---|
| L1 分支全量 `go test -p 1 -count=1 ./...`（`139ee4e`，库 `invoice_test_l1merge`） | 06:59:43 | 07:06:14 | 6m31s | exit 0，30 包（postgresstore 282s） |
| L1 分支前端 / no-secrets | 07:06:27 | 07:06:32 | 5s | typecheck 0；22 文件 339 用例；no-secrets 0 |
| 单位显示分支全量（`dba423b`，库 `invoice_test_unitdisp`） | 06:43:37 | 06:49:59 | 382s | go test 0（376s）；vet 0；22 文件 374 用例；no-secrets 0 |
| 两分支干跑合并 `git merge-tree` | 06:5x | — | — | clean（App.tsx / http-api.ts / types.ts 自动合并） |
| `scripts/test-release-image-gate.ps1`（改名后） | 07:1x | — | 2s | exit 0 |
| 合并后全量（`verify.ps1`，detached runner + WSL bash） | 待跑 | | | |
| 镜像门禁 / 产物校验 / 签名 | 待做 | | | |

## 顺序

1. ✅ 合并两分支（`aeb3dc2`）→ 推进发布身份（`0dea130`）——**在打 tag 之前**
2. 🔄 `verify.ps1`（`release/run-rc107-gate.ps1` 经 `scripts/run-detached.ps1`）
3. `K:/发票/wt-XM-INV-AUTOLOGIN` detach 到本分支最终提交
4. 签名 tag `v0.1.0-rc107-signed`
5. 镜像门禁（预期 exit 42）→ 普通校验 → 严格可传输校验（`release/run-rc107-image-gate.ps1` 经 detached runner）
6. `ssh-keygen -Y sign` 签 `SHA256SUMS`（bash 重定向验签）
7. **停下，把传输/部署命令交给负责人**；部署带迁移 0032（roll-forward 步骤 0 自动执行；索引创建，无锁表）

## 部署后预期观测

- readyz 200；`source_ingest_events` 无 dead；管理端来源健康页各流「就绪」，不再出现「未识别的安全阻断原因」。
- 用户端两格显示换算数（用户 34：`301.80 · SoloV API 余额` / `502.89 · SoloV API 余额`），title 里保留原始单位。
- 下一次任何账号出现死信：只有该账号被冻结（管理端冻结队列出现 EVENT_DEAD 且「已兜住」），其它账号继续可开票，readyz 保持 200。

## 回滚（带迁移，不是「重发镜像」一步）

`migrate.Verify` 对「库里有、二进制没有」的迁移会拒绝启动（`backend/internal/migrate/migrate.go:75,147`
`database contains unknown migration`）。`b39a123` 改的是**测试**的排除表，与运行时无关——所以 0032 打上之后，
RC106 的 API 镜像**起不来**，回滚必须两步：

1. 先在开票库（owner 角色）撤掉账本记录：`DELETE FROM public.schema_migrations WHERE name='0032_eligibility_freezes_open_revision_index.sql';`
   索引 `eligibility_freezes_open_revision_idx` 可以留着（无害），要干净就 `DROP INDEX IF EXISTS eligibility_freezes_open_revision_idx;`
2. 再按常规 roll-forward 到 RC106（`/root/invoice-system/app/releases/7efece3e…`）。

两步都是 Platform Lifecycle Operation，需负责人批准后执行；第 1 步 SQL 在执行前先 `SELECT` 核对只命中一行。
仓库里没有现成的「带迁移回滚」手册段落（grep 过 PRODUCTION-RUNBOOK.md），这是 L2（0033）前必须补的 runbook 欠账，记入 follow_ups。
