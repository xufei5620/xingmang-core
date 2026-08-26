# Runbook：Registry

## 本地起库并初始化

```bash
docker compose -f deploy/compose/dev.yaml up -d
go run ./cmd/migrate -database "postgres://xingmang:xingmang-dev@127.0.0.1:5433/xingmang?sslmode=disable" up
```

查看当前迁移版本：把末尾 `up` 换成 `version`。

## 跑集成测试

```bash
XM_TEST_DATABASE_URL="postgres://xingmang:xingmang-dev@127.0.0.1:5433/xingmang?sslmode=disable" \
  go test ./internal/platform/registry/ -v
```

未设置该变量时集成测试自动跳过，领域测试照常运行。

## 改了 SQL 之后

```bash
go tool sqlc generate   # 重新生成 internal/platform/registry/gen
go build ./...
```

CI 会用 `git diff --exit-code` 校验生成物与 SQL 一致；不一致直接失败。

## 新增迁移

1. 新建 `db/migrations/00000N_<描述>.up.sql` 与 `.down.sql`，**不得修改已发布的迁移文件**；
2. 新增列 → 回填 → 加约束，分三次迁移（规格 §5.7）；
3. 生产执行前必须有备份与恢复验证（规格 §20.7）；
4. 执行是 Platform Lifecycle Operation：变更单 + 人工批准 + 独立审计。

## 常见故障

| 现象 | 处置 |
|---|---|
| `migrate` 报 `unknown driver postgres` | 连接串 scheme 由 cmd/migrate 内部改写为 pgx5://；若绕过该命令直连需自行改写 |
| `migrate` 报 dirty | `go run ./cmd/migrate ... version` 查看版本，人工修复数据后 force 版本号（需变更单） |
| 集成测试报 FK 违反 | 测试未按 connector → service → connection 顺序建数据 |
| `credential_ref` 插入被拒 | 该值不是 `secret://<scope>/<name>` 形态——这是设计如此，不要放宽 CHECK |
| 容器启动即退出并提示 pg_upgrade | PostgreSQL 18 要求挂载 `/var/lib/postgresql`（不是 `/var/lib/postgresql/data`） |
| 宿主连不上已发布端口（SYN_RECEIVED） | 本机 Docker 端口转发被代理 TUN 劫持，与本项目无关；改用容器内执行或依赖 CI |
