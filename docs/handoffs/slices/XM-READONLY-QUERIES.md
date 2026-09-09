# XM-READONLY-QUERIES：三条只读 Query（连接器 / 连接 / 迁移记录）+ 两条错误映射

- **status:** implemented，未提交（按指示改完即停，不 commit / 不 push）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `8c446e5`。
- **来源：** 三个缺口的共同点是「后端有东西、前端看不到」，且都要动
  `internal/platform/httpapi/router.go`，所以合成一片做。
- **dbroles 已按验收线批准的方案 B 落地**（新建 core 视图 + 更新摘要链），
  见「dbroles：按方案 B 落地」。
- 另外修了普查发现的一个**真 bug**：`/registry` 声明了六个子页签而页面根本
  不渲染 Tabs，`?sub=connectors` 静默显示服务表。见「顺带修的一个真 bug」。
- **⚠️ 迁移号占用**：本片占了 **000050**（`core_schema_migration_state`）。
  它在本工作树里还是未跟踪文件，**别的工作树看不见它**——若有并行的线也在
  开 000050，合入时会撞号。合入顺序若把本片排在后面，把本片这两个文件重命名
  成当时的下一个可用号即可（视图名与策略对象名不含版本号，改文件名就够）。
- **⚠️ 本工作树有并发改动**：另一条线（Sub2API 利润 / 支付分桶 / 运营工作台）
  正在同一个 worktree 里改十几个 `components/` `lib/` `pages/` 下的文件。
  除 `router.test.tsx` 里的一个用例外（本片造成的连坐，见「门禁」），
  我一个都没碰。**提交时注意别把他们的改动一起带上。**

## 三个缺口的复核结果（先做的一步）

三条今天**都还在**，逐条对过代码而不是照抄任务书：

| 缺口 | 复核 |
|---|---|
| 资源目录两张表 | `RegistryPage.tsx:79-82` 仍写着「连接器与连接两张表尚未建」；`router.go` 全文无 `/connectors`、`/connections` 列表路由。574 行那条 `/connectors/config` 确认是**凭据模块**的（`credentials.ScopeConnectorManage`），不是这个。 |
| 迁移记录 | `ChangesPage.tsx:189` 那段缺口描述仍在；`router.go` 无任何 migrations 路由。 |
| 两条错误映射 | `finance/actions.go:630` 的 `domainError` case 表里确实没有这两个哨兵，二者落 default 分支返回**裸 error**，被内核归一成 `EXECUTION_FAILED`（502）。 |

任务书没说、但实测发现的一条：`registry.Store` 也**没有**这两个列表方法。
它只有 `GetConnector`（按 key+version）与 `ListConnectionsByService`（按服务），
没有「列全部连接器」「按环境列连接」。所以本片不止加路由，还要加 SQL + Store。

## 缺口一：资源目录的连接器与连接

### 端点契约（逐字）

**`GET /api/v1/connectors`** —— 权限 `registry.read`。**不接受也不使用
`environment` 参数**：`core.connector` 没有 environment 列（迁移 000001），
它是「平台有哪几种连接实现」的类型目录，全平台一份。排序 `(key, version)`。

```json
{"items":[{
  "id":"11111111-1111-4111-8111-111111111111",
  "key":"sub2api", "version":"0.1.0", "contract_version":"v1",
  "connection_schema_path":"contracts/connectors/sub2api.v1.json",
  "target_allowlist":["api.example.test"],
  "read_capabilities":["sub2api.user.list"],
  "write_capabilities":["sub2api.user.update"],
  "supported_upstream_versions":["0.1.179"],
  "compatibility_test_path":"tests/connectors/sub2api",
  "created_at":"2026-09-01T03:04:05Z","updated_at":"2026-09-02T03:04:05Z"
}]}
```

**`GET /api/v1/connections?environment=<可选>`** —— 权限 `registry.read`。
环境由 `resolveEnvironment` 决定（不传用调用者自己的，传了必须一致，否则 403
「不允许跨环境读取：调用者身份属于 X」）。排序 `created_at`。

```json
{"items":[{
  "id":"22222222-2222-4222-8222-222222222222",
  "connector_id":"…","service_id":"…","environment":"development",
  "credential_ref":"secret://sub2api/dev-token",
  "target_allowlist":["api.example.test"],
  "granted_capabilities":["sub2api.user.list"],
  "kill_switch":"", "status":"enabled",
  "detected_upstream_version":"0.1.179","version_fingerprint":"sha256:abc",
  "last_verified_at":null,
  "created_at":"2026-09-01T03:04:05Z","updated_at":"2026-09-02T03:04:05Z"
}]}
```

三处空值语义写进了契约与界面，不是留白：`last_verified_at: null` = **从未
核验**（不是「很久以前核验过」）；`kill_switch: ""` = 未配置（纯读连接）；
`supported_upstream_versions: []` = **未声明**（不是「兼容所有版本」）。
四个数组列一律出 `[]` 不出 `null`——前端直接 `.length`，多一个 null 分支
只会多一处漏判。

### scope 归属与依据

**复用 `registry.ScopeRead`（`"registry.read"`）**，不新增 scope。依据：
服务 / 连接器 / 连接是 ADMIN-IA 给注册表的同一份职责（三张表），泄漏面相当
——与 `/servers/*` 四条复用它是同一条理由（`router.go:586-590` 那段注释）。
因此 `STAFF_ROLE_CATALOG` **不需要动**。

`credential_ref` 出这一列是刻意的，依据是仓库自己已经写下的判断：
`registry/actions.go:100-104` 说明它进审计链是安全且必要的——「它是引用不是
凭据（ADR-014），而『这条连接绑了哪个凭据』正是事故复盘要问的第一个问题」。
值一步都不进端点，只有 SecretProvider 碰得到（宪法 7 条）。
**验收线已裁定保留**，并补了一条比我更强的依据：`internal/platform/secrets/ref.go:16`
写着「**Ref 本身不是秘密，可安全出现在日志、配置与审计中**」——既然它能进
审计和日志，给持 `registry.read` 的人看就不构成新的泄漏面。由此暴露出的
「同一类数据在 registry 与 finance 两处档位不同」已登记进 follow_ups 第 2 条，
留给产品负责人裁定。

### 前端

`RegistryPage.tsx` 现在按信息架构渲染六个子页签，「连接器」「连接」两格填
本片新接的这两条 Query（页面结构与那个子页签 bug 一起改的，见下面专门一节）。

连接表把 `connector_id` / `service_id` **就地换成人认得出的名字**（用同页
已有的两份数据做映射），认不出时原样显示 UUID 并标「不在本页清单里」——
留空会让「指向一个已经不在清单里的对象」看起来像「这一列没数据」。

三张表分成三个 `useQuery`：一格 403 不该把另外两格拖成错误态（有用例钉着）。
三个都挂在页面级而不是各自的格里——连接那一格要靠另外两份数据做名字映射，
拆到格里之后切一次页签就得重取。

**写入 UI 不在本片**：`connector.create` 是 L2、`connection.create` 是 L3，
要走审批中心。空状态里写清了这一点，页面上没有对应按钮。

## 缺口二：数据库变更（迁移记录）

### 端点契约（逐字）

**`GET /api/v1/ops/migrations`** —— 权限 `ops.read`。不按环境筛（一个进程
只连一个库）。

```json
{"applied_version":50,"dirty":false,"ahead":0,
 "items":[{"version":1,"name":"init_core_registry","applied":true,"has_down":true}]}
```

`has_down` **不叫「可回滚」**：迁移是 forward-only（规格 §5.7），down 脚本
存在只是让退路有据可查，不是一个按钮。

### scope 归属与依据

**`ops.read`**，与你定的一致。仓库里的准确写法是常量
`ops.ScopeRead`（`internal/platform/ops/freshness.go:397`，
`const ScopeRead = "ops.read"`）——已核对，`alerts.ScopeRead` 复用的也是它。
依据：迁移版本回答「这套部署自己处在什么状态」，与心跳、队列积压、控制平面
健康同一类运行保障面；`registry.read` 那一族说的是「平台管着哪些**被管**
系统」，不是同一件事。

`lifecycle.ScopeRead` 与 `ops.ScopeRead` 由 `TestScopeReadIsOpsRead` 钉住逐字
相等（同 `alerts/consistency_test.go` 的做法）——两处各写一个字符串常量迟早
漂开，而漂开的症状是「界面 403、权限表里查得到那个 scope」，最难查的一类。

### 数据从哪来：库 + 二进制，两边都要

`public.schema_migrations` **只有两列**（`version`、`dirty`），而且只有一行
——golang-migrate 不留逐条台账。所以「列出已应用的迁移版本」只靠库答不出来，
必须把脚本清单也带上：

- 版本号与 dirty ← 库；
- 脚本编号 / 名字 / 有没有 down ← **二进制里嵌着的 `db/migrations/*.sql`**。

嵌是必须的，不是偷懒：`deploy/docker/go.Dockerfile` 只往 **migrate** 镜像
`COPY db/migrations`，**platform-api 镜像里没有这个目录**，读文件系统读不到。

嵌进来还多一个真实收益：`ahead > 0` 直接说出「镜像更新了而迁移容器没跑」
——那是一个发生过的部署形态，读运行时目录反而看不出来。

嵌入点放在 `db/embed.go`（package `db`）而**不是** `db/migrations/` 里面：
那个目录被三样东西按目录读（`cmd/migrate` 的 golang-migrate、`sqlc.yaml` 的
schema、`check-governance.sh` 的不可变性检查），往里塞 `.go` 文件等于给三条
链路各加一次风险。对照 `db/migrations/river/mirror.go`——那份镜像自成一个
目录，所以可以就地嵌。

### 读不到时**报错**，不回「版本 0」

`lifecycle.Store.state` 只把 `42P01`（表不存在，全新库的正常形态）当成
「一条都没跑过」，**其余错误一律往上抛，尤其是 `42501`
（insufficient_privilege）**。把权限错误吞成「版本 0」会在界面上显示「一条
迁移都没应用」——一句笃定的假话比一个 500 难查得多（宪法 12 条）。
前后端各有一条用例钉这个。

### 前端

`ChangesPage` 的「数据库变更」格从纯蓝图态变成「真实读数 + 保留蓝图预览」。
蓝图那张表还要「迁移前后行数 / 执行人 / 验证 / 回滚路径」四列——平台没有
逐条迁移台账，那四列取不到，**所以没有编，页面上直接写明为什么取不到**。
`dirty` 与 `ahead > 0` 用 `role="alert"` 单独说，不混进正文。

三处落款文案（`TAB_SOURCE` / `TAB_HEADLINE` / `TABLE_SOURCE` 的 `database`
条目）一并订正——它们原本写的是「缺的是一条新 Query」。

## dbroles：按方案 B 落地

验收线在 A / B 之间选了 **B**，并要求包括改那条摘要链。理由（原话）：
**B 不是绕过那条隔离，B 是那条隔离预先规定好的出口**——设计稿
`docs/superpowers/specs/2026-08-28-database-role-separation-design.md` §7.6
自己写着「ops 若后续需要任务诊断，**另批只读视图**」。A 则是明着推翻 §7.6
并翻掉 `NoRuntimeAccess`，那要产品负责人级别的裁定。

**为什么现在就要做**（这句请原样留着，它是这一步的全部理由）：

> 今天不改也不影响功能，风险是角色拆分落地那天 fail closed。

生产 compose 用的是 `POSTGRES_USER=xingmang`（`deploy/compose/launch.yaml:27`，
既是超级用户又是表 owner，`deploy/bootstrap/002_grants_evidence.sql` 自己写着
「闸门今天不存在」），角色拆分还没落到生产。所以缺授权的代码今天照样跑得通
——而这正是本仓库那条教训（**迁移新建表要重放 permissions**）的形状：
不报错、不影响当下、在拆分落地那天静静地 403。

### 改了什么（五处，缺一处链就断）

1. **`db/migrations/000050_core_schema_migration_state.{up,down}.sql`（新增）**
   —— 建 `core.schema_migration_state`，投影 `public.schema_migrations` 的
   `version` 与 `dirty` 两列。视图 owner 是 `xm_migrator`，PostgreSQL 的视图
   默认 `security_invoker = false`，所以对基表的权限检查走 **owner**，
   而 xm_migrator 本来就是那张表的 owner。读者只需要 `core` 的 USAGE
   （各 runtime 角色本来就有）+ 本视图的 SELECT，**`public` 的权限一格都不用动**。
   迁移里**不含 GRANT**：库权限全部由角色策略施加（同 000017 的做法）。
2. **`internal/platform/dbroles/policy.go`** —— `defaultObjects()` 里加
   `view core.schema_migration_state`，授 `xm_api_runtime` / `xm_ops_read` /
   `xm_backup_read` 的 SELECT；`tableColumns()` 里加 `{"version","dirty"}`。
   **`xm_worker_runtime` 刻意不给**：§7.6 写明 worker 对 schema_migrations
   无权限，从视图绕过去等于把那条裁定架空。`xm_lifecycle_runtime` 也不给
   ——它没有这个需要。
3. **`contracts/database/role-policy.v1.json`** —— 重新生成。生成配方是
   `json.MarshalIndent(DefaultPolicyV1(), "", "  ")` **加一个换行**，
   与旧文件逐字节相等（先验证过再改的）。
4. **`contracts/database/role-policy-state-events.v1.jsonl`** —— 追加第三条
   `policy-update` 事件：

   | 字段 | 值 |
   |---|---|
   | `event_id` | `evt-core-schema-migration-state-view` |
   | `sequence` | 3 |
   | `previous_event_sha256` | `173ed29d…428a50f4`（第二条事件的规范摘要） |
   | `previous_policy_sha256` | `c9c79c95…21895504`（旧策略） |
   | `current_policy_sha256` | `108e9be2…878afd42`（新策略） |
   | 本事件自身摘要 | `4ce037e0…f896a0fca2` |

   追加后立刻回读校验：`LoadStateEvents` 通过，`ValidateEventChain` 零违规。
5. **`internal/platform/lifecycle/migrations.go`** —— 查询从
   `public.schema_migrations` 改成 `core.schema_migration_state`。
   `42P01` 的兜底语义跟着改：视图不存在＝库还没迁到 000050，仍按「不知道跑到
   哪一版」处理；**42501 照旧一律往上抛**。

### 一处必须说明的取舍：那条 DDL 包在 EXECUTE 里

直接写 `CREATE VIEW … AS SELECT … FROM public.schema_migrations` 会让
`go tool sqlc generate` **退出 1**：

```
db/migrations/000050_...up.sql:1:1: relation "schema_migrations" does not exist
```

原因是 `public.schema_migrations` **不是任何迁移脚本建的**——golang-migrate
自己在应用第一条迁移之前创建它。sqlc 把 `db/migrations` 当作全部 schema，
所以它不知道有这张表，而生成一致性是一条门禁。

三条路，选了第三条（理由逐条写在迁移文件顶部）：

1. 往 `db/migrations` 加一条 `CREATE TABLE IF NOT EXISTS
   public.schema_migrations …` 让 sqlc 看见——**否决**：在 forward-only 的
   不可变迁移流里写一条永远不生效的 DDL，还等于宣称我们拥有另一个工具的台账表；
2. 给 `sqlc.yaml` 的八份配置各加一个「外部对象声明」schema 目录——**否决**：
   八处配置改动，外加八个 `models.go` 各多出一个没人用的结构体；
3. **用 `DO $$ EXECUTE … $$` 把这条 DDL 挡在 sqlc 的解析之外**——选它。
   代价是读者看到的是字符串而不是裸 DDL，所以理由写在那个文件里。
   不损失任何东西：`core.schema_migration_state` 不进 `db/queries`
   （sqlc 不需要认识它），由 `internal/platform/lifecycle` 手写查询读取。

实测：改用 EXECUTE 之后 `go tool sqlc generate` 退出 0。

### `policy_test.go` 那条断言改成了「不随条数变化」的形式

验收线要求「改完把那条断言从『等于 3』的角度再想一遍」。**改成了钉两端，
不钉条数**：

```go
// 头：genesis 携带一个已知常量，链的起点不可被重新奠基
if events[0].Kind != EventKindGenesis || events[0].CurrentPolicySHA256 != genesisPolicyDigest {…}
// 尾：最后一条事件必须为**当前**这份策略作证
if last := events[len(events)-1]; last.CurrentPolicySHA256 != RawDigest(data) {…}
// 外加：整条链零违规
if violations := ValidateEventChain(events); len(violations) != 0 {…}
```

理由：`len(events) != 2` 那个写法**每追加一条合法事件就要改一次，
却挡不住任何摘要链已经挡住的东西**——`LoadStateEvents` 本身就跑完整的
哈希 / 序号链校验，事件无法被重排、重放或悄悄改写。真正需要钉的是两端：
起点不能被换成另一份策略，终点必须为磁盘上这份策略作证。后者正是「改了策略
忘了追加事件」会破坏的那条不变量，而它**不随条数变化**。

### 与 dbroles 相关的新测试

`TestSchemaMigrationStateViewIsTheOnlyWayRuntimeReadsMigrationVersion`
**两半都断言**——只断言「视图授了权」是不够的，同时把 `public` 开了口子会让
这个视图变得毫无意义：

- 视图存在、kind 是 `view`、owner 是 `xm_migrator`（owner 决定权限走谁，
  不是整洁问题）；
- api / ops_read / backup_read 能 SELECT，**worker 不能**；
- `public_schema_contract` 仍是 `river-only`；
- `public.schema_migrations` 仍标着 `no_runtime_access`；
- 四个 runtime 角色**都没有** schema `public` 的 USAGE，也都不能直接
  SELECT 那张基表。

## 缺口三：两条错误映射

`internal/platform/finance/actions.go` 的 `domainError` 补两条：

| 哨兵 | 新映射 | HTTP | 对外文案（逐字） |
|---|---|---|---|
| `ErrRefundNotDecreasing` | `INVALID_PARAMS` | 400 | 累计退款额只增不减：这一格填的是累计总额，不是本次新增，必须不低于已登记的累计退款额。 |
| `ErrAlreadyTerminated` | `CONFLICT` | 409 | 该批次或代理已经终止过：终止日决定结转的损失金额，登记之后不可再改，平台也没有撤销终止的 Action。 |

**两处与任务书不同，理由如下（在实现当下就记下来的；两处均已被验收线确认保持）：**

1. **`ErrAlreadyTerminated` 用 `CONFLICT` 而不是 `INVALID_PARAMS`。**
   换任何一个终止日都还是这个答案，它不是参数问题——与审批中心的
   「审批单已经执行过」（`approval/service.go:137`）是同一个形状：一次性操作
   被做了第二次。仓库里 `CodeConflict` 的既有用法全是这一类
   （`alerts/actions.go:157`、`localauth/actions.go:366`、`sms/actions_request.go:84`）。
   顺带修掉一个更要紧的毛病：落兜底时结果码是 `EXECUTION_FAILED`（**502**），
   而前端 `ApiError.retryable` 对 `>= 500` 一律判可重试（`api/client.ts`）
   ——一个永远不会成功的写操作会被当成瞬时故障反复重发。409 不可重试。
   如果你坚持要 400，改一个常量即可，但那条重试路径也会跟着回来。

2. **没有直接复用现成的 `err.Error()`。** 上面那一支 INVALID_PARAMS 是
   `action.NewError(..., err.Error(), err)`，照抄的话文案会变成
   `"5000000 < 已登记的 8000000: finance: 累计退款额只增不减"`——那串
   scale-6 微单位对填表的人没有意义，只会让人以为自己少填了三个零。
   所以给了一句写全「该怎么办」的话，原始错误仍作为 cause 进服务端日志
   （有用例断言那两个数字**不**出现在对外文案里）。

**前端那两处客户端预拦截按指示保留**（`lib/subscriptionForms.ts` 的
`validateLifecycleForm` 与 `SubscriptionLifecycleDialog` 的 `alreadyTerminated`）。

**一处我做了、并已被验收线确认保留的偏离**：`subscriptionForms.ts` 里那段 doc 注释原本
写着「`finance.domainError` 没有把它列进 INVALID_PARAMS 那一支……**服务端的
原话到不了界面**，所以必须在这里拦」——补完映射之后这句话就是假的了。我**只
改了注释、没改任何行为**（校验逻辑与文案一字未动），把它改成「这一层现在是
优化而不是必需」并指向新的集成测试。验收线的裁定：**保留**——「一条过期的注释比没有注释更坏：
它会让下一个人基于一个不成立的前提做判断。行为一字未改、只是让注释与事实
一致，这不算越界，这是收尾。」

**还有一条同类的没补**（不在你给的范围内，只报告）：`ErrBatchImmutableField`
同样没有映射，也会落兜底。它今天是否可达我没有验证——`SetBatchRefund` /
`TerminateBatch` 两条路径上没看到它被返回，可能是死代码。要不要一并处理请你定。

## 顺带修的一个真 bug：/registry 的子页签根本没渲染

普查发现的，验收线要求一并修。**这不是缺功能，是错显示**：

`ui-admin/navigation.ts:154-167` 给 `/registry` 声明了**六个**子页签
（services / connectors / connections / capabilities / apps / environments），
而 `RegistryPage` 从来不渲染 `<Tabs>`——于是 `?sub=connectors`
**静默显示服务表**。地址栏说你在看连接器，屏幕上给的是服务；把这个地址贴给
别人，对方看到的也不是你以为的那一屏。

改法照本仓库既有的做法（`ChangesPage` / `FinancePage`）：

- 渲染真实页签条，六格都在，默认落 `services`；
- `?sub=` 决定看哪一格，换页签用 `replace`（连点六格不该堆六条历史）；
- **认不出来的 `?sub=` 不静默回落**：留在本页显示「「x」子页尚未接入」
  加一个回默认格的链接；
- 三个 query 都挂在页面级（连接那一格要靠服务与连接器两份数据把外键 UUID
  换成名字，拆到格里之后切一次页签就得重取），但带 `enabled: known`
  ——一个拼错的地址不白打四次请求，而 hooks 仍然无条件调用、顺序不变
  （条件式 hook 是另一类 bug）。

**connectors / connections 两格填真数据**（就是本片新接的两条 Query），
**capabilities / apps / environments 三格按诚实占位处理**，逐格写清在等什么
——三格等的是三类不同的东西，一句「尚未接入」把它们抹平只会让人以为都在等排期：

| 格 | 在等什么 |
|---|---|
| 支持能力 | 能力**不是一张独立的表**：它登记在 `core.connector` 的 `read_capabilities` / `write_capabilities` 两列上，已经逐行显示在「连接器」格里。这一格要的是**按能力反查**的投影（哪些连接器支持它、哪条连接被授了它），那需要一条新的聚合 Query |
| 应用与模块 | 平台里**没有这个对象**：`core` schema 只有 environment / service / connector / connection 四张表。等的不是接线，是先定义这个对象要记什么 |
| 环境 | `core.environment` 在库里（迁移 000001），但只有三行且被 CHECK 约束钉死成 development / staging / production——它是**常量枚举**，不是运营可增删的登记簿。等的是「要不要为一份常量单开一条 Query」这个判断 |

### 顺带订正的措辞

原来页面上写着「连接器与连接两张表**尚未建**」——**那句是错的**：两张表在
`000001_init_core_registry.up.sql` 就建好了，三个写 Action 也早就注册着，
缺的只有只读端点。照这句话读，人会以为要从建表开始。现在写的是：

> 服务、连接器、连接三张表**都已建好**（迁移 000001），三个写入动作也早已注册；
> 此前这一页只列得出服务，缺的是连接器与连接的**只读端点**，现已补上。

## 关于「测试污染」那条旁证：查了，不是污染

收到的观察是：`pages/RegistryPage.test.tsx` 与 `pages/SupplyDetailPages.test.tsx`
全量跑 3 条红、单独跑 5 条红，失败断言涉及「Relay 甲」「订阅批次：付款、摊销与
有效期」「这是计量型账号」「上游登记簿里没有这一条」。

按「不要当噪声」的要求查清了，结论是**不是顺序依赖，也不是我的文件**：

1. **复现不出来。** 单独跑 `RegistryPage.test.tsx` 11/11 绿；单独跑
   `SupplyDetailPages.test.tsx` 23/23 绿；两个一起跑 34/34 绿；
   admin-web 全量 136 文件 / 1983 条全绿。
2. **专门找顺序依赖也找不到。** `--sequence.shuffle` 换三个种子跑三遍全量，
   三次都是 1983/1983。
3. **机制上也不可能跨文件污染。** `vitest.config.ts` 没有覆盖 `isolate`，
   默认是 `pool: forks` + `isolate: true`——每个测试文件拿到独立的模块注册表
   与环境，模块级状态传不过去。我的文件本身也是干净的：`vi.stubGlobal("fetch")`
   配 `afterEach(() => vi.unstubAllGlobals())`，每次 `render` 新建
   `QueryClient`，没有模块级可变状态，没有 `vi.mock`。
4. **那四条失败断言一条都不在我的文件里。** 逐字 grep：

   | 断言片段 | RegistryPage.test.tsx | SupplyDetailPages.test.tsx |
   |---|---|---|
   | `Relay 甲` | 0 | 2 |
   | `订阅批次` | 0 | 4 |
   | `计量型账号` | 0 | 2 |
   | `上游登记簿里没有这一条` | 0 | 1 |

5. **`SupplyDetailPages.test.tsx` 与它测的 `UpstreamDetailPage.tsx` 当时都
   处在另一条线的未提交改动里**（`git status` 里是 ` M`，不在我的改动清单）。

所以那次观察记录的是**共享工作树在那一刻的中间态**，不是用例之间的污染。
我的文件被一起点名，多半是因为它是同一次运行里唯一的新文件。

**这条旁证仍然有价值**：它促使我把 shuffle 跑了三遍，那是这一片此前没做过的
检查。「单独跑更多红」那个方向的判断是对的——只是这次的因不在测试隔离上。

## 测试

**Go（新增 5 个文件）**

- `internal/platform/lifecycle/migrations_test.go`（3 条）——嵌入清单与**磁盘**
  逐版本对账（判据取自磁盘，不取自嵌进来的那份自己）；第一版名字；
  scope 与 `ops.ScopeRead` 逐字相等。
- `internal/platform/httpapi/registry_catalog_test.go`（8 条）——两个端点的
  逐字段契约、空数组不为 null、`registry.read` 两个方向、跨环境 403 且**不
  到达仓储**、不传参数用调用者环境、三处空值语义、依赖为 nil 时 404、
  仓储报错不泄漏底层细节。
- `internal/platform/httpapi/registry_catalog_integration_test.go`（3 条）——
  **真库 + 真仓储 + 真路由**：环境过滤真的落在 SQL 上（生产那条的
  `credential_ref` 一个字都不出现）、连接器不按环境筛（互为对照）、
  `ORDER BY` 真的在（逆序登记构造判据）。
- `internal/platform/httpapi/migrations_test.go`（5 条）+
  `migrations_integration_test.go`（1 条）——形状、dirty 两个方向、
  `ops.read` 两个方向、**读不到时报错而不是回版本 0 且不泄漏**、nil 时 404；
  集成那条判据取自 `lifecycle.Inventory()` 而不是写死一个数字。
- `internal/platform/httpapi/finance_domain_error_integration_test.go`（3 条）
  ——**真库 + 真内核 + 真 Action + HTTP 端点**，穿过
  「仓储 → domainError → 内核 → StatusForCode → safeMessage」五段。
  代理那条单独测：两个 handler 各自调 `domainError`，漏掉一个不会有任何东西报错。

- `internal/platform/dbroles/policy_test.go`（+1 条，改 1 条）——新增
  `TestSchemaMigrationStateViewIsTheOnlyWayRuntimeReadsMigrationVersion`
  （视图侧与 public 侧**两半都断言**）；`TestCheckedInPolicyContractLoads`
  的条数断言改成头 / 尾 / 整链三条（理由见 dbroles 那一节）。

**前端**：`pages/RegistryPage.test.tsx`（新增，**17 条**——含子页签路由那组）、
`pages/ChangesPage.test.tsx`（16 → 21 条）。

### 变异验证（11 项，全部「改条件」不删代码，且都带对照组）

| # | 变异 | 期望红在哪 | 结果 |
|---|---|---|---|
| 1 | `db/embed.go` 的 `//go:embed migrations/*.sql` → `*.up.sql` | 清单与磁盘对账的 `has_down` 那一行 | **红在 `migrations_test.go:81` 与 `:114`**，正是 has_down 两条 |
| 2 | `ListConnectionsByEnvironment` 的 `WHERE environment = $1` → `… OR true` | 「只该看见本环境的 1 条」 | **红在 `registry_catalog_integration_test.go:141`**，报「实际 2 条」；同次运行里假仓储那 8 条与连接器那 2 条**全绿** |
| 3 | `domainError` 两条 case 换成别的哨兵（`ErrBatchImmutableField` / `ErrNoAmortizableBatch`） | 三条 HTTP 用例的状态码断言 | **红在 `:152`、`:208`、`:272`**，且报文正是修复前的形态：`502` + `EXECUTION_FAILED` + 「action … 执行失败」。`TestHandlerDomainErrorReachesHTTPUnchanged` 等对照组全绿 |
| 4 | `ResolvedRef` 的 `label === undefined` → 恒真 | 连接表的 UUID→名字映射 | 红在 `RegistryPage.test.tsx:133/165/177/267`，其余 7 条绿 |
| 5 | `last_verified_at === null` → `!== null` | 「从未核验」那一对 | 红在 `:185` 与 `:193`（正反两条一起），其余 9 条绿 |
| 6 | `{data.dirty ? …}` → `{!data.dirty ? …}` | dirty 横幅 | 红在 `ChangesPage.test.tsx:271` |
| 7 | `{data.ahead > 0 ? …}` → `>= 0` | ahead 横幅 | 红在 `:296`；6 与 7 同跑，其余 14 条绿 |
| 8 | `getMigrations` 加 `.catch(() => 零值报告)` | 「读不到时显示错误」 | 红在 `:318`——**但那是正向锚点，不是我想证明的那条缺席断言** |
| 9 | 策略里给 `xm_worker_runtime` 也授视图 SELECT | worker 排除那条 | **红在 `policy_test.go:378`**，且**只红这一条**——只改了 Go 默认没动磁盘 JSON，所以摘要链那条对照组保持绿 |
| 10 | 改策略并重新生成 JSON，**但不追加事件**（正是要防的回归） | 摘要链的尾部绑定 | **红在 `policy_test.go:323`**，报文点名「event 3 (evt-core-schema-migration-state-view) says 108e9be2…, file is 2f76b36a…」——**它跟的是最后一条，不是写死的下标**，新断言形式因此确实生效 |
| 11 | `activeSub` 忽略 `?sub=`（恢复修复前那个「每一格都显示服务表」的行为） | `?sub=connectors` 那格的正向锚点 | **红在 `RegistryPage.test.tsx:157`**（`findByText("契约版本")`），共 13 条红；四条不依赖 `?sub=` 路由的用例保持绿（六格页签在、`?sub=services` 判据自检、三张表各打各的端点、口径句） |

**第 8 项要单独说：那次变异什么都没证明。** 它红在
`findByText(/服务内部错误/)` 这个锚点上，而下面
`queryByText("第 0 版")).toBeNull()` 根本没跑到。这两条在这个用例里是**互斥
的**（错误态渲染出来了，就不可能同时渲染出「第 0 版」），所以没有任何变异
能让锚点绿着而缺席断言红。改法是给它加一条**判据自检**用例：
「库确实一条都没跑过时如实显示『第 0 版』」——它证明这个匹配器在字符串真的
出现时抓得到，缺席断言因此不是恒真。

前端所有缺席断言都是**先 await 正向锚点、再同步 `queryByText`**，没有把
`waitFor` 套在缺席断言外面。子页签那组的三条缺席断言（`?sub=connectors`
时服务表不在、`?sub=connections` 时服务表不在、占位格里没有真表）另配了一条
**判据自检**用例——`?sub=services` 时「接入地址」必须找得到，否则那个匹配器
可能只是永远匹配不上而恒为 null。

## 门禁

**全部绿**（另一条线此前那两轮把前端弄红的中间态都已被他们自己修掉，
这一轮是干净的）：

| 门禁 | 结果 |
|---|---|
| `go test -p 1 -count=1 ./...`（带 `XM_TEST_DATABASE_URL`） | 退出 0，**59 个包全 ok**，无 FAIL / panic |
| `go vet ./...` | 退出 0 |
| `bash scripts/check-governance.sh` | 退出 0（显式捕获 `$?`，不是 `tail` 的） |
| `gofmt -l`（本片全部 Go 文件） | 无输出 |
| `go tool sqlc generate` | 退出 0（EXECUTE 包装就是为了这个，见 dbroles 那节） |
| `pnpm -r run typecheck` | 退出 0，5 个包全过 |
| `pnpm -r run test` | 退出 0 —— admin-web **1991 条全绿**（137 文件），ui-admin 261、ui-primitives 16、design-tokens 10 |
| `pnpm -r run build` | 退出 0；`admin-web build: ✓ 365 modules transformed.` + `dist/index.html` / `dist/assets/index-*.{css,js}` / `✓ built in 387ms` 产物行都在（`/tmp/f3.log` 第 147-153 行）。`ui-storybook` 这次没崩，但仍是**按产物行确认**的，不只看退出码 |

### 一处本片造成的连坐，已修：`router.test.tsx` 的「环境」查询

加了子页签之后，`router.test.tsx` 的
「登记服务（写路径）→ 环境取自身份且只读」**红了，而且那是本片造成的**：

```
TestingLibraryElementError: Found multiple elements with the text of: 环境
```

原因是资源目录新增的六格里有一格叫**「环境」**，Radix 会用触发器的文字给对应
的 `tabpanel` 挂 `aria-labelledby`——于是全局 `screen.getByLabelText("环境")`
同时命中「那个隐藏的面板」和「对话框里的环境字段」。

**断言本身没错**：报错信息里那个 `<input aria-label="环境" readonly
value="development">` 正是它要的东西，值和只读都对。错的是查询范围。

改法：这条用例本来就有 `openDialog()` 返回的 `within(dialog)` 句柄（同一个
describe 里别的用例都在用），只有它用了全局 `screen`。改成
`dialog.getByLabelText("环境")`，**断言一字未改**。`getBy*` 在零命中时抛错，
所以这个查询不会变成恒真。改完 `router.test.tsx` 163 条全绿。

**撞的两个元素究竟是什么**（值得写清，因为很容易被猜成别的）：错误信息里逐个
列出来的就两个——一个是 `<div role="tabpanel" aria-labelledby="…-trigger-environments"
hidden>`（**隐藏的、未激活的**那个面板），一个是对话框里的
`<input aria-label="环境" readonly value="development">`。
**没有第三个，也没有任何「环境筛选器」**：全仓带 `aria-label="环境"` 的控件
只有 `RegisterServiceDialog.tsx:137` 这一个（另两处命中在
`ui-primitives/src/Select.test.tsx`，是那个包自己的测试夹具）。
连接列表**没有**环境筛选控件——它按调用者身份的环境过滤，没有可选的下拉。

**顺带查了同类隐患**：六个页签标签里只有「服务」在别处也有同名控件
（`SMSExtrasPanels.tsx:141` 的 `aria-label="服务"`），但那在接码页，与
`/registry` 不同路由、不会同屏。其余四个（连接器 / 连接 / 支持能力 /
应用与模块）在全仓都没有同名控件。

**产品层面要不要改标签：判断是不用。** 两者一个是导航页签、一个是模态对话框
里的表单字段，**从不同时可操作**（错误信息里 body 上就带着
`pointer-events: none`、兄弟节点带 `aria-hidden`），读屏也按角色区分
（tab / textbox）。而且页签标签是**冻结的**——`ui-admin/navigation.test.ts:105-112`
把 `subLabels("/registry")` 逐字钉成那六个串，改它是另一件事。

⚠️ **给后面加页签的人的一条通则**：Radix 会用触发器文字给每个 `tabpanel` 挂
`aria-labelledby`，所以**页签标签从此也是一个 accessible name**。任何用
全局 `screen.getByLabelText("<某个页签名>")` 的既有用例都会因此变成多命中。
正解是把查询收窄到真正的容器（`within(dialog)` / `within(form)`），
**不要**改成 `getAllBy…[0]`——那会在将来多出第三个同名元素时静默选中一个
不确定的元素而测试照样绿。

⚠️ **`router.test.tsx` 也是另一条线在改的文件**（他们刚修完「今日到期」那组）。
我只动了 1608-1615 这一个用例，没碰别处；合入时若有冲突，取他们的版本再把这
一行的 `screen.` 换成 `dialog.` 即可。

## sqlc 生成物漂移（独立一节：给做同步那一片的人看）

**这不是本片造成的，本片也没有修它。** 单独成节是因为现在有多条线同时在加
迁移，谁先顺手 `sqlc generate` 一下，谁就会把一份 +462 行的无关 diff 卷进
自己那一片。

### 漂移从哪里开始

committed 的 `gen/models.go` 反映的 schema 停在**迁移 000021**。逐条验证
（拿 sqlc 的命名规则 `Schema+Table` 去 grep committed 的文件）：

| 迁移 | 代表类型 | 在 committed models.go 里 |
|---|---|---|
| 000015 ui_saved_views | `UiSavedView` | ✅ 在 |
| 000021 staff_accounts | `CoreStaffAccount` | ✅ 在 |
| **000023 server_registry** | `ServerServerAsset` | ❌ **不在——漂移从这里开始** |
| 000024 staff_totp | `CoreStaffTotpRecoveryCode` | ❌ 不在 |
| 000025 assurance_probes | `AssuranceProbeDeclaration` | ❌ 不在 |

（000022 只加索引不建表，所以最后一条被反映的建表迁移是 000021，
第一条没被反映的是 000023。**我早前口头说的「000025 之后」是错的**，
以这张表为准。）

重新生成会补进 **27 个**结构体，来自 assurance / cards / sms 三个 schema
外加 `core.approval_request`、`core.approval_decision`、
`core.staff_login_challenge`、`core.staff_totp_recovery_code`、
`server.server_*` 等。

### 重新生成会动哪 8 个文件

`sqlc.yaml` 里八份配置共用同一个 `schema: "db/migrations"`，所以**每一份都会
把全库的模型重写一遍**——八个文件内容彼此相同：

```
internal/platform/action/gen/models.go
internal/platform/alerts/gen/models.go
internal/platform/audit/gen/models.go
internal/platform/finance/gen/models.go
internal/platform/ops/gen/models.go
internal/platform/registry/gen/models.go
internal/platform/savedviews/gen/models.go
internal/platform/server/gen/models.go
```

各 +462 行左右，合计约 +3700 行。**注意 `*.sql.go` 不受影响**——那些只随
`db/queries/*.sql` 变化，本片改的 `registry/gen/registry.sql.go`（+85 行，
两个新查询函数）就是那一类，与本漂移无关。

### 为什么不能顺手一起生成

1. **它会把别人正在做的迁移一起卷进来。** `sqlc generate` 读的是
   `db/migrations` **当前目录的全部内容**，不是「我这一片加的那几条」。
   在多条线并行加迁移的时候，谁跑它，谁的 diff 里就会出现别人还没提交的表的
   模型——那些行没人 review 得了，出了问题也说不清是谁带进来的。
2. **八份内容相同的文件同时被改，是最容易冲突的形状。** 三条线各自生成一遍，
   就是三份互相冲突的 +462 行。
3. **它不属于任何一片的范围。** 补齐 000023 之后的模型是一次独立的、可验证的
   动作（跑一次生成、确认只有 models.go 变、跑一次全量测试），应当在一个
   **没有别的迁移在飞**的时间窗里单独做。

### 本片是怎么处理的

改了 `db/queries/registry.sql`（加两条查询）之后必须跑 `sqlc generate`，
于是八个 `models.go` 也被重写了。**我把那八个 `git checkout` 回去，只留
`registry/gen/registry.sql.go` 的 +85 行。** 这样本片的 diff 里只有自己的东西，
漂移原样留在原地等那一片来收。

### 给做同步那一片的人

- 先确认**没有别的线在飞迁移**（这是这一片唯一的前置条件）；
- `go tool sqlc generate` → 确认变的只有八个 `models.go`（`git status` 应当
  没有别的东西）；
- 跑 `go test -p 1 -count=1 ./...`：模型多出字段一般不破坏编译，但
  `emit_pointers_for_null_types` 下可空列的类型是**指针**，如果有代码按值
  接收过某个被重新生成的类型，那里会红；
- 顺带把 `scripts/ci-local.sh:170-184` 那条一致性检查跑一遍确认它转绿——
  **它今天是红的**，而 GitHub Actions 停摆期没人会撞见它。

## risks

- **`core.schema_migration_state` 是本仓库第一个跨 schema 的视图。**
  它的正确性依赖 PostgreSQL 的一条默认行为：视图 `security_invoker` 默认为
  false，所以对基表的权限检查走 owner。**这条默认将来若被改掉**（PG 允许
  `ALTER VIEW … SET (security_invoker = true)`），API 身份会立刻读不到它。
  已在真库上验证过 owner=xm_migrator、relkind=v、`security_invoker` 未设置。
- **角色策略的施加（DBR2）还没落到生产**，本片只更新了契约。也就是说这条
  授权今天是**目标态**，`cmd/db-role-verify` 对一次性集群验证得到；真正生效
  是在角色拆分部署那天。这一片做的正是「别让那天 fail closed」。
- **`credential_ref` 出在 `registry.read` 后面**（`staff` 默认持有它）。
  验收线已裁定**保留**，依据是仓库自己的明文规则
  `internal/platform/secrets/ref.go:16`：「**Ref 本身不是秘密，可安全出现在
  日志、配置与审计中**」。由此引出的 scope 档位不一致已登记进 follow_ups。
- **`ahead` 比的是「这个进程」与「它连着的库」**，不是仓库与库。一个跑着旧
  镜像的进程会报 `ahead: 0`，即便仓库里已经有更新的迁移。界面上写了这句。
- **「已应用」是按版本号推断的**（`version <= applied_version`），因为库里
  没有逐条台账。golang-migrate 是顺序 forward-only 的，所以这个推断成立，
  但它是推断——代码与界面上都写明了。
- 连接表的 UUID→名字映射依赖同页另外两份数据；连接器那格 403 时连接表的
  「连接器」列会退化成 UUID + 「不在本页清单里」。有用例覆盖，但那句话在
  这种情形下略有误导（对象是在的，只是这一格没取到）。

## follow_ups

1. **角色拆分部署那天（DBR2）要把 `core.schema_migration_state` 的 SELECT
   一并授出去**。契约已经写进去了，但施加权限是另一步——这正是本仓库
   「迁移新建表要重放 permissions」那条教训的落点。
2. **【留给产品负责人】`credential_ref` 的可见档位在两处不同，需要一次有意识
   的裁定。** 查完两边之后要先更正一句：**两处对 `credential_ref` 本身的判断
   其实是一致的**，不一致的是它们所在端点的 scope 档位，而那个档位是为端点的
   **别的内容**定的——`credential_ref` 只是顺带落在了不同档位上。

   | | 端点 | scope | 谁默认持有 | 出 `credential_ref` 的依据（原文位置） |
   |---|---|---|---|---|
   | 资源目录 | `GET /api/v1/connections` | `registry.read` | **staff + admin** | `registry/actions.go:100-104`：引用不是凭据，且「这条连接绑了哪个凭据」是复盘第一问 |
   | 成本登记簿 | `GET /api/v1/finance/upstream-accounts` | `finance.read` | 仅 admin | `httpapi/finance.go:137-139`：**同一条理由**——只出 Ref，明文在这条路径上根本不存在 |

   档位差的真正来源在 `router.go:577-580`：`finance.read` 之所以只给 admin，
   是因为那个端点还带着**充值倍率与令牌映射**——「倍率是商业条款、映射是成本
   归属的对账键，两样都比看板上的余额数字敏感一个量级」。**与 `credential_ref`
   无关。**

   **我的建议：维持现状，并把「Ref 不是秘密、不参与 scope 定档」写成一条明文
   规则。** 理由：`secrets/ref.go:16` 已经在**类型层**给出了这个判断，让它去
   驱动端点的授权档位等于把一条已经拍过的板重新打开；反过来把资源目录抬到
   admin，则是为了一个仓库明说「不是秘密」的字段，把整个资源目录对 staff 关掉。
   要推翻这条，该改的是 `secrets/ref.go` 那句话本身，而不是逐个端点各判一次。
3. **资源目录三个占位格**（支持能力 / 应用与模块 / 环境）各自在等什么，
   写在 `RegistryPage.tsx` 的 `PENDING_TAB_COPY` 里。「支持能力」那格最接近
   可做——数据已经在 `core.connector` 上，缺的只是一个按能力反查的投影。
4. **前端两处预拦截可以简化**：后端映射补上之后，
   `lib/subscriptionForms.ts` 的退款下限判断与
   `SubscriptionLifecycleDialog` 的 `alreadyTerminated` 已经不是「服务端说不
   清」的补救，只是省一次往返。留给后续切片决定要不要留。
5. **`ErrBatchImmutableField` 是否也要映射**（见缺口三末尾）。
6. **`blueprints/governance.ts` 的 `CHANGES_BLUEPRINT` 那五句 `source` 仍写着
   「随 Foundation-B（XM-0030）上线」**——这是 `ChangesPage` 里
   `honestBlueprintTab` 就地覆写的原因，上一片已经请求订正过，本片同样没动
   那个数据文件（它被 `blueprints.test.ts` 与占位页共同消费）。
7. **sqlc 生成物同步**——见上面那个独立小节，那一节就是给这一片写的。
8. 资源目录的**写入 UI**（connector.create L2 / connection.create L3 /
   connection.set_status L2）——要走审批流，单独一片。
