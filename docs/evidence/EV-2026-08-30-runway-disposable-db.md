# EV-2026-08-30 · RUNWAY0 disposable PostgreSQL verification

## scope

一次性本地 PostgreSQL 容器，仅验证 RUNWAY0 migration 000017、lifecycle bootstrap、
verified view 和 history append-only trigger；容器在验证结束后已停止并自动移除。
未使用共享 `xingmang-launch` 数据卷、生产凭据或真实上游。

## commands / checks

- `go run ./cmd/migrate -database <ephemeral-loopback-dsn> up` — PASS
- `go run ./cmd/runway-threshold-bootstrap up`（`DATABASE_PASSWORD_REF` 通过 env provider
  注入）— PASS，输出仅含 environment/revision/三档。
- `SELECT count(*)`：`config=1 | history=1 | current_verified=1` — PASS
- 在事务中把 current revision 临时改为无 history 的 `2`，查询 verified view 返回 `0` 行；
  随后恢复 revision — PASS（orphan fail-closed）。
- 对 history 执行 UPDATE，触发固定 append-only SQLSTATE `55000` — PASS。

## sanitized output

```text
RUNWAY-DISPOSABLE-DB-PASS rows=1|1|1 view_orphan=0 trigger=55000
```

No DSN, password, token, host credentials or raw environment values were retained.
