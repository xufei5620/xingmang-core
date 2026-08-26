# platform-api

管理后台与内部集成 API（规格 §5.5）。**不参与用户实时请求路径**（ADR-011）。

## 环境变量

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `ENVIRONMENT` | 是 | — | `development` / `staging` / `production` |
| `DATABASE_URL` | 是 | — | PostgreSQL 连接串。非开发环境**不得内联密码** |
| `XM_DATABASE_URL` | 否 | — | `DATABASE_URL` 的旧名字；两者同规则，同时配以前者为准 |
| `DATABASE_PASSWORD_REF` | 非开发环境必填 | — | `secret://<scope>/<name>`，解析到 `DATABASE_PASSWORD` |
| `DATABASE_PASSWORD` | 配了 ref 时必填 | — | 明文密码，只由 Provider 读取，不进日志 |
| `LISTEN_ADDR` | 否 | `127.0.0.1:8080` | 绑回环，由宿主 Nginx 反代（规格 §21.2）；容器内设 `0.0.0.0:8080` |
| `REQUEST_TIMEOUT` | 否 | `30s` | 单请求期限 |

### 连接串纪律（宪法 7 条 / XM-R008）

`cmd/platform-api/database.go` 与 `cmd/platform-worker/database.go` 是同一套
规则的两份实现——两个进程连同一个库，只挡一边等于没挡。判定不自己解析 URL，
而是让 `internal/platform/pgdsn` 问 pgx **实际**会用什么配置：`?password=`、
`?host=`、`PGPASSWORD`、`~/.pgpass` 都能覆盖 DSN 的表面声明，任何「先 net/url
解析再交给 pgx」的检查都是空的。查询参数走白名单，不认识的一律拒绝。

## 本地运行

```bash
docker compose -f deploy/compose/dev.yaml up -d
go run ./cmd/migrate -database "postgres://xingmang:xingmang-dev@127.0.0.1:5433/xingmang?sslmode=disable" up

ENVIRONMENT=development \
DATABASE_URL="postgres://xingmang:xingmang-dev@127.0.0.1:5433/xingmang?sslmode=disable" \
  go run ./cmd/platform-api
```

开发环境（且未配 `DATABASE_PASSWORD_REF`）才允许上面这种内联密码。
一键起 staging 全栈见 `docs/runbooks/LAUNCH.md`。

## 开发期身份

Foundation-A 阶段用请求头注入身份（**生产环境启动即拒绝**）：

```bash
curl -s localhost:8080/api/v1/services \
  -H 'X-Dev-Principal-ID: staff_alice' \
  -H 'X-Dev-Principal-Type: HUMAN' \
  -H 'X-Dev-Scopes: registry.read,registry.service.manage'
```

执行一个 Action：

```bash
curl -s -X POST 'localhost:8080/api/v1/actions/registry.service.create/versions/1/execute' \
  -H 'Content-Type: application/json' \
  -H 'X-Dev-Principal-ID: staff_alice' \
  -H 'X-Dev-Principal-Type: HUMAN' \
  -H 'X-Dev-Scopes: registry.service.manage' \
  -d '{"params":{"service_type":"sub2api","instance_id":"sub2api-dev","environment":"development","endpoint":"https://api.solov.cc","owner":"platform"}}'
```

XM-0008 接入 Keycloak 后，这些 `X-Dev-*` 头会失效，改用 Bearer Token。

## 本机 Docker 端口不通时的冒烟方式

本机 Docker 端口发布被代理 TUN 劫持（见 `docs/modules/registry/RUNBOOK.md`）。
可以交叉编译后在 compose 网络内跑：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ./.smoke-platform-api ./cmd/platform-api
docker create --name xm-api-smoke --network compose_default \
  -e ENVIRONMENT=development -e LISTEN_ADDR=0.0.0.0:8080 \
  -e DATABASE_URL='postgres://xingmang:xingmang-dev@postgres:5432/xingmang?sslmode=disable' \
  alpine:3 /platform-api
docker cp ./.smoke-platform-api xm-api-smoke:/platform-api && docker start xm-api-smoke
docker run --rm --network compose_default alpine:3 sh -c \
  'apk add -q --no-cache curl; curl -s http://xm-api-smoke:8080/healthz'
```

Git Bash 下需加 `MSYS_NO_PATHCONV=1`，否则容器内路径 `/platform-api` 会被改写。
