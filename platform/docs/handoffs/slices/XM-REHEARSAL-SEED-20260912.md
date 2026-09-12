# XM-REHEARSAL-SEED-20260912

依据负责人本轮明确授权，在冻结 D 副本中生成合成员工、TOTP、SUB/NEW 用户、验证邮箱/抬头与合成已消费 wallet 资格；生产 E 改为五项公开验收，真人登录由负责人执行。基于 `a0d323052ecb96d2e7e63ea4902e2207c63d4d01`，本切片不推送、不操作服务器。

新增两个工具都在既有 invoice-tools 镜像中，不增加镜像角色或依赖：HTTPS fixture provider 与离线 seed SQL 工具。后者通过已有平台 Argon2 依赖和 `rehearsal_tools` build tag 构建。产品登录/SSRF/额度策略未改，只有 D-only 数据与操作器配置发生变化。契约详见根目录 `deploy/rehearsal-unified/SEED.md`。

D 实际核 CID、镜像、owner、新卷、内网与 Unix 数据库身份；拒绝 PG socket 覆盖及 PGHOSTADDR/PGSERVICE 重定向，恢复/播种都固定已核 CID。随机凭据和 fixture TLS 私钥仅在真正的 Docker tmpfs，Windows 主控仅在内存取指定字段。日志写入失败不能阻止真实擦除，但最终仍失败；擦除失败保留原 holder/tmpfs，不把重新挂载空卷当作已 shred。C1/C2 原生产事实不能由播种结果覆盖。原 11 闩与一个 300 秒预算、D 十二步扫描/下载写链保留。

E 五项分别为 readyz、页面 shell、旧入口拒绝、实际两表 dead=0、实际 ledger-top/full ledger 对照。原 permissions 与 migration digest 约束保留，报告 `human_login_verified:false`。已有 dead 直接失败，不清队列或改业务记录。

另将 lifecycle 与 canonical verifier 的 SHA256 改为等效分块读取，兼容服务器 Python 3.10；没有降低 Python/镜像/manifest 校验，空串、abc、2,097,427 字节三向量的新旧 SHA 完全一致。

本地证据位于 `G:/xingmang/logs/unified-server-execute-20260912/rehearsal-seed/`：

- 原失败测试：01/03/06；修复后对应测试与完整 unified verifier 通过。
- provider 11、shred 6、seed SQL 8、嵌套 TOTP 1、身份平台投影 1、操作器 9 个有效隔离变异，恢复通过。
- 真实新 PG18 约束验证 `seed-sql/actual-pg/r2/RESULT.json`：2026-09-12 12:23:18.095301Z → 12:23:36.372650Z。原平台 55/开票 32 迁移、两域实际 seed SQL 均通过；6 个错误 OID/nonce/project 的 SQL 均拒绝并无残留行。2 平台身份投影、2 profiles/verified_emails/active_accounts/funding_lots/usage 和消费镜像均正确，invoice_requests=0；仅本次容器卷归属核定后清理，未使用现有测试库。
- 独立复审第一版 e590 的 3 个 P1 已保留原 FAIL 报告。socket/TCP、日志盘满阻断擦除、失败时删除 holder 四反例修后通过；18-REVIEW-MUTATIONS 的四个回退变异均红，旧 e590 源文件精确归档不覆盖。
- 17-REVIEW-OPERATOR-RESTORED：206 个操作器测试，13:07:38.581867Z → 13:07:47.208432Z，exit 0。16 的 5 个旧夹具缺 services 字段失败记录保留；只补真实 descriptor 字段后全套重跑通过。
- 真实 SecretProvider 嵌套 TOTP、provider race/vet/Linux 构建、专用 seed tag 测试及原 runbook 顺序用例均有原 UTC/exit 记录。
- libpq 实际短连接 `libpq-actual-01/RESULT.json`：13:18:16.870814Z → 13:18:21.031720Z；新建独立 PG18，旧 PGSERVICE 空串组合实测 exit 2，产品 `local_postgres_exec` 的真正 `env -u` 参数在刻意污染环境后仍返回 `1|postgres|t|t`、exit 0。只清理本次核定容器卷，未复用现有测试数据库。

这是代码与本地数据库约束证据。完整的新 seed-enabled 本地 D/E、最终 HEAD 的七门禁/11 镜像及服务器 D/E 由主任务继续执行，不能用旧 fixture 或 SQL 生成测试替代。本切片未声称已经部署、真人登录通过或服务器运行通过；独立复验结果由主任务的 `seed-independent-review/` 外部记录追溯。
