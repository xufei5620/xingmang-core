# IDEP-009 补充修复

状态：已修复，定向验证通过。

恢复成功提示原先固定写 source_states=4；现在从已完成检查的 source_directories 数组取长度。Compose 注释去掉过时的四项计数。

先运行失败检查再修改：旧版本三个列表场景均报告计数错误；修后实际列表输出 10，增加一项输出 11，缩成一项输出 1，public_tables=37 仍正确。固定 4、固定 10、误取首个名称长度、交换两项输出、恢复过时注释的 5 个变体全部 exit 1；恢复后 exit 0。

验证仅执行源文件中真实的数组声明与最终 printf；没有运行恢复流程、Docker 或读取密钥。既有运行环境、资源清理和容量测试使用其原有本地合成夹具。

| 检查 | UTC 开始 | UTC 结束 | 退出码 | 预期 | 日志 |
|---|---|---|---:|---:|---|
| red-before | 2026-09-10T20:08:34.395892+00:00 | 2026-09-10T20:08:34.551998+00:00 | 1 | 1 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/red-before.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/red-before.stderr.log>) |
| green-after | 2026-09-10T20:08:50.277863+00:00 | 2026-09-10T20:08:50.453202+00:00 | 0 | 0 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/green-after.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/green-after.stderr.log>) |
| mutant-fixed-four | 2026-09-10T20:09:44.192669+00:00 | 2026-09-10T20:09:44.448407+00:00 | 1 | 1 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-fixed-four.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-fixed-four.stderr.log>) |
| mutant-fixed-ten | 2026-09-10T20:09:44.539926+00:00 | 2026-09-10T20:09:44.739818+00:00 | 1 | 1 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-fixed-ten.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-fixed-ten.stderr.log>) |
| mutant-first-name-length | 2026-09-10T20:09:44.836486+00:00 | 2026-09-10T20:09:45.038797+00:00 | 1 | 1 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-first-name-length.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-first-name-length.stderr.log>) |
| mutant-swapped-values | 2026-09-10T20:09:45.130612+00:00 | 2026-09-10T20:09:45.329787+00:00 | 1 | 1 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-swapped-values.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-swapped-values.stderr.log>) |
| mutant-stale-compose-comment | 2026-09-10T20:09:45.425099+00:00 | 2026-09-10T20:09:45.616009+00:00 | 1 | 1 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-stale-compose-comment.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutant-stale-compose-comment.stderr.log>) |
| restored-green | 2026-09-10T20:09:45.704923+00:00 | 2026-09-10T20:09:45.888362+00:00 | 0 | 0 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/restored-green.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/restored-green.stderr.log>) |
| restore-syntax | 2026-09-10T20:10:20.445235+00:00 | 2026-09-10T20:10:20.478151+00:00 | 0 | 0 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-syntax.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-syntax.stderr.log>) |
| restore-runtime-env | 2026-09-10T20:10:20.546515+00:00 | 2026-09-10T20:10:20.873152+00:00 | 0 | 0 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-runtime-env.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-runtime-env.stderr.log>) |
| restore-cleanup | 2026-09-10T20:10:20.943752+00:00 | 2026-09-10T20:10:28.159128+00:00 | 0 | 0 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-cleanup.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-cleanup.stderr.log>) |
| restore-capacity | 2026-09-10T20:10:28.396110+00:00 | 2026-09-10T20:10:36.514635+00:00 | 0 | 0 | [输出](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-capacity.stdout.log>) / [错误](<G:/xingmang/logs/full-audit-20260911-followup/gates/restore-capacity.stderr.log>) |

完整命令与复现脚本：

- [检查脚本](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/check-summary.py>)；[变异脚本](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/mutate-summary.py>)。
- 每个表格记录的同名 `.json` 保存实际 command、cwd、UTC、退出码和目标失败文本。
- [正文保留哈希](<G:/xingmang/logs/full-audit-20260911-followup/IDEP-009/preservation.json>)：去除唯一修改的最终 printf 后，恢复脚本前后 SHA-256 同为 `2dbe8b36fe9224ad7d6965ffb398d2567833916c08d02dc5e79a4deef00aaa80`；Compose 去除注释后同为 `ed77a184e7c74653958347a52619beaa72f8c6a97c99c830a161fbc27cb5db16`。

本次未重跑应用后端/前端全套：这些源码及依赖未变；原完整门禁记录保留，本次补充检查覆盖这次计数与注释修改。
