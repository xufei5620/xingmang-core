# OPS-07 — correct historical tokenmap permission prerequisite

status: FIXED_TARGETED_VERIFIED
branch: ai/codex/XM-FULL-AUDIT-platform-docs-20260911
base: 4b02255e480e3bdc34994145b1ba9fbbf6762013

只修改本项文档及回归；未执行生产、真实配置、数据库、凭据、SSH或CPA操作。

## 改动

- `platform/docs/runbooks/REQLOG-RECORDER.md`
- `platform/tests/runbooks/reqlog_permission_doc_test.py`

## 验证

| 阶段 | UTC 起止 | exit |
|---|---|---:|
| red | 2026-09-10T17:47:39.663772+00:00 → 2026-09-10T17:47:39.780622+00:00 | 1 |
| green | 2026-09-10T17:47:39.782478+00:00 → 2026-09-10T17:47:39.873945+00:00 | 0 |
| mutation-1 | 2026-09-10T17:47:39.875702+00:00 → 2026-09-10T17:47:39.983844+00:00 | 1 |
| restored-green-1 | 2026-09-10T17:47:39.985693+00:00 → 2026-09-10T17:47:40.081987+00:00 | 0 |

完整命令、stdout/stderr、源文件字节哈希：`G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/OPS-07`。变异只恢复本项旧文档/指定错误片段，try/finally 恢复准确固定字节后重跑绿色。

## 限制

只验证实际文档片段与其消费者/前置契约；边界使用新建合成夹具。运行时行为、生产证据及负责人批准不由本检查替代；全量门禁由主代理统一执行。
