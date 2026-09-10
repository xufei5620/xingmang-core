# OPS-05 — preserve shadow tool exit codes in documented invocation

status: FIXED_TARGETED_VERIFIED
branch: ai/codex/XM-FULL-AUDIT-platform-docs-20260911
base: b05cee9d79bb16aac9623a417acf8dab0fb8faae

只修改本项文档及回归；未执行生产、真实配置、数据库、凭据、SSH或CPA操作。

## 改动

- `platform/docs/runbooks/SHADOW-COMPARE.md`
- `platform/tests/runbooks/shadow_exit_test.py`

## 验证

| 阶段 | UTC 起止 | exit |
|---|---|---:|
| red | 2026-09-10T17:45:02.845592+00:00 → 2026-09-10T17:45:04.882892+00:00 | 1 |
| green | 2026-09-10T17:45:04.884438+00:00 → 2026-09-10T17:45:05.575782+00:00 | 0 |
| mutation-1 | 2026-09-10T17:45:05.577659+00:00 → 2026-09-10T17:45:06.299576+00:00 | 1 |
| restored-green-1 | 2026-09-10T17:45:06.301250+00:00 → 2026-09-10T17:45:06.952539+00:00 | 0 |

完整命令、stdout/stderr、源文件字节哈希：`G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/OPS-05`。变异只恢复本项旧文档/指定错误片段，try/finally 恢复准确固定字节后重跑绿色。

## 限制

只验证实际文档片段与其消费者/前置契约；边界使用新建合成夹具。运行时行为、生产证据及负责人批准不由本检查替代；全量门禁由主代理统一执行。
