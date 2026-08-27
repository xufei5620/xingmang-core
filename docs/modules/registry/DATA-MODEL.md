# Registry 数据模型

Schema：`core`。迁移：`db/migrations/000001_init_core_registry.up.sql`（forward-only）。

## core.environment

| 列 | 类型 | 说明 |
|---|---|---|
| id | text PK | 仅 development / staging / production（CHECK 约束） |
| description | text | 说明 |
| created_at | timestamptz | UTC |

种子数据随迁移写入，不可删除（被其他表 FK 引用，ON DELETE RESTRICT）。

## core.service

业务唯一键 `(service_type, instance_id)`；主键为 uuid（规格 §18.7 主键与业务键分开）。

| 列 | 约束 |
|---|---|
| service_type / instance_id | `^[a-z0-9][a-z0-9-]{0,63}$` |
| environment | FK → core.environment |
| endpoint | 必须 `https://` 开头；**不得带凭据**——userinfo（`user:pass@`）或 token/key/secret/password 类查询参数一律拒绝（XM-0031） |
| status | active / degraded / retired |
| source_watermark / observed_at | 数据新鲜度回写（规格 §9.1）；observed_at 可空表示未采集 |

## core.connector

业务唯一键 `(key, version)`。

| 列 | 约束 |
|---|---|
| key | `^[a-z0-9][a-z0-9-]{0,63}$` |
| version | 精确三段语义化版本 |
| target_allowlist | text[]，**cardinality ≥ 1**（ADR-004 铁律） |
| read_capabilities / write_capabilities | text[]，形如 `<system>.<resource>.<action>` |

## core.connection

业务唯一键 `(connector_id, service_id, environment)`。

| 列 | 约束 |
|---|---|
| credential_ref | CHECK 必须匹配 `^secret://<scope>/<name>$`（宪法 7 条纵深防御） |
| target_allowlist | cardinality ≥ 1 |
| kill_switch | 具写能力时由领域层强制必填 |
| status | enabled / disabled / killed |

## 不变量分层

| 不变量 | 领域层 | 数据库层 |
|---|---|---|
| 标识符字符集 | ✓ | ✓ CHECK |
| endpoint 必须 https | ✓ | ✓ CHECK |
| endpoint 不含凭据 | ✓ | ✗（需解析 URL，不适合 CHECK） |
| allowlist 非空 | ✓ | ✓ CHECK |
| credential_ref 形态 | ✓（并复用 secrets.ParseCredentialRef） | ✓ CHECK |
| 写能力必须有 Kill Switch | ✓ | ✗（跨列语义，由领域层保证） |
| 读/写能力集合不交叉 | ✓ | ✗ |

## 已知陷阱

数组非空约束必须用 `cardinality(arr) >= 1`，**不能**用 `array_length(arr, 1) >= 1`：
空数组的 `array_length` 返回 NULL，`NULL >= 1` 为 NULL，而 CHECK 只在结果为
FALSE 时拒绝——写成 array_length 会让空数组通过。本项在开发期实测发现并修正。
