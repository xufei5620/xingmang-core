# XM-CRED0-backend · 凭据登记后端（文件 SecretProvider 存储 + Action + 只读 Query）

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-CRED0-BACKEND`
- implementation commit: 本分支 HEAD（单提交，`git log -1`）
- worktree: `K:/星芒统一控制平台/acceptance/wt-cred0-backend`

## summary

运营在管理后台粘贴上游凭据；**明文只以文件形式落在 `XM_SECRET_ROOT`**
（`<root>/<scope>/<name>`，与 `secrets.NewFileProvider` 同布局），数据库只存引用、
`sha256` 指纹前 16 位、版本与吊销时间。另有一张按平台的连接器运行配置表
（fake/real、端点、主机白名单、凭据引用），供 worker 每轮读取（worker 侧由另一
切片实现，本片只提供表、Action 与只读端点）。

### 迁移 `000020_credential_ref_and_connector_config`

- `core.credential_ref(ref PK, scope, name, environment FK, fingerprint, version, revoked_at, updated_at, updated_by)`
- `core.connector_config(platform, environment FK, mode, endpoint, target_allowlist text[], credential_ref, version, updated_at, updated_by; PK (platform, environment))`
- 手写 pgx 查询，不经 sqlc。

### Action（全部 L1、HUMAN-only、三环境；Environment 与操作者只来自 Principal）

| Action | 权限 | 参数 |
|---|---|---|
| `credential.secret.upsert@1` | `credential.manage` | `credential_ref`（必填）、`secret_value`（必填） |
| `credential.secret.rotate@1` | `credential.manage` | 同上；**要求引用已登记**，否则 `PRECONDITION_FAILED` |
| `credential.secret.revoke@1` | `credential.manage` | `credential_ref`、`reason`（必填非空白） |
| `connector.config.set@1` | `connector.manage` | `platform`（sub2api\|newapi）、`mode`（fake\|real）、`endpoint`、`target_allowlist`（逗号分隔主机）、`credential_ref` |

- 结果值：`{"credential_ref","fingerprint","version"}`（revoke 另带 `"revoked":true`）；
  `connector.config.set` 返回整份配置摘要。**任何结果、审计摘要、错误文本都不含值。**
- 值校验：去首尾空白；空、>8192 字节、含换行一律 `INVALID_PARAMS`。
- `mode=real` 要求 https 端点、非空白名单、可解析引用，且端点主机必须在白名单里。
- 跨环境改写（引用已在另一环境登记）→ `ENVIRONMENT_MISMATCH`。

### 只读 Query（`Deps.Credentials` 为 nil 时整组不挂载）

- `GET /api/v1/credentials?environment=` → `{"items":[{credential_ref, scope, updated_at, fingerprint, version, available, revoked}]}`，scope `credential.manage`
- `GET /api/v1/credentials/expected` → `{"items":[{credential_ref, platform, purpose, configured}]}`（configured = 已登记 ∧ 未吊销 ∧ 文件可读）
- `GET /api/v1/connectors/config` → `{"items":[{platform, mode, endpoint, target_allowlist, credential_ref, version, updated_at, updated_by}]}`，scope `connector.manage`
- 环境解析沿用 `resolveEnvironment`：不传用 Principal 的；传了必须一致。

### 装配与部署

- `cmd/platform-api`：`XM_SECRET_ROOT`（默认 `/run/xm/secrets`）；注册 Action；`Deps.Credentials`。
- `oidcauth`：新增角色 `credential-admin → {credential.manage, connector.manage}`（**不进 staff/admin**）；
  `credential.` 加入平台 scope 前缀（令牌里出现即漂移）。
- `deploy/compose/launch.yaml`：命名卷 `xm-secrets`，platform-api 读写挂 `/run/xm/secrets`，
  platform-worker 只读挂同一路径；两者都注入 `XM_SECRET_ROOT`。
- `deploy/docker/go.Dockerfile`：运行底座预建 `/run/xm/secrets` 并 chown 给 10001——
  命名卷首次创建时继承该属主，否则非 root 进程写不进去。

## files_changed

- `db/migrations/000020_credential_ref_and_connector_config.up.sql` / `.down.sql`
- `internal/platform/credentials/{permissions,store,connector_config,actions,expected}.go`
- `internal/platform/credentials/{store_test,store_integration_test,actions_test}.go`
- `internal/platform/httpapi/credentials.go`、`credentials_test.go`、`router.go`
- `cmd/platform-api/main.go`、`config.go`、`config_test.go`
- `internal/platform/oidcauth/rolemap.go`、`resolver_test.go`
- `deploy/compose/launch.yaml`、`deploy/compose/.env.example`、`deploy/docker/go.Dockerfile`
- `docs/modules/httpapi/PERMISSIONS.md`

## tests_run

- `gofmt -l`（改动文件）clean；`go build ./...`；`go vet`（credentials / httpapi / oidcauth / platform-api）PASS。
- `go test -count=1 ./internal/platform/credentials/... ./internal/platform/httpapi/... ./internal/platform/oidcauth/... ./cmd/platform-api/...` PASS（本机 Windows，proxy 变量已 unset）。
- DB 集成测试：一次性 `postgres:18`（launch.yaml 同 digest）容器 + `go run ./cmd/migrate up`，
  `XM_TEST_DATABASE_URL` 下 `TestStoreUpsertRotateRevokeRoundTrip` / `TestStoreNeverStoresTheValue` /
  `TestConnectorConfigSetAndList` PASS；`migrate down` 后两表消失、再 `up` 成功。
- `bash scripts/check-governance.sh` PASS。
- `docker compose -f deploy/compose/launch.yaml --env-file <临时 .env> config --quiet` PASS（临时 .env 在仓库外，未提交）。

## not_run / risks

- 未做真实镜像构建与栈启动验证（Dockerfile 的 `/run/xm/secrets` 预建目录只经 compose config 校验）。
- `internal/platform/httpapi/finance_test.go` 在基线上就不满足 `gofmt -l .`（本片未改它）；CI 的 gofmt 步骤会因此失败，需另行处理。
- 文件与 DB 行不在同一事务里：只有「文件已替换、COMMIT 失败」这个窗口会让指纹暂时落后于文件，重做一次 upsert 即可修复。
- `Store.Upsert/Rotate/Revoke` 返回 `WriteResult{Before *Metadata, After Metadata}`（任务书写的是 `(Metadata, error)`）——为了把轮换前后指纹都写进审计链；`List` / `ConnectorConfig` 形状按任务书。

## follow_ups

- worker 切片：按 `XM_SECRET_ROOT` 构造 `secrets.NewFileProvider`，每轮读 `credentials.Store.ListConnectorConfigs`。
- 前端：凭据页对接三条 Query 与四个 Action；`credential-admin` 角色需在 Realm/`XM_OIDC_ROLE_SCOPES` 人工审定。
