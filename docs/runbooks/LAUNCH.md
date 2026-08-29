# Runbook：初步上线（staging 一键启动栈）

任务 XM-0023。本文覆盖 `deploy/compose/launch.yaml` 这一档的部署、验证、
排障与回滚。

> 服务器共享验收与 production 的发布闸门由 XM-C-DEPLOY0-b 的
> `deploy/scripts/deploy.sh` / `promote.sh` 负责；本文件中的手工 Compose 命令
> 仍适用于本地一次性启动栈，不应替代服务器脚本。完整流程见
> `docs/runbooks/DEPLOY.md`。

> **这一档跑的是什么数据**
> 当前栈的 Sub2API 连接器是 **Fake 模式**（`XM_SUB2API_MODE=fake`）。
> 它产生的一切数字都是**演示与联调**用的构造数据，**不是业务真相**，
> 不得用于对账、开票、结算或任何对外承诺。
> 切真实数据的前置条件是 **XM-0017**（Sub2API 只读账号还没拿到）。

---

## 0. 这一档的边界

| 能做 | 不能做 |
|---|---|
| staging 验收、内部演示、前后端联调 | 承载生产流量 |
| 跑迁移、跑 Bootstrap、跑 Action | 处理真实业务凭据 |
| 用 `X-Dev-*` 头模拟身份 | 当作生产鉴权 |

`ENVIRONMENT=production` 会让 `platform-api` **拒绝启动**，见第 6 节。

---

## 1. 前置条件

- Docker Engine ≥ 29 + Compose v5（本机验证版本：29.7.2 / v5.3.1）
- 能拉到 `docker.io/library/postgres:18`、`golang:1.27-alpine`、
  `node:24-alpine`、`nginx:1.28-alpine`、`alpine:3.22`
- **不需要**宿主装 Go / Node / pnpm：三个 Go 二进制和 admin-web 都在容器里构建

镜像版本以 `VERSIONS.lock` 为准，全部钉死，禁止 `latest`（宪法 §4）。

---

## 2. 配置

```bash
cp deploy/compose/.env.example deploy/compose/.env
```

编辑 `deploy/compose/.env`，**必须**填 `DATABASE_PASSWORD`（没有默认值，
空着 compose 会直接报错退出）：

```bash
openssl rand -base64 32
```

`.env` **不入库**——根 `.gitignore` 的 `.env` / `.env.*` 已经挡住，
只有 `*.example` 例外。提交前用 `git status` 确认它没被列出来。

各变量的含义与取舍写在 `.env.example` 的注释里，不在这里重复。

---

## 3. 启动

```bash
docker compose -p xingmang-launch -f deploy/compose/launch.yaml up -d --wait
```

`-p xingmang-launch` 不是可选的：本机可能已经跑着 `deploy/compose/dev.yaml`
起的开发库（`compose-postgres-1`）。独立项目名 = 独立网络 + 独立卷 + 独立
容器名，两个栈互不干扰。本栈也刻意不发布 5432/5433。

启动顺序由 compose 的依赖条件保证，不需要人工掐时间：

```
postgres (healthy)
   ├─→ migrate   (一次性，退出码必须 0)
   │       ├─→ bootstrap      (一次性，幂等)
   │       ├─→ platform-api   (healthy 后)
   │       │        └─→ web
   │       └─→ platform-worker
```

预期终态：

```
SERVICE           STATUS                     EXIT
bootstrap         Exited (0)                 0
migrate           Exited (0)                 0
platform-api      Up (healthy)               0
platform-worker   Up                         0
postgres          Up (healthy)               0
web               Up (healthy)               0
```

`migrate` 与 `bootstrap` 显示 `Exited (0)` 是**正常终态**，不是故障——
它们是一次性作业，跑完就该退出。

---

## 4. 迁移与 Bootstrap 是怎么回事

两者都是 **Platform Lifecycle Operation**（宪法 3 条 / ADR-003）：
不走 Action API，但受版本化脚本 + 变更单 + 人工批准 + 独立审计约束。
**任何模块都不得把这条通道当成绕过 Action 的写入口。**

### 4.1 migrate 服务（两步）

见 `deploy/docker/migrate-entrypoint.sh`：

1. `db/migrations/*.sql` —— 平台自己的 forward-only 迁移，`cmd/migrate`
   （golang-migrate）执行。
2. River 表结构 —— `platform-worker -migrate`，用 River 内嵌的 pinned bundle
   （`internal/platform/jobs.Migrate`）。
   `db/migrations/river/` 下那份 SQL 是**可审阅镜像**，不是第二条独立迁移线，
   不能拿 golang-migrate 去跑。

合在一个容器里跑完，是为了不出现「业务迁移过了、River 没过」的半截状态。

### 4.2 bootstrap 服务

执行 `deploy/bootstrap/001_staging_seed.sql`：登记 staging 环境行 +
一条 `sub2api-staging` 服务实例。全部 `ON CONFLICT DO NOTHING`，
可重复执行。

**刻意不建 `core.connection`**：真实凭据还没到（XM-0017），Fake 模式也不需要；
为了「先建起来」去编一个假的 `secret://` ref，等于在库里种一条将来会被当成
真的假事实。

为什么它不走 Action，脚本头部有完整说明（先有蛋问题 + 它写的是环境自身的
事实而非业务决定）。

---

## 5. 验证

### 5.1 迁移退出码

```bash
docker inspect xingmang-launch-migrate-1 --format '{{.State.ExitCode}}'   # 0
docker logs xingmang-launch-migrate-1
```

日志里应看到 `step 1/2` 的「迁移完成」和 `step 2/2` 的 7 条
`Applied migration`（首次）或 `No migrations to apply`（重跑）。

### 5.2 网络内探针（**这是权威验证方式**）

```bash
NET=xingmang-launch_default
docker run --rm --network $NET alpine:3.22 wget -q -O- http://platform-api:8080/healthz
docker run --rm --network $NET alpine:3.22 wget -q -O- http://platform-api:8080/readyz
```

预期：

```json
{"build":"xingmang-platform staging (unknown)","status":"ok"}
{"status":"ready"}
```

### 5.3 Bootstrap 的数据能被 API 读到

```bash
docker run --rm --network $NET alpine:3.22 wget -q -O- \
  --header 'X-Dev-Principal-ID: staff_ops' \
  --header 'X-Dev-Principal-Type: HUMAN' \
  --header 'X-Dev-Scopes: registry.read' \
  http://platform-api:8080/api/v1/services
```

预期看到 `instance_id: sub2api-staging`，且
`"observed_at": null, "stale_seconds": null`。

**`null` 是对的**：从来没采集过就不该有时间戳。前端必须据此显示
「未初始化」，而不是显示一个裸数字冒充实时数据（宪法 12 条）。

### 5.4 web 静态托管与 /api/ 同源反代

```bash
docker run --rm --network $NET alpine:3.22 wget -q -O- http://web/index.html
docker run --rm --network $NET alpine:3.22 wget -q -O- \
  --header 'X-Dev-Principal-ID: staff_ops' \
  --header 'X-Dev-Principal-Type: HUMAN' \
  --header 'X-Dev-Scopes: registry.read' \
  http://web/api/v1/services
```

同源意味着前端不配 API base URL，也就没有 CORS、没有预检、没有
「本地能跑上了 staging 就 403」那类环境差异。
`X-Dev-*` 头由 Nginx 原样透传（`deploy/nginx/launch.conf`）。

### 5.5 幂等性（值得单独验一次）

```bash
docker compose -p xingmang-launch -f deploy/compose/launch.yaml run --rm bootstrap
docker compose -p xingmang-launch -f deploy/compose/launch.yaml run --rm migrate
```

两条都应退出 0，bootstrap 第二次是 `INSERT 0 0`，migrate 第二次是
「无待应用迁移」+「No migrations to apply」。
**「重跑一次」必须永远是安全操作**，否则事故里没人敢重跑。

### 5.6 前端页面目前长什么样

admin-web 现在是 **UI 骨架**：一个 dev 登录壳 + 一个运营总览页，
页面上是一块占位空态「Sub2API 数据未接入」。它**还没有调用 `/api/v1/*`**。

所以 5.3 / 5.4 的 curl 是当前唯一能证明「数据通了」的手段，
别指望打开页面就能看到那条服务记录。前端接数据是另一条任务线的事，
本任务只消费它的构建产物，不改 `web/` 源码。

---

## 6. 上生产前必须先做什么

**`ENVIRONMENT=production` 会让 `platform-api` 拒绝启动。这是有意设的闸，
不是 bug，也没有开关。**

实测（把上面的栈换成 production 跑）：

```
level=ERROR msg=api_start_failed module=platform.api
  error_code=no_principal_resolver
  err="开发期 Principal 注入器不允许在生产环境使用；请接入 OIDC（XM-0008）"
```

原因：Foundation-A 的身份来自 `X-Dev-Principal-ID` 等请求头。
**允许调用方自称身份 = 没有鉴权**。所以拒绝是硬编码在
`internal/platform/httpapi.NewDevHeaderResolver` 里的。

解闸的前置条件：

1. **CR-0001（Keycloak）** —— 部署 Keycloak，把 dev-header 换成 OIDC Bearer。
   代码侧只需新增一个 `PrincipalResolver` 实现，接口与所有 handler 不动。
2. `VERSIONS.lock` 里 `keycloak.digest` 还是「待XM-0007部署时锁定」，
   部署时必须锁死。
3. 生产档还需要：TLS 入口、真实备份与恢复演练（宪法 22 条）、
   外部看门狗（宪法 23 条）。这些不在本任务范围。

**在 CR-0001 完成前，不要试图把这个栈改成 production 档。**
能改的只有 `.env` 里一个字符串，改完的后果是一个没有鉴权的管理后台
——那个拒绝启动的行为正是在防这件事。

---

## 7. 已知问题

### 7.1 宿主 → 容器端口转发可能不通（本开发机）

**现象**：从宿主访问 `http://127.0.0.1:8088` SYN 停滞、浏览器转圈超时，
而容器网络内一切正常。

**原因**：本开发机的代理 TUN 会劫持宿主到 Docker 端口映射的连接。
这是**宿主环境问题，不是栈的问题**。服务器部署不受影响。

**本次 XM-0023 验证时宿主访问是通的**（`curl 127.0.0.1:8088` 返回 200），
但这个现象不稳定、会随代理开关变化，**不要把它当作栈是否健康的判据**。

**权威验证方式**：始终用第 5.2 / 5.3 / 5.4 节的
`docker run --rm --network xingmang-launch_default ...` 从 compose 网络内验证。
容器内通 = 栈是好的；宿主不通只说明宿主到 Docker 的这一跳被劫持。

如果确实需要从宿主浏览器看页面，可以试：关掉代理 TUN / 全局模式，
或把 `WEB_BIND` 改成 `0.0.0.0` 再用宿主的局域网 IP 访问。

### 7.2 首次构建慢

admin-web 在容器里构建（见 `deploy/docker/web.Dockerfile` 顶部的取舍说明），
首次要装整个 pnpm workspace 的依赖。BuildKit 会缓存依赖层，
只要 `pnpm-lock.yaml` 不变就不重装。

### 7.3 PG18 的卷路径

`postgres18-data` 必须挂到 `/var/lib/postgresql`，**不是** `.../data`。
PG18 把集群放在按大版本分的子目录下，挂到 `data` 上容器起不来。
compose 里有注释标了，**不要"顺手改回去"**。

---

## 8. 停止与清理

```bash
# 停止，保留数据卷
docker compose -p xingmang-launch -f deploy/compose/launch.yaml down

# 连数据一起删（不可逆）
docker compose -p xingmang-launch -f deploy/compose/launch.yaml down -v
```

`-p xingmang-launch` 同样不能省——省了会去操作**默认项目名**的栈，
可能碰到别人的容器。开发库 `compose-postgres-1` 属于另一个项目，
本栈的 `down` 不会动它。

---

## 9. 排障速查

| 现象 | 多半是 |
|---|---|
| `DATABASE_PASSWORD is required` | `.env` 没建或没填密码（第 2 节） |
| `error_code=database_url_invalid`，提示「密码必须经 CredentialRef」 | DSN 里内联了密码。非开发环境只能用 `DATABASE_PASSWORD_REF`（见下） |
| `error_code=no_principal_resolver` | `ENVIRONMENT=production`（第 6 节，是闸不是 bug） |
| `error_code=config_invalid` | `ENVIRONMENT` 缺失或不是三值之一 |
| postgres 起不来，日志说目录非空 | 卷挂到了 `/var/lib/postgresql/data`（7.3） |
| migrate 退出码非 0 | 看 `docker logs xingmang-launch-migrate-1` 定位是第 1 步还是第 2 步 |
| web 返回 502 | platform-api 没 healthy；先跑 5.2 |
| 宿主访问超时但容器内正常 | 7.1，不是栈的问题 |

### 关于 DSN 纪律

`platform-api` 与 `platform-worker` 用同一套规则（`cmd/*/database.go` +
`internal/platform/pgdsn`）：非开发环境，密码只能来自 CredentialRef。
DSN 内联密码、`?password=`、`PGPASSWORD`、`~/.pgpass` 一律拒绝。

判定不是「用 net/url 解析一下 URL」——pgx 会把 query 参数当连接设置读，
而且是在填完 host/user **之后**覆盖它们，所以自己解析一定漏。
`pgdsn` 的做法是问 pgx 实际会用什么配置。查询参数走白名单，
不认识的一律拒绝。详见 `internal/platform/pgdsn` 的包注释。

唯一的例外在迁移容器第 1 步：`cmd/migrate` 走 golang-migrate，
入口脚本用 `PGPASSWORD=... migrate ...` 的**单条命令前缀**传密码，
不 export 到整个脚本——否则第 2 步的 pgdsn 校验会（正确地）拒绝启动。

---

## 10. 相关文档

- `deploy/compose/.env.example` —— 每个变量的含义
- `deploy/bootstrap/001_staging_seed.sql` —— Bootstrap 为什么不走 Action
- `deploy/docker/web.Dockerfile` —— 为什么在容器里构建前端
- `cmd/platform-api/README.md` / `cmd/platform-worker/README.md` —— 环境变量
- `docs/runbooks/secrets.md` —— 凭据管理（SOPS / CredentialRef）
- `docs/modules/registry/RUNBOOK.md` —— Registry 本地开发流程
