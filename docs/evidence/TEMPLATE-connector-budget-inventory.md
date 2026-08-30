# Connector 预算与路由盘点证据（R215-1）

> 这是脱敏模板，不得填入 endpoint host、query、请求正文、响应正文、cursor、
> CredentialRef 值或任何 token。R215-1 只冻结静态策略；源站限流与持久化预算
> 属于 R215-2/R215-3，不能在此模板中宣称已启用。

## 基本信息

| 字段 | 值 |
|---|---|
| policy version | `1` |
| policy artifact | `contracts/connectors/budgets.v1.json` |
| release / branch | `<sha>` |
| inventory generated at (UTC) | `<timestamp>` |
| real source access | `not_run` |

## 路由清单

从 `connector.RegisteredRouteSpecs()` 与各 Connector 的
`RegisteredRouteSpecs()` 生成；只记录稳定的 route id、capability、HTTP 方法和
路径模板。路径模板不得包含主机、查询参数或秘密。

| connector | capability | route id | method | path template | policy owner |
|---|---|---|---|---|---|
| `<type>` | `<capability>` | `<route-id>` | `GET`/`HEAD` | `/<template>` | `<capability>` |

## 每项能力预算

| capability | cost class | requests | pages | rows | bytes | cost units | run ms | source concurrency | coverage |
|---|---|---:|---:|---:|---:|---:|---:|---:|---|
| `<capability>` | `<probe|light|page|count-heavy>` | `<n>` | `<n>` | `<n>` | `<n>` | `<n>` | `<n>` | `<n>` | `<complete|partial>` |

## 门禁记录

- [ ] `go test ./internal/platform/connector ./connectors/...`
- [ ] `go vet ./...`
- [ ] `gofmt -l` 无输出
- [ ] `gitleaks protect --staged` 通过；增量日志扫描无秘密命中
- [ ] 未新增 migration、网络请求、凭据解析、compose 或运行时配置
- [ ] 未启用 destructive-consume / write capability

## 未运行与后续

R215-1 不连接真实源站，不改变现有轮询间隔，也不执行 source-budget reservation。
R215-2 需另行批准 migration/DBR；R215-3 需逐 Connector 的 fake/shadow 证据；
R215-4 才能在负责人批准的 staging 窗口启用 enforcement。
