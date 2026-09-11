# OPS-02 — verify reqlog in the selected deployment container

status: FIXED_TARGETED_VERIFIED
branch: ai/codex/XM-FULL-AUDIT-platform-docs-20260911
base: 6a3b6bc0f102e2f53f7bef976e002bb8193e1ed7

只修改本项文档及回归；未执行生产、真实配置、数据库、凭据、SSH或CPA操作。

## 改动

- `platform/docs/runbooks/REQLOG-RECORDER.md`
- `platform/tests/runbooks/reqlog_container_doc_test.py`

## 验证

| 阶段 | UTC 起止 | exit |
|---|---|---:|
| red | 2026-09-10T17:47:40.327600+00:00 → 2026-09-10T17:47:40.439664+00:00 | 1 |
| green | 2026-09-10T17:47:40.441472+00:00 → 2026-09-10T17:47:40.592111+00:00 | 0 |
| mutation-1 | 2026-09-10T17:47:40.593816+00:00 → 2026-09-10T17:47:40.718487+00:00 | 1 |
| restored-green-1 | 2026-09-10T17:47:40.720377+00:00 → 2026-09-10T17:47:40.907411+00:00 | 0 |
| mutation-2 | 2026-09-10T17:47:40.909027+00:00 → 2026-09-10T17:47:41.095643+00:00 | 1 |
| restored-green-2 | 2026-09-10T17:47:41.097480+00:00 → 2026-09-10T17:47:41.236960+00:00 | 0 |

完整命令、stdout/stderr、源文件字节哈希：`G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/OPS-02`。变异只恢复本项旧文档/指定错误片段，try/finally 恢复准确固定字节后重跑绿色。

## 限制

只验证实际文档片段与其消费者/前置契约；边界使用新建合成夹具。运行时行为、生产证据及负责人批准不由本检查替代；全量门禁由主代理统一执行。
