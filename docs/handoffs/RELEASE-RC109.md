# RELEASE RC109 —— RC108 去掉对账中自锁那片后的重发：用户端不露原始单位 + 影子绑定 CLI + 手册回滚章节

- status: 凑片完成、改名完成；源码门禁待跑；未 tag、未传输、未部署；**上线前必须负责人拍板**（RC108 影子评估 not_ready 后的备用方案）
- branch: `ai/claude/XM-INV-RC109`
- base: RC107 生产提交 `b3ded699`（2026-09-09 08:0xZ 已部署，tag `v0.1.0-rc107-signed`）+ RC107 分支后续文档 `f80d3f4`
- worktree: `K:/发票/wt-XM-INV-RC109`（`web/node_modules` 是指向 `wt-XM-INV-RC107` 的 junction，锁文件未变）
- 内容（并入顺序，与 RC108 相同但**不含** `ai/claude/XM-INV-PENDING-RECON-IMPL`）：
  - `ai/claude/XM-INV-UNIT-DISPLAY-USERONLY`：`3d64314`
  - `ai/claude/XM-INV-RUNBOOK-ROLLBACK-VERIFYCOMMIT`：`f02163f`
  - `ai/claude/XM-INV-SHADOW-BINDING-IMPL`：`3f39099` → … → `fc8e17d`（两轮复审经过见 `RELEASE-RC108.md`，本文不重复）；合并 `8d8009d`
  - 发布身份 RC107→RC109 改名（三份门禁脚本 + 手册 12 行发布命令 + 9c 镜像下限改为 rc109），**先于打 tag**
- **无迁移**（`backend/migrations` 最后仍是 `0032`）；回滚 = 单步 `deploy/roll-forward.sh b3ded699`
- **不需要影子评估**：本次没有触碰手册 11.2「when」清单里的评估器 / 投影 / 修复逻辑 / 相关迁移（影子绑定只改 identity / source_sync / application / cmd）

## 为什么是 RC109 而不是 RC108

RC108（tag `v0.1.0-rc108-signed` = `c347396`）四片齐全、源码门禁与镜像门禁都过、已传输并 stage2，但服务器上的影子评估判 **not_ready**
（rehearsal `20260909T134104Z-2914553`，备份 `invoice-20260909T080659Z`）：

1. 用户 34 的投影报 `PROJECTION_FAILED`：`consumption.go` SOFTFAIL 路径的 `DELETE FROM source_credit_events`（基线就有）撞上
   `consumption_allocations_credit_event_id_fkey` 的 RESTRICT——`--reevaluate-evidence` 清了评估行却留下旧合成额度（交接单 §4.1 自列的影子伪象 1），
   重判同一张检查点时新合成被 ON CONFLICT 挡住、softfail 去删已有几千条分配引用的旧额度。生产是否可达待实现方证实；「删有引用的额度」本身是潜在缺陷。
2. 用户 12 跑了 8 个投影版本仍 `not_invoiceable_pending_reconciliation`，`accounts_released=0`，与交接单 §4.1 预期（→ active）不符，根因未明。

按 RC108 记录写死的判据（不 ready 或 diff 超出清单 → 停下，不部署），RC108 不部署；对账中自锁那片留在分支上继续查根因，
修好并重新通过影子评估后另发。其余三片与评估器无关，先以 RC109 发出。RC108 的 tag、镜像与 `incoming/rc108` 保留作记录。

## 门禁（全部实测，UTC）

| 门禁 | 开始 | 结束 | 耗时 | 结果 |
|---|---|---|---|---|
| 源码门禁 `scripts/verify.ps1`（经 `release/run-rc109-gate.ps1` + `run-detached.ps1`；`rc109-gate`，树 `2157b8f`） | 13:54:08 | 14:02:21 | 8m13s | **PASS**（exit 0；postgresstore 236.6s） |
| 签名 tag `v0.1.0-rc109-signed` | 待打 | | | |
| 镜像门禁（`wt-XM-INV-AUTOLOGIN` detached 到 tag，`run-rc109-image-gate.ps1`） | 待跑 | | | 预期 exit 42 |
| 产物验证（普通 + `-RequireTransferReady -SignedReleaseTag v0.1.0-rc109-signed`） | 待跑 | | | |
| `ssh-keygen -Y sign` 签 `SHA256SUMS` | 待签 | | | |
| 传输 + stage2 | 待做 | | | |

## 顺序

1. 源码门禁 → tag → 镜像门禁 → 两次产物验证 → 签名 → bundle → 传输 → stage2（同 RC108，脚本改 rc109）
2. **问负责人一次**（「走」）→ 签名备份（约 4 分钟，停/起服务）→ 负责人跑 `deploy/roll-forward.sh`
3. 部署后观测：18 容器 rc109、readyz 200、账本 top 仍 `0032`、Dead=0；用户端两格显示「暂无法换算」或换算值、无原始单位；
   tools 镜像含 `invoice-account-bind`；随后按手册 9c 对候选客户 2823 做 dry-run（`--apply` 前再问负责人）

## 回滚

- 无迁移：单步 `deploy/roll-forward.sh b3ded699`（RC107 九个镜像仍在主机上）。已代绑定的账号在旧代码下仍是合法行，客户登录照常认领。
