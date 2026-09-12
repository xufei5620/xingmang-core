# XM-PLATFORM-BACKUP-20260912

负责人已授权建立平台签名备份链。基线 `a0d323052ecb96d2e7e63ea4902e2207c63d4d01`；本切片只提交本地代码、适当验证和台账，服务器操作/合main/推送由主任务执行。

`platform/deploy/backup/backup.sh` 调用同目录Python标准库实现：核定本机Docker和PG/API/worker完整ID、image、项目/服务标签及数据库Unix socket；记录原writer集合后冻结，pg_dump直管age，导出canonical两CSV并在dump前后比对，再以platform-backup/solov-platform-backup-v1签名验签。原writer集合恢复后才发布；manifest最后，文件单链接且拒绝覆盖。没有任意命令hook、DSN/私钥正文输出、明文dump落盘或开票代码修改。

异常恢复不因日志盘满而停止原writer的inspect/start尝试；日志失败仍非零，不发布PASS。活动journal绑定独立before原件SHA/内容及完整writer身份；recover的新日志不覆盖首次失败记录。POSIX文件/目录fsync覆盖before、active、原子JSON和发布/解除标记。两个长期私钥仅路径登记在根 `docs/runbooks/SECRETS-INVENTORY.md` 并链接原平台台账，生产root0400。

证据根：`G:/xingmang/logs/unified-server-execute-20260912/platform-backup/`。

| 验证 | 实测证据 | 结果 |
|---|---|---|
| 首个失败测试 | `01-RED.json`；2026-09-12 11:14:25.247628～11:14:25.324876 UTC | exit1：部分stop失败时缺原集合恢复 |
| 7个定向单元 | `07-RECOVER-UNIT.json`；11:55:27.916981～11:55:28.056863 UTC | exit0：正常/失败恢复、原停worker不启动、ENOSPC、before误改、恢复目录满盘 |
| 最终真实本地整链 | `08-REAL-FINAL.json`；11:56:49.007625～11:57:03.584138 UTC | exit0，真实PG18/age/OpenSSH；解密pg_restore后两CSV与canonical消费者查询逐字节相同；未改consumer |
| 真实producer失败 | `local-r4/RESULT.json`、其events与failure run journal | pg_dump=1、age=0，整链1；API恢复，原先停着worker仍停；无顶层manifest |
| 制品消费/文件形态 | `local-r4/canonical-backup-verification.json`、`canonical-platform-metadata-comparison.json` | unmodified restore.verify_backups验签/hash通过；最终两age/manifest/sig均nlink1 |
| 3个真实工具变异 | `MUTATIONS.json`；11:59:25.679072～11:59:59.116284 UTC | 吞pg_dump失败、错签名namespace、metadata排序漂移均被实际用例检出exit1；工作树源文件不变 |
| 独立对抗复审 | `G:/xingmang/logs/unified-server-execute-20260912/platform-backup-independent-review/REPORT.md` / `RESULT.json` | 最终560558ab…源指纹PASS；before篡改、恢复目录满盘、正常恢复、恢复命令日志满盘4个关键probe均0（安全响应） |

最终 `backup.py` SHA256：`560558ab84caf0ee78714bb3e2ecc4d9080ac9c2a017b3f293f938496a956c5f`。早期本地r1的Windows测试harness关闭stdout方式曾有readerthread警告，后续已修测试harness；r2/r3/r4无该警告。原证据全部保留，最终采用r4。同一原PG官方digest，未改依赖、基础镜像或业务schema。

所有本地临时容器/卷按完整ID及任务归属核对后清理，原业务资源未动。Windows合成测试不证明生产Linux私钥mode/目录掉电持久性；生产模式在服务器执行时仍强制root/Linux/0400及目录fsync，实际证据由主任务补充。本切片未SSH、未推送、未执行生产备份。
