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
| 源码门禁 `scripts/verify.ps1`（经 `release/run-rc110-gate.ps1` + `run-detached.ps1`） | 待跑 | | | |
| 签名 tag `v0.1.0-rc110-signed` | 待打 | | | |
| 镜像门禁 + 两次产物验证 | 待跑 | | | 预期 exit 42 |
| `ssh-keygen -Y sign` 签 `SHA256SUMS` | 待签 | | | |
| 传输 + stage2 | 待做 | | | |
| **影子评估 A**：`--reproject-all --finalization-window --finalization-window-provable`（不带 reevaluate、不带 lag） | 待跑 | | | 先核 `pending_accounts[].requested_through > finalized_through`；12 出现 `idle_reevaluation:true` 结转证明、matched、→ active；其余不变 |
| **影子评估 B**：A 的参数 + `--reevaluate-evidence` | 待跑 | | | 无投影错误；34 作业完成、差异可由有符号口径解释；12 在 B 里不退出属预期；34 的合成条数与生产不可比（伪象） |

## 顺序

1. 源码门禁 → tag → 镜像门禁 → 两次产物验证 → 签名 → bundle → 传输 → stage2（脚本 `rc110-*.sh`）
2. 影子评估 A、B（服务器上，各约 7 分钟；age 身份暂存 `/dev/shm/rbk`、跑完 shred），逐账号 diff 对照交接单 §4.1
3. **问负责人一次** → 签名备份 → 负责人跑 `deploy/roll-forward.sh`
4. 部署后观测：18 容器 rc110、readyz 200、账本 top 仍 `0032`、Dead=0；用户 12 是否在下一个 balances 周期后出现 `idle_reevaluation:true` 证明并转 active；
   用户 34 的下两张真实检查点是否合成并 matched；出现 `eligibility.balance_blip.rebaselined` 或 `synthesis_conflict` 审计即停下看

## 回滚

- 无迁移：单步 `deploy/roll-forward.sh 11159a19a828399a93589c4ee433bce84af1b568`（RC109 九个镜像仍在主机上）。
  副作用：用户 12 不再自动重评（已退出者保持 active）；用户 34 会被旧的无符号口径重新判回 pending。
