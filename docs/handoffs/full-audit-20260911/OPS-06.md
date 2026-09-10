# OPS-06 — preserve archive env prerequisite during teardown

status: FIXED_TARGETED_VERIFIED
branch: ai/codex/XM-FULL-AUDIT-platform-docs-20260911
base: 97af6593cfa0eab89ee5658c700063be5f49a92c

只修改本项文档及回归；未执行生产、真实配置、数据库、凭据、SSH或CPA操作。

## 改动

- `platform/docs/runbooks/AUDIT-ARCHIVE.md`
- `platform/tests/runbooks/archive_fixture_env_test.py`

## 验证

| 阶段 | UTC 起止 | exit |
|---|---|---:|
| red | 2026-09-10T17:44:19.770420+00:00 → 2026-09-10T17:44:19.899223+00:00 | 1 |
| green | 2026-09-10T17:44:19.900884+00:00 → 2026-09-10T17:44:19.995322+00:00 | 0 |
| mutation-1 | 2026-09-10T17:44:19.996644+00:00 → 2026-09-10T17:44:20.106907+00:00 | 1 |
| restored-green-1 | 2026-09-10T17:44:20.108413+00:00 → 2026-09-10T17:44:20.201760+00:00 | 0 |

完整命令、stdout/stderr、源文件字节哈希：`G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/OPS-06`。变异只恢复本项旧文档/指定错误片段，try/finally 恢复准确固定字节后重跑绿色。

## 限制

只验证实际文档片段与其消费者/前置契约；边界使用新建合成夹具。运行时行为、生产证据及负责人批准不由本检查替代；全量门禁由主代理统一执行。
