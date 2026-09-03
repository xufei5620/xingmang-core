# Console Assertion Keyring 契约 v1

状态：冻结（CR-0006 起生效）。清单文件：`console-assertion-keyring.v1.json`
（本仓库与 invoice-system 仓库各存一份只读副本，逐字段比对一致，见
`docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md` 步骤 1）。
实现：`internal/platform/consoleassertion`（本仓库，签发方）；
`backend/internal/auth/console_assertion.go`（invoice-system 仓库，验证方）。

## `algorithm` 字段 vs. JWS header 的 `alg`：两个不同的字符串，不可混用

清单里每条记录的 `algorithm` 字段与断言 JWS 的 header `alg` 声明的是两件
不同的事，字面值也不同：

| 位置 | 字段/声明 | 唯一合法值 | 命名的是什么 | 本仓库常量 |
|---|---|---|---|---|
| `console-assertion-keyring.v1.json` 每条记录 | `algorithm` | `Ed25519` | 密钥算法本身 | `consoleassertion.KeyringAlgorithm` |
| 断言 JWS 的 protected header | `alg` | `EdDSA` | 签名方案名（JOSE 登记名） | `consoleassertion.Algorithm` |

两侧都按字面值精确校验，互不兼容、不做归一化：清单记录的 `algorithm` 绝不能
写成 `"EdDSA"`，JWS header 的 `alg` 也绝不能写成 `"Ed25519"`。

**事故记录：** XM-INVCON-KEYRING-ALG（2026-09-03）—— 本仓库
`internal/platform/consoleassertion` 曾经只有一个 `Algorithm = "EdDSA"`
常量，同时被 JWS header 序列化和清单记录校验两处复用，导致
`cmd/console-assertion-keygen` 生成、提交进本文件的公钥记录带着
`"algorithm":"EdDSA"`；该记录原样复制进 invoice-system 仓库后，其验证方
按字面值要求 `algorithm=Ed25519`，于是一启用断言兑换就在启动阶段拒绝服务。
修复：拆分出独立的 `KeyringAlgorithm = "Ed25519"` 常量专供清单一侧使用，
`Algorithm` 只保留给 JWS header；并新增回归测试直接加载本仓库提交的
`console-assertion-keyring.v1.json`，断言每条记录的 `algorithm` 字段等于
`Ed25519`，防止再次跑偏。详见
`docs/handoffs/slices/XM-INVCON-KEYRING-ALG.md`。

## 字段与校验规则

字段形状、`key_id`/`purpose`/`protocol`/有效期/撤销规则直接复用
`internal/platform/jobs/fleet_keyring.go` 的 `JobFleetTrustedKey` 校验集
（域分离：`purpose=console_admin_assertion_signing`、
`protocol=xm-console-assertion-v1`，与 job fleet 清单互不可信）。完整规则见
`docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md` §3.3
与 `internal/platform/consoleassertion/keyring.go` 的 `PublicKeyRecord.
Validate`/`ValidateRecords`。

## 破坏性变更

本契约的字段形状或校验规则变化必须发布 v2 并保留 v1 解析兼容期；双仓库须
同步评审、同步合入。
