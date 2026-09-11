# OPS-08 — separate tokenmap shape checks from real reader acceptance

status: FIXED_TARGETED_VERIFIED
branch: ai/codex/XM-FULL-AUDIT-platform-docs-20260911
base: 09c557fa8428d0ed15272ed43dac4d1d311c8d1b

只修改本项文档及回归；未执行生产、真实配置、数据库、凭据、SSH或CPA操作。

## 改动

- `platform/docs/runbooks/REQLOG-RECORDER.md`
- `platform/tests/runbooks/reqlog_shape_claim_test.py`

## 验证

| 阶段 | UTC 起止 | exit |
|---|---|---:|
| red | 2026-09-10T17:50:21.234231+00:00 → 2026-09-10T17:50:21.450147+00:00 | 1 |
| green | 2026-09-10T17:50:21.452055+00:00 → 2026-09-10T17:50:21.662339+00:00 | 0 |
| mutation-1 | 2026-09-10T17:50:21.663940+00:00 → 2026-09-10T17:50:21.897870+00:00 | 1 |
| restored-green-1 | 2026-09-10T17:50:21.899576+00:00 → 2026-09-10T17:50:22.129006+00:00 | 0 |

完整命令、stdout/stderr、源文件字节哈希：`G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/OPS-08`。变异只恢复本项旧文档/指定错误片段，try/finally 恢复准确固定字节后重跑绿色。

## 限制

只验证实际文档片段与其消费者/前置契约；边界使用新建合成夹具。运行时行为、生产证据及负责人批准不由本检查替代；全量门禁由主代理统一执行。
