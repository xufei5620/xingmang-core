# XM-DBTEST-FIX0：修复验收线首次真正跑通的 DB 集成测试

## status

READY（待验收线审读合入 `release/v0.1-launch`）。纯测试/夹具修复，无生产代码改动，无契约/迁移改动。

## branch

`ai/claude/XM-DBTEST-FIX0`（base `release/v0.1-launch` @ `3e653d7`），worktree `K:/星芒统一控制平台/wt-xmDBTESTFIX0`。

## background

验收线的门禁脚本在此之前从未导出过 `XM_TEST_DATABASE_URL`，所以仓库里所有靠这个环境变量开关的 DB 集成测试，从项目历史至今全部被静默跳过（`t.Skip`），从未真正跑过一次。今天第一次带着这个变量跑，暴露出三个独立的、彼此无关的夹具缺陷（详见下）。本片逐一定位、修复，并把整条 DB 集成测试套件在共享库上跑到确定性通过。

## commits

三个独立提交，一个失败对应一个提交：

- `85bcbb9` — fix(finance): truncate platform_channel_binding in DB test fixtures
- `37cc07a` — fix(jobs): repair connector_config integration test fixture
- `1ecae47` — fix(savedviews): correct flawed cross-table Remove assertion in isolation test

## summary

### 失败 1（团队交接单已知）：`internal/platform/jobs` — `TestPgConnectorConfigSourceIntegration`

**判定：测试夹具漂移，非产品缺陷。** 两处独立问题，都只在真的导出 `XM_TEST_DATABASE_URL` 后才会暴露：

1. newapi 行的 INSERT 只给了 `platform/environment/mode`，把 `updated_at`/`updated_by` 都留空。`migrations/000020_credential_ref_and_connector_config.up.sql` 里这两列是 `NOT NULL` 且**没有默认值**（`endpoint`/`target_allowlist`/`credential_ref`/`version` 四列虽然也是 `NOT NULL` 但都带 `DEFAULT`）。测试注释写的是"故意把可空列全留 NULL"，但迁移早已把这些列收紧成 NOT NULL——测试写在迁移收紧之前，之后没人跟着改。修复：显式给 `updated_at`/`updated_by`，断言也从"应读成 NULL/空值"改成"应读成 DEFAULT 值"（`version` 应为 `1` 而非 `0`）。顺手把测试里 `CREATE TABLE IF NOT EXISTS`（给未迁移库跑用的兜底建表）的列约束也同步成和 `migrations/000020` 一致的 NOT NULL/DEFAULT 形态，不然这条兜底路径本身也是过时契约。
2. 测试硬编码 `environment = "staging"`（`environment` 列有 FK 到 `core.environment`，只能用已登记的三个环境之一，代码里的注释"用带纳秒后缀的唯一值"是过时的，实际代码从未这么做过）。`(platform, environment)` 键空间只有 2×3=6 种组合，而 `internal/platform/credentials` 包的 `TestConnectorConfigSetAndList` 恰好也会写 `('sub2api','staging')` 这一行，且只在**开始前** TRUNCATE、不在结束后清理。取决于包的跑测顺序，`jobs` 包这条测试可能撞上 `credentials` 包留下的行，报 `duplicate key value violates unique constraint "connector_config_pkey"`。修复：本测试自己的键位在跑测**前后**都主动 DELETE 一遍，不再假设表原本是空的、也不依赖任何别的包"跑完会清理干净"这件事。

### 失败 2（团队交接单已知）：`internal/platform/savedviews` — `TestStoreIsolatesOwnerEnvironmentAndTable`

**判定：测试断言写错了，不是 `savedviews.Store.Remove` 的越权漏洞。** 已核实 `Store.Remove` 及其生成 SQL（`db/queries/savedviews.sql` 的 `DeleteSavedViewOwned`）始终以 `id + owner_issuer + owner_subject + identity_zone + environment` 全量限定；`Store.List`/`Set` 同样以完整 owner 元组 + environment（+ table_key/name）限定；`ui.saved_view.remove@1` 这个 Action 的 schema（`internal/platform/savedviews/actions.go` 的 `removeDefinition`）**只接受 `saved_view_id` 一个参数**，owner 永远来自 `ownerFromContext` 解出的 principal，不可能来自请求字段——这条路径已有 `TestSetSchemaRejectsClientOwnerAndEnvironment`、`TestSetHandlerUsesPrincipalOwnerAndHashOnlyAudit` 两个既有单测覆盖。

真正的问题是测试本身：`checks` 列表里混了三种情形（不同 owner、不同 environment、**同一个 owner + 不同 table**），却对全部三种情形都调用 `Remove` 并断言应返回 `ErrNotFound`。但 `Remove` 根本不接受 `table_key` 参数——第三种情形传的 owner 正是该行的真实 owner（`alice`/`staging`），`Remove` 用 id + 正确 owner 删除自己的行，理所当然成功（`err == nil`），断言失败报的正是"foreign remove err = <nil>"。修复：把 `List` 的隔离检查（owner × environment × table 三个维度，`List` 确实接受 `table_key`）和 `Remove` 的隔离检查（只有 owner × environment 两个维度，因为 `Remove` 没有 `table_key`）拆成两组分别断言；额外补一步"真 owner 事后仍能删除该行"，证明它在前面每一次冒名尝试里都完好无损，而不是被其中某一次悄悄删掉了。

### 失败 3（本次巡查新发现，团队交接单未提及）：`internal/platform/finance` 包 48 个测试全部失败

**判定：测试夹具漂移，非产品缺陷。** 报错统一是 `cannot truncate a table referenced in a foreign key constraint`。根因：`migrations/000016_finance_platform_channel_binding.up.sql` 给 `finance.platform_channel_binding` 加了一条 `ON DELETE RESTRICT` 外键指向 `finance.upstream_account`，但 `store_integration_test.go`（`testPool`）与 `actions_integration_test.go`（`actionPool`）里那条显式列全所有引用表的 `TRUNCATE ... finance.upstream_account` 语句一直没有把这张新表加进去——按 Postgres 规则，`TRUNCATE` 一张被外键引用的表，必须在同一条语句里连带列出所有引用它的表，漏了就直接报错，且这条语句是所有该包 DB 集成测试的共享入口，一炸全炸。修复：两处 TRUNCATE 语句都补上 `finance.platform_channel_binding`。（`channel_binding_store_integration_test.go` 自己的 `bindingPool` 只 TRUNCATE 这一张表本身，没有这个问题，未改动。）

## previously-skipped DB test packages — status now

以下按 `grep -rl XM_TEST_DATABASE_URL` 找到的全部包统计；"本次改动"列只标出真正动了代码的包，其余包在导出 `XM_TEST_DATABASE_URL` 后**首次运行即全绿，未做任何改动**。

| 包 | DB 集成测试文件（举要） | 本次改动 | 现状 |
|---|---|---|---|
| `connectors/metering` | revenuedb_integration_test.go | 无 | 通过 |
| `internal/platform/action` | store_test.go | 无 | 通过 |
| `internal/platform/alerts` | audit/reconcile/store_integration_test.go | 无 | 通过 |
| `internal/platform/audit` | store_test.go | 无 | 通过 |
| `internal/platform/audit/archive` | catalog_integration_test.go | 无 | 通过 |
| `internal/platform/credentials` | store_integration_test.go | 无 | 通过 |
| `internal/platform/finance` | store/actions/channel_binding/subscription/summary/profit_store_integration_test.go | **有**（TRUNCATE 补列） | 通过（48 个此前失败的用例全绿） |
| `internal/platform/httpapi` | audit_integration_test.go | 无 | 通过 |
| `internal/platform/jobs` | connector_config/newapi_sync/query_store/retention/river/sub2api_sync_integration_test.go | **有**（连接器配置夹具修复） | 通过 |
| `internal/platform/localauth` | store_test.go | 无 | 通过 |
| `internal/platform/ops` | store_test.go | 无 | 通过 |
| `internal/platform/registry` | store_test.go | 无 | 通过 |
| `internal/platform/savedviews` | store_integration_test.go | **有**（断言拆分修正） | 通过 |
| `internal/platform/server` | store_integration_test.go | 无 | 通过 |
| `internal/platform/shadow` | soloai_integration_test.go | 无 | 通过 |

## files_changed

- `internal/platform/finance/store_integration_test.go` — TRUNCATE 列表补 `finance.platform_channel_binding`
- `internal/platform/finance/actions_integration_test.go` — 同上
- `internal/platform/jobs/connector_config_integration_test.go` — newapi 行补 `updated_at`/`updated_by`，断言改为 DEFAULT 值；兜底建表语句同步 NOT NULL/DEFAULT 契约；测试键位前后自清理
- `internal/platform/savedviews/store_integration_test.go` — 拆分 List/Remove 隔离检查，补真 owner 事后删除验证

无生产代码、无迁移、无契约、无前端改动。

## tests_run

全部在 `K:/星芒统一控制平台/wt-xmDBTESTFIX0` 下、`XM_TEST_DATABASE_URL` 指向 `postgres://postgres:test@127.0.0.1:55432/xm_test`（八个代理环境变量已确认 unset）执行，串行（`-p 1`）：

```
go build ./...
go vet ./...
gofmt -l .   # 见 risks，一处与本片无关的既有问题
bash scripts/check-governance.sh
XM_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/xm_test" go test -p 1 -count=1 ./...
```

结果：

- `go build ./...`：通过。
- `go vet ./...`：通过。
- `gofmt -l .`：本片改动的四个文件均无输出（已格式化）；`internal/platform/httpapi/finance_test.go` 有输出，但在 `git stash` 到本片改动之前（即 base `3e653d7`）复测同样有输出——确认是与本片无关的既有问题，未处理（见 risks）。
- `bash scripts/check-governance.sh`：通过（exit 0，无输出）。
- 全量 DB 集成测试 `go test -p 1 -count=1 ./...`：**修复前**在 `internal/platform/finance`（48 个测试）、`internal/platform/jobs`（`TestPgConnectorConfigSourceIntegration`）、`internal/platform/savedviews`（`TestStoreIsolatesOwnerEnvironmentAndTable`）三个包失败；**修复后**连续跑了两次（第二次单独验证确定性），49 个包全部 `ok`，exit 0，无 FAIL。
- `gitleaks detect --source . --log-opts="3e653d7..HEAD"`：`3 commits scanned`，`no leaks found`。

未额外做 loopback httptest 隔离重跑，因为两次全量串行跑测均无 flake（无需要单独重跑定位的失败）。

## not_run

- `pnpm` 相关命令（前端 typecheck/test/storybook）：本片未改动任何前端代码，且交接单声明"无 pnpm changes expected"。
- 未针对未迁移的空库验证 `connector_config_integration_test.go` 的 `CREATE TABLE IF NOT EXISTS` 兜底路径（当前共享测试库已迁移，该分支从未被真正执行到）；已同步其列约束到与 `migrations/000020` 一致的契约，但这条路径本身未被 CI 覆盖过，值得后续单独起一个未迁移库验证一次。

## risks

- `internal/platform/httpapi/finance_test.go` 有 gofmt 未格式化的问题，但**确认为本片改动之前就存在**（`git stash` 回退到 base commit `3e653d7`后复测同样报出），与本片三处修复无关，未处理，留给后续任一涉及该文件的切片顺手带上。
- `internal/platform/credentials` 包的 `TestConnectorConfigSetAndList`（`store_integration_test.go`）只在启动时 TRUNCATE，不在结束后清理它写入 `core.connector_config` 的行——这正是本次撞见 `internal/platform/jobs` 那处 duplicate-key 失败的另一半原因。本次选择在 `jobs` 侧（消费方）做防御性前置清理，而不是反过来要求 `credentials` 包改变其既有"跑前清空"惯例（该包内部自洽，改了可能影响其他依赖同一惯例的用例）。这是一个可接受但值得记录的耦合：任何新增的、复用 `('sub2api'|'newapi', development|staging|production)` 这 6 个键位之一的 DB 测试，都应该照本片 `jobs` 侧的模式自带前后清理，不能假设表是空的。
- 三处修复均只改测试文件，未改任何生产代码/SQL/迁移；`savedviews.Store.Remove` 及其调用链在核实过程中确认符合"写操作按 owner+environment(+table_key/id) 全量限定、owner 只能来自 principal"的红线，未发现需要修的产品缺陷。

## follow_ups

- 建议给 `internal/platform/credentials` 的 DB 集成测试补一条"结束后清理"的惯例（比如统一用 `t.Cleanup` 注册一次收尾 TRUNCATE），从根上消除"跑测顺序敏感"这类耦合，而不是只在下游消费者（`jobs` 包）侧防御。
- `internal/platform/httpapi/finance_test.go` 的 gofmt 问题建议在下一个改到该文件的切片里顺手跑一次 `gofmt -w` 清掉。
- 未迁移库上的 `connector_config_integration_test.go` 兜底建表路径值得单独跑一次验证（见 not_run）。

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01XtTQUB1k16Vw2K49WQJhpi
