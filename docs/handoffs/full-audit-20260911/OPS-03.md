# OPS-03 — align real switching with database configuration precedence

status: FIXED_TARGETED_VERIFIED
branch: ai/codex/XM-FULL-AUDIT-platform-docs-20260911
base: 5ebf6132862637c6a1008055b9b63c1197a4e219

只修改本项文档及回归；未执行生产、真实配置、数据库、凭据、SSH或CPA操作。

## 改动

- `platform/docs/runbooks/SWITCH-NEWAPI-REAL.md`
- `platform/docs/runbooks/SWITCH-SUB2API-REAL.md`
- `platform/tests/runbooks/connector_mode_doc_test.py`

## 验证

| 阶段 | UTC 起止 | exit |
|---|---|---:|
| red | 2026-09-10T17:52:40.516710+00:00 → 2026-09-10T17:52:40.609326+00:00 | 1 |
| green | 2026-09-10T17:52:40.611737+00:00 → 2026-09-10T17:52:41.786345+00:00 | 0 |
| mutation-1 | 2026-09-10T17:52:41.788056+00:00 → 2026-09-10T17:52:42.514081+00:00 | 1 |
| restored-green-1 | 2026-09-10T17:52:42.516168+00:00 → 2026-09-10T17:52:43.225558+00:00 | 0 |
| mutation-2 | 2026-09-10T17:52:43.227103+00:00 → 2026-09-10T17:52:43.310463+00:00 | 1 |
| restored-green-2 | 2026-09-10T17:52:43.312612+00:00 → 2026-09-10T17:52:44.011525+00:00 | 0 |
| mutation-3 | 2026-09-10T17:52:44.013086+00:00 → 2026-09-10T17:52:44.757684+00:00 | 1 |
| restored-green-3 | 2026-09-10T17:52:44.759980+00:00 → 2026-09-10T17:52:45.497998+00:00 | 0 |
| mutation-4 | 2026-09-10T17:52:45.499653+00:00 → 2026-09-10T17:52:45.585849+00:00 | 1 |
| restored-green-4 | 2026-09-10T17:52:45.587999+00:00 → 2026-09-10T17:52:46.309814+00:00 | 0 |

完整命令、stdout/stderr、源文件字节哈希：`G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/OPS-03`。变异只恢复本项旧文档/指定错误片段，try/finally 恢复准确固定字节后重跑绿色。

## 限制

只验证实际文档片段与其消费者/前置契约；边界使用新建合成夹具。运行时行为、生产证据及负责人批准不由本检查替代；全量门禁由主代理统一执行。
