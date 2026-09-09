# RELEASE RC110 —— 对账中自锁解除（RC108 影子评估打回后的修正版）+ 影子绑定手册 9c 修正

- status: 凑片完成、改名完成；源码门禁待跑；未 tag、未传输、未部署；**上线前必须两次影子评估（A/B）都通过并经负责人拍板**
- branch: `ai/claude/XM-INV-RC110`
- base: RC109 生产提交 `11159a19`（2026-09-09 14:4xZ 已部署，tag `v0.1.0-rc109-signed`）+ RC109 分支后续文档 `7e667a2`
- worktree: `K:/发票/wt-XM-INV-RC110`（`web/node_modules` 是指向 `wt-XM-INV-RC107` 的 junction，锁文件未变）
- 内容（在 RC109 之上）：
  - `ai/claude/XM-INV-PENDING-RECON-IMPL`：`7bc8b80` → … → `7e95f42`（RC108 里的全部）→ `fa36478`（softfail 只撤销本事务写下的额度，RC108 影子根因一）
    → `47448aa`（合成三态 inserted / existing_identical / existing_conflicting + `synthesis_conflict` 审计；§4.1 拆 A/B 两次跑；三个指标口径）
    → `47ce4b8`（交接单提交表）；合并 `ff13951`、此前 `7eb0389`
  - `ai/claude/XM-INV-SHADOW-BINDING-IMPL`：`af788cd`（9c 的登录 origin 改从 api 容器 `printenv` 取——生产 `.env.production` 没有这两个键，值来自 compose `:-` 默认；
    工具错误提示同步）；合并 `ebf865c`
  - `ea577e9`（cherry-pick `1c47fcb`：手册 `invoice_eligibility_repair()` 里 `--pull never`，过发布门禁静态检查）
  - 发布身份 RC109→RC110 改名（三份门禁脚本 + 手册 12 行发布命令；9c 的 tools 镜像下限**保持 rc109**，因为二进制已随 RC109 上线），**先于打 tag**
- **无迁移**（`backend/migrations` 最后仍是 `0032`）；回滚 = 单步 `deploy/roll-forward.sh 11159a19…`（RC109）

## 为什么发

RC108 的对账中自锁片在影子评估上 not_ready（见 `RELEASE-RC108.md`「影子评估结果」）。两条根因都已处置：
1. 用户 34 的 `PROJECTION_FAILED`：softfail 撤销暂定额度时没区分「本事务刚写的」与「以前已存在的同键额度」，撞 `consumption_allocations` 的 RESTRICT 外键。
   `fa36478` 只在本事务真的插入时才撤销；`47448aa` 把同键旧行分成 identical / conflicting，conflicting 写独立审计、绝不删旧行、账号不错放。
2. 用户 12 没退出：不是代码缺陷，是影子评估跑法——`--finalization-window-lag 1h` 让窗口回落到 `[F, F]`，恰等于 F 的周期 0 个，候选为空。
   交接单 §4.1 改为两次跑：A 保留评估只开窗口（验 C2 与 12 退出）；B 清评估重判（验 C5 与 34 不再报错）。影子评估不覆盖 C1（它直接写作业绕过排队谓词）。

## 门禁（全部实测，UTC）

| 门禁 | 开始 | 结束 | 耗时 | 结果 |
|---|---|---|---|---|
| 源码门禁第一次（树 `c0e4be5`；`rc110-gate`） | 15:00:37 | 15:10:16 | 9m39s | PASS（exit 0） |
| 源码门禁第二次（并入影子绑定文档尾巴 `312f647`/`16d7ce0` 后，树 `3fcb3a5`；`rc110-gate2`） | 15:11:19 | 15:19:40 | 8m21s | PASS（exit 0） |
| 源码门禁第三次（并入 `9bb88f6` 收尾文档后，最终树 `9bcf083`；`rc110-gate3`） | 15:22:11 | 15:30:30 | 8m19s | **PASS**（exit 0）—— tag 打在本行之后的记录提交上 |
| 签名 tag `v0.1.0-rc110-signed` → `277063c`（`git verify-tag` Good） | 15:31 | 15:31 | | OK |
| 镜像门禁（`wt-XM-INV-AUTOLOGIN` detached 到 `277063c`，`run-rc110-image-gate.ps1`，`rc110-imagegate`）+ 两次产物验证 | 15:31:52 | 15:43:44 | 11m52s | exit 42（唯一预期值）；两次验证 exit 0；`release/0.1.0-rc110-exact1` |
| `ssh-keygen -Y sign` 签 `SHA256SUMS` + bash 重定向验签 | 15:44 | 15:44 | 1s | Good；66/66；manifest `source.gitHead` = `277063c…` |
| 传输包 → `incoming/rc110/`，服务器侧 sha256 三项 OK | 15:44 | 15:47:52 | ~4m | OK |
| stage2（9 imageId 对清单；tools 含 `invoice-account-bind`；服务器验签 Good、66/66；源码 `releases/277063c…/source`；env 仅改 tag；账本 top 仍 `0032`） | 15:48:22 | 15:48:42 | 20s | OK；记录目录 `deployment-records/rc110-deploy-20260909T154819Z/` |
| **影子评估 A**（备份 `invoice-20260909T143200Z`；`--reproject-all --finalization-window --finalization-window-provable`，无 lag / 无 reevaluate；rehearsal `20260909T154900Z-3668424`） | 15:48:52 | 15:55:47 | 6m55s | verdict ready、0 错误、0 新冻结原因；**12 未退出：F 前后都是 14:11:03，窗口零宽**——根因是冻结副本上 bound=min_wm−900s 只比 F 高几十秒（四流水位落后约 5 分钟，生产 finalize 已把 F 顶到前沿），(F,bound] 内无周期；生产上 (F,14:32] 有 19 个周期。不是 provable 退化、不是代码缺陷；工具改动（允许负 lag 封顶 min_wm）留下一版 |
| **影子评估 B**（A 的参数 + `--reevaluate-evidence`；rehearsal `20260909T155639Z-3714182`） | 15:56:39 | 16:05:25 | 8m46s | verdict ready；清掉并重判 **9,101** 条评估、2 轮、**0 投影错误、0 失败账号、0 新冻结原因**；9 个 active 账号状态与 overage 逐字不变，2222 仍 frozen；12 仍 pending（B 里属预期）；**34 的作业停在 `BALANCE_PROOF_PENDING`（2 次尝试，projection_version 未变）——它在冻结副本上没跑到评估**，所以 B 没有直接演到 RC108 那条 FK 路径；该路径由 `fa36478` 的集成用例（逐字复现同一错误串后修复、变异 M37 红）与外部复核的独立复现覆盖 |

**发布判据（负责人 2026-09-10 00:1x 决定今晚上线）**：B 证明评估器改动对全部 9 个 active 账号与 2222 无副作用、无错误；12/34 两个目标账号在冻结副本上分别因零宽窗口与证明待定没有被演到，
其行为由单测/集成测试与复审推演覆盖，上线后直接观察（12 应在下一个 balances 周期后派生并退出；34 的下两张真实检查点应合成并 matched）。
残余风险：若 34 的实时路径仍出错，形状是该账号作业 PROJECTION_FAILED → 8 次后 dead → readyz 503（不影响客户流量）；处置是单步回滚 RC109。

## 顺序

1. 源码门禁 → tag → 镜像门禁 → 两次产物验证 → 签名 → bundle → 传输 → stage2（脚本 `rc110-*.sh`）
2. 影子评估 A、B（服务器上，各约 7 分钟；age 身份暂存 `/dev/shm/rbk`、跑完 shred），逐账号 diff 对照交接单 §4.1
3. **问负责人一次** → 签名备份 → 负责人跑 `deploy/roll-forward.sh`
4. 部署后观测：18 容器 rc110、readyz 200、账本 top 仍 `0032`、Dead=0；用户 12 是否在下一个 balances 周期后出现 `idle_reevaluation:true` 证明并转 active；
   用户 34 的下两张真实检查点是否合成并 matched；出现 `eligibility.balance_blip.rebaselined` 或 `synthesis_conflict` 审计即停下看

## 补记：2026-09-09 部署结果（UTC；+08 加 8 小时）

- 负责人 16:1x「走」→ 签名备份 16:17:26–16:21:42（`invoice-20260909T161726Z`，Good，暂存私钥已 shred，服务由脚本拉起，18×rc109、readyz 200）
- 负责人执行 `deploy/roll-forward.sh 277063cc…`，16:3x 报「跑完了」
- 核实（16:35Z）：**18 个容器全部 `0.1.0-rc110`**，healthz/readyz 200；账本 top 仍 `0032`；无死信、无待处理事件；投影作业队列空；api 近 5 分钟 ERROR 0。
- **用户 12（acdcdce9…）已退出「对账中」→ `active`**：16:23:45.9Z（部署后约一分钟、第一个 balances 周期）派生 `as_of=16:03:24` 的闲置结转证明（复述 deficit 3,610,140）→ matched →
  `eligibility.pending_reconciliation.exited` → active。结转证明 5 → 6。这正是 C1+C2 的设计路径，与复审推演一致。
- **用户 34（40bd883d…）仍 `not_invoiceable_pending_reconciliation`，属预期**：它最后一张真实检查点在 15:48:55Z（此后停止消费），部署后没有新事实、也没有新投影；
  连击 0，按 D2(a) 闲置派生只对连击 ≥1 的账号，所以它不会被闲置路径放出。出路是 C5：下一次消费产生的两三张真实检查点 → 正向延后 → 下一张确认 → 合成 ≈ 加款额 + 当时 deficit
  → matched → 连击 1 → 再一张 matched → 连击 2 → active。届时看审计 `synthesis_conflict` / `balance_blip.rebaselined` 是否出现（出现即停下看）。
- 状态分布：active 11（原 9 + 2823 + 12）/ frozen 1（2222）/ pending 1（34）。
- 记录目录 `deployment-records/rc110-deploy-20260909T154819Z/`（release-manifest、TRANSFER-SHA256SUMS、backup 日志、影子评估 A/B 报告与摘要、containers-after、deploy-record）。

## 回滚

- 无迁移：单步 `deploy/roll-forward.sh 11159a19a828399a93589c4ee433bce84af1b568`（RC109 九个镜像仍在主机上）。
  副作用：用户 12 不再自动重评（已退出者保持 active）；用户 34 会被旧的无符号口径重新判回 pending。
