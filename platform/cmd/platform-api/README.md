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

### 请求详情 / reqlog 只读网关（XM-0039）

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `XM_REQLOG_MODE` | 否 | `off` | `off` 两个端点不挂载；`fake` 演示样本；`real` 走真实客户端骨架 |
| `XM_REQLOG_ENDPOINT` | real 必填 | — | 必须 https（见下方未决冲突） |
| `XM_REQLOG_TARGET_ALLOWLIST` | real 必填 | — | 逗号分隔的**精确**主机清单；留空 = 一个请求都发不出去 |
| `XM_REQLOG_CREDENTIAL_REF` | real 必填 | — | `secret://<scope>/<name>`。**本层只校验形状，不解析明文** |
| `XM_REQLOG_TOKEN` | — | — | 上面那个引用在 env Provider 下的落点，形态是 `用户名:口令` **整串** |
| `XM_REQLOG_TIMEOUT` | 否 | `30s` | 单次控制台读取超时 |

三条与 worker 侧连接器不同的纪律，理由见
`contracts/connectors/reqlog.read.v1.md` §7：

- **默认 `off` 而不是 `fake`**——这条通道读的是用户与模型的完整对话；
- **生产禁 `fake`**，启动即拒；
- **`real` 配置不全启动即拒**（worker 那边是写失败观测继续跑）。

⚠️ 两处现状：`real` 模式**读不出数据**（控制台 API 形状未核实，端点返回 501）；
且控制台是 `http://127.0.0.1:9300`（回环明文）而只读连接配置要求 https，
这条冲突尚未拍板（同文档 §5）。

需要的权限：列表 `request.read`，正文 `request.content.read`。
两者都不在 `DefaultRoleScopeMap` 的 `staff` 里；`admin` 只有前者——
**看正文要显式授予一个专门的角色**（XM-0039 验收裁定，理由见
`docs/modules/httpapi/AUTH-SWITCH.md`）：

```bash
XM_OIDC_ROLE_SCOPES='{"request-auditor":["request.read","request.content.read"]}'
```

**每次读取正文都会写一条 `request.content.viewed` 审计事件；写不进去就不返回内容。**

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
