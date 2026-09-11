# CONTRACT-DOC-01 补充修复

状态：已修复，文档核对与定向变异检查通过。

三份 CR 增加一个带 UTC 截止与出处的历史状态摘要，旧状态及原始业务正文放在明确的历史范围内。CR-0007/0009 按已有 RC73/RC79 记录说明历史交付；CR-0008 区分实现、文件统计与真实读侧验收，≥99% 运行时指标尚未核实。

四份文档修正均基于本地既有记录；没有重验生产或补写批准。以下检查是一次性的文档交付核验，未新增永久关键词门禁。

| 文件 | 原始全文 SHA-256（移除新增摘要后保持相同） |
|---|---|
| [CR-0007-invoice-admin-freeze-queue-operability.md](<G:/xingmang/01-core/platform/docs/change-requests/CR-0007-invoice-admin-freeze-queue-operability.md>) | `570828cf310cbac04b5f4970d1255642d2e0b75d3fae439f144b24eb378acbfb` |
| [CR-0008-reqlog-tokenmap-upstream-user-id.md](<G:/xingmang/01-core/platform/docs/change-requests/CR-0008-reqlog-tokenmap-upstream-user-id.md>) | `3c77b40dd8ab0e6a10350b607212596325a62d49a481d0048c5f6676c0557878` |
| [CR-0009-invoice-admin-user-ledger-view.md](<G:/xingmang/01-core/platform/docs/change-requests/CR-0009-invoice-admin-user-ledger-view.md>) | `2e7a18b6bf391f6c13aa0cfc348bb4c862a6cbc969df304d45316cd7d78e131d` |

| 文件 / 检查 | UTC 开始 | UTC 结束 | 退出码 | 预期 |
|---|---|---|---:|---:|
| CR-0007 / red-original-conflict | 2026-09-10T20:11:23.626004+00:00 | 2026-09-10T20:11:23.695379+00:00 | 1 | 1 |
| CR-0008 / red-original-conflict | 2026-09-10T20:11:23.696994+00:00 | 2026-09-10T20:11:23.764061+00:00 | 1 | 1 |
| CR-0009 / red-original-conflict | 2026-09-10T20:11:23.765551+00:00 | 2026-09-10T20:11:23.838398+00:00 | 1 | 1 |
| CR-0007 / green | 2026-09-10T20:11:23.911662+00:00 | 2026-09-10T20:11:23.966332+00:00 | 0 | 0 |
| CR-0007 / mutation-restore-old-summary | 2026-09-10T20:11:23.967736+00:00 | 2026-09-10T20:11:24.040431+00:00 | 1 | 1 |
| CR-0007 / mutation-drop-history-boundary | 2026-09-10T20:11:24.041786+00:00 | 2026-09-10T20:11:24.130681+00:00 | 1 | 1 |
| CR-0007 / mutation-drop-current-limit | 2026-09-10T20:11:24.132014+00:00 | 2026-09-10T20:11:24.201235+00:00 | 1 | 1 |
| CR-0007 / mutation-drop-whole-history-scope | 2026-09-10T20:11:24.202714+00:00 | 2026-09-10T20:11:24.278415+00:00 | 1 | 1 |
| CR-0007 / mutation-drop-cutoff | 2026-09-10T20:11:24.279753+00:00 | 2026-09-10T20:11:24.349579+00:00 | 1 | 1 |
| CR-0007 / mutation-claim-current-production | 2026-09-10T20:11:24.351056+00:00 | 2026-09-10T20:11:24.423460+00:00 | 1 | 1 |
| CR-0007 / restored-green | 2026-09-10T20:11:24.424390+00:00 | 2026-09-10T20:11:24.472965+00:00 | 0 | 0 |
| CR-0008 / green | 2026-09-10T20:11:24.478511+00:00 | 2026-09-10T20:11:24.531837+00:00 | 0 | 0 |
| CR-0008 / mutation-restore-old-summary | 2026-09-10T20:11:24.533108+00:00 | 2026-09-10T20:11:24.602342+00:00 | 1 | 1 |
| CR-0008 / mutation-drop-history-boundary | 2026-09-10T20:11:24.604001+00:00 | 2026-09-10T20:11:24.674384+00:00 | 1 | 1 |
| CR-0008 / mutation-drop-current-limit | 2026-09-10T20:11:24.675790+00:00 | 2026-09-10T20:11:24.748978+00:00 | 1 | 1 |
| CR-0008 / mutation-drop-whole-history-scope | 2026-09-10T20:11:24.750611+00:00 | 2026-09-10T20:11:24.826321+00:00 | 1 | 1 |
| CR-0008 / mutation-drop-cutoff | 2026-09-10T20:11:24.827980+00:00 | 2026-09-10T20:11:24.904030+00:00 | 1 | 1 |
| CR-0008 / mutation-claim-current-production | 2026-09-10T20:11:24.905396+00:00 | 2026-09-10T20:11:24.982803+00:00 | 1 | 1 |
| CR-0008 / restored-green | 2026-09-10T20:11:24.983677+00:00 | 2026-09-10T20:11:25.036820+00:00 | 0 | 0 |
| CR-0009 / green | 2026-09-10T20:11:25.042791+00:00 | 2026-09-10T20:11:25.176844+00:00 | 0 | 0 |
| CR-0009 / mutation-restore-old-summary | 2026-09-10T20:11:25.178167+00:00 | 2026-09-10T20:11:25.251692+00:00 | 1 | 1 |
| CR-0009 / mutation-drop-history-boundary | 2026-09-10T20:11:25.253562+00:00 | 2026-09-10T20:11:25.329089+00:00 | 1 | 1 |
| CR-0009 / mutation-drop-current-limit | 2026-09-10T20:11:25.330657+00:00 | 2026-09-10T20:11:25.407359+00:00 | 1 | 1 |
| CR-0009 / mutation-drop-whole-history-scope | 2026-09-10T20:11:25.408972+00:00 | 2026-09-10T20:11:25.487845+00:00 | 1 | 1 |
| CR-0009 / mutation-drop-cutoff | 2026-09-10T20:11:25.489756+00:00 | 2026-09-10T20:11:25.569120+00:00 | 1 | 1 |
| CR-0009 / mutation-claim-current-production | 2026-09-10T20:11:25.570739+00:00 | 2026-09-10T20:11:25.648945+00:00 | 1 | 1 |
| CR-0009 / restored-green | 2026-09-10T20:11:25.649824+00:00 | 2026-09-10T20:11:25.704575+00:00 | 0 | 0 |

完整命令、stdout/stderr、出处哈希和本地 Git 对象核对：[results.json](<G:/xingmang/logs/full-audit-20260911-followup/CONTRACT-DOC-01/results.json>)。
检查脚本：[history-doc-repair.py](<G:/xingmang/logs/full-audit-20260911-followup/history-doc-repair.py>)，只使用 `check` 子命令复验文档；原始阶段的编辑入口已执行，不应重复执行。

每份旧文档先因无边界的矛盾摘要 exit 1；修后 exit 0；恢复旧摘要、去掉历史边界、删除当前限制、删除全文历史范围、移除 UTC、冒充当前生产已验证等六个副本变异均 exit 1，最后原工作文件复验 exit 0。所有原始全文字节保持不变。
