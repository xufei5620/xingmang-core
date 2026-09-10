# OPS-04 — retain production override in real connector restart cards

status: FIXED_TARGETED_VERIFIED
branch: ai/codex/XM-FULL-AUDIT-platform-docs-20260911
base: 91800ca2f6fced3b16d358bc00a72882bebeea93

只修改本项文档及回归；未执行生产、真实配置、数据库、凭据、SSH或CPA操作。

## 改动

- `platform/docs/runbooks/SWITCH-NEWAPI-REAL.md`
- `platform/docs/runbooks/SWITCH-SUB2API-REAL.md`
- `platform/tests/runbooks/real_restart_overlay_test.py`

## 验证

| 阶段 | UTC 起止 | exit |
|---|---|---:|
| red | 2026-09-10T17:47:38.935215+00:00 → 2026-09-10T17:47:39.022441+00:00 | 1 |
| green | 2026-09-10T17:47:39.025307+00:00 → 2026-09-10T17:47:39.094340+00:00 | 0 |
| mutation-1 | 2026-09-10T17:47:39.096262+00:00 → 2026-09-10T17:47:39.201104+00:00 | 1 |
| restored-green-1 | 2026-09-10T17:47:39.203271+00:00 → 2026-09-10T17:47:39.267203+00:00 | 0 |
| mutation-2 | 2026-09-10T17:47:39.268986+00:00 → 2026-09-10T17:47:39.348106+00:00 | 1 |
| restored-green-2 | 2026-09-10T17:47:39.350489+00:00 → 2026-09-10T17:47:39.413680+00:00 | 0 |

完整命令、stdout/stderr、源文件字节哈希：`G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/OPS-04`。变异只恢复本项旧文档/指定错误片段，try/finally 恢复准确固定字节后重跑绿色。

## 限制

只验证实际文档片段与其消费者/前置契约；边界使用新建合成夹具。运行时行为、生产证据及负责人批准不由本检查替代；全量门禁由主代理统一执行。
