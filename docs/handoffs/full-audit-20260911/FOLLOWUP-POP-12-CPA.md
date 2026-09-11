# POP-12-CPA — 专属测试加固完成

本次仅修改普通 CPA 安全测试及同目录测试辅助脚本；没有修改部署脚本、安装器、服务/定时器、Compose、CPA 连接器或生命周期。原“不碰 CPA”暂停由用户本次明确授权测试修复覆盖；不据此扩大功能或服务器操作权限。

旧测试只看关键文本存在：将安装块移到 app-up 后仍 exit 0；要求变异返回 1 的元验证因此 RED。现改为对实际安装到 app-up 片段记录合成调用序列，要求成功安装恰好一次且先于一次应用启动；安装失败（即使打印 PASS）或缺少验证证据必须非零退出且不启动应用。

仅执行隔离副本中的生命周期片段：实际安装器、snapshot 二进制、Compose、systemctl、UID、chmod 边界均由惰性替身截获。真实 grep/tail 仅读取刚生成的合成日志。没有运行生产入口、服务器、容器、实际快照/数据库或读取任何密钥。

## 变更与门禁接线

- `platform/tests/security/cpa-snapshot-runtime-wiring.test.sh` SHA256 `af1d563ab65f08f93373be25587d78facbce23c543be2f5e8117db9bf53cb704`
- `platform/tests/security/cpa-snapshot-install-order.test.py` SHA256 `3e97ef6bf54e7c5cc3f7f7804e3953a2ad31bb57b06aa1bae44cf1309b0b5f02`

原正常门禁已有接线：`platform/scripts/ci-local.sh:114` 及 `platform/.github/workflows/ci.yml:39` 枚举 `tests/security/*.test.sh`。原 `.test.sh` 现在调用辅助 Python 文件；未新增门禁框架。

Linux/WSL 普通入口实际通过。Git Bash + Windows Python 普通入口也实际通过：由调用 Bash 显式传入 bash/grep/tail 路径，避免 PATH 选到 WSL launcher；转换 Git Bash 路径。当前机器 python3 是 WindowsApps 别名，因此 Git Bash 检查在证据目录使用一次性 python3 shim，明确委派到已有 `C:\Python314\python.exe`，没有修改全局配置。正式使用 Git Bash 仍须 python3 可用。

## 实测结果（全部时间为 UTC）

| 检查 | 开始 | 结束 | 实际 exit | 期望 exit |
|---|---|---|---:|---:|
| old-baseline | 2026-09-10T20:09:31.376872+00:00 | 2026-09-10T20:09:36.766792+00:00 | 0 | 0 |
| red-old-test-must-detect-late-install | 2026-09-10T20:09:36.863319+00:00 | 2026-09-10T20:09:37.270138+00:00 | 0 | 1 |
| green-fixed-test | 2026-09-10T20:15:49.301718+00:00 | 2026-09-10T20:15:54.517438+00:00 | 0 | 0 |
| gitbash-normal-entry | 2026-09-10T20:15:55.086727+00:00 | 2026-09-10T20:15:57.328989+00:00 | 0 | 0 |
| mutation-install-after-app | 2026-09-10T20:16:31.610553+00:00 | 2026-09-10T20:16:36.606307+00:00 | 1 | 1 |
| mutation-commented-install | 2026-09-10T20:16:36.656859+00:00 | 2026-09-10T20:16:37.141699+00:00 | 1 | 1 |
| mutation-missing-app-call | 2026-09-10T20:16:37.192272+00:00 | 2026-09-10T20:16:37.650151+00:00 | 1 | 1 |
| mutation-duplicate-app-call | 2026-09-10T20:16:37.700083+00:00 | 2026-09-10T20:16:38.184880+00:00 | 1 | 1 |
| mutation-ignore-installer-error | 2026-09-10T20:16:38.233197+00:00 | 2026-09-10T20:16:38.738545+00:00 | 1 | 1 |
| mutation-ignore-verification-evidence | 2026-09-10T20:16:38.786400+00:00 | 2026-09-10T20:16:39.265085+00:00 | 1 | 1 |
| mutation-early-success | 2026-09-10T20:16:39.313776+00:00 | 2026-09-10T20:16:39.753929+00:00 | 1 | 1 |
| mutation-late-error | 2026-09-10T20:16:39.809011+00:00 | 2026-09-10T20:16:40.275853+00:00 | 1 | 1 |
| mutation-missing-phase | 2026-09-10T20:16:40.322923+00:00 | 2026-09-10T20:16:40.766998+00:00 | 1 | 1 |
| mutation-duplicate-phase | 2026-09-10T20:16:40.815411+00:00 | 2026-09-10T20:16:41.239234+00:00 | 1 | 1 |
| restored-green | 2026-09-10T20:16:41.339357+00:00 | 2026-09-10T20:16:41.856261+00:00 | 0 | 0 |
| normal-entry-helper-unavailable | 2026-09-10T20:16:49.231601+00:00 | 2026-09-10T20:16:49.643638+00:00 | 1 | 1 |
| meta-bypassed-helper-must-not-hide-late-install | 2026-09-10T20:16:49.697505+00:00 | 2026-09-10T20:16:50.096244+00:00 | 0 | 1 |

最终字节上 8 个行为变异 + 2 个边界缺失/重复变异均在预期断言处 exit 1；还原入口 exit 0。helper 不可用使原门禁 exit 1。另故意绕过 helper 后，错误安装顺序再次漏检（正常入口 exit 0、期望 1），元验证按预期 RED，证明变异核验走普通门禁入口而非绕开它直接测试 helper。这两处 RED 是所需缺陷/元验证证据，不是最终实现失败。

完整命令、环境、UTC、退出码、源码哈希、stdout/stderr 均在各运行目录的 `result.json` 与日志；最终变异清单 `portable/mutations.json`，不变性证明 `portable/preservation.json`，普通入口元证明 `portable/normal-entry-metaproof.json`。前一版仅 Linux 证据保留在根目录，最终实现采用 portable/ 证据。

## 限制

本地替身只证明当前 CPA 部署测试能拦住既有安装顺序错误和失败放行；不证明真实 snapshot 发布、systemd 状态、挂载权限、线上观测或服务健康。没有运行全套门禁。生产生命周期保持原样；若将来合法调整阶段边界，需明确更新此隔离夹具。

完整证据索引：[result.json](<G:/xingmang/logs/full-audit-20260911-followup/POP-12-CPA/result.json>)；[最终变异](<G:/xingmang/logs/full-audit-20260911-followup/POP-12-CPA/portable/mutations.json>)；[运行代码保留哈希](<G:/xingmang/logs/full-audit-20260911-followup/POP-12-CPA/portable/preservation.json>)。
