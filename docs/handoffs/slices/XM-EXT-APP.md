# XM-EXT-APP：应用与配置从只读蓝图建成真实页面

- **status:** READY（待验收线审读、复跑并人工合入）。**未上线、未部署、未推送。**
- **branch:** `ai/claude/XM-EXT-APP`，基线 `8c446e5`，工作树 `wt-XM-EXT-APP`。
- **来源：** 产品负责人 2026-09-08 的裁定变更（ADMIN-IA §5.4 原裁定「扩展能力
  四页只读蓝图、不得因此提前建后端」被推翻其中三页）。本片做 `应用与配置`
  一页；`接口与自动化` / `内容发布` 由并行切片做，`AI能力管理` 维持原裁定。

## 一、裁定变更已回写仓库

按仓库规矩（ADMIN-IA §九「改导航的顺序：先改本文件，再改 navigation.ts，
再改测试」），**先改的 `docs/architecture/ADMIN-IA.md`**：

- **§5.4** 原来的一行表格换成「原动作 / 现动作」两列的四行表，逐页写明哪一页
  被推翻、哪一页维持；原裁定文字保留在「原动作」列作为历史背景。
- **新增 §5.4.1「应用」指什么**：逐字记下产品负责人的定义——**平台自己纳管的
  前端站点**（admin-web、console 这类我们自己部署的前端），不是被管平台
  （Sub2API / NewAPI）的前端，也不是通用低代码页面搭建器。这条定义是数据模型
  的边界，写进文档才有约束力。
- **§九** 新增「扩展能力段『应用与配置』建成」一条；并在治理段那一条里就地
  标注「这一句已被 2026-09-08 的裁定变更部分推翻」——那句话原文是
  「现在仍走 PlaceholderPage 的只有扩展能力四页……它们是刻意的只读蓝图，
  不是待补的缺口」，今天只对 `/ext/ai` 还成立。

**并行切片提示**：另外两页的切片也会改 §5.4 的同一张表（各自把自己那行的
状态从 ⬜ 改成 ✅）。三片合入时这一节会冲突，冲突解法是**把三行的状态合到
一起**，不是三选一。

## 二、地基调查结论：哪几格能接真数据

先读了冻结的蓝图（`web/apps/admin-web/src/blueprints/ext.ts` 的
`EXT_APP_BLUEPRINT`），再逐个查了仓库里已有的东西。结论与任务书的判断一致，
但**「版本与发布」那一格的依据与任务书不同**，见下面 2.3。

### 2.1 `internal/platform/registry` 装不下「应用」——另起一张表，依据有三条

任务书要求「看它是不是已经能装下『应用』这个概念，能的话优先扩展它」。
查完的结论是**不能**，三条依据都在代码里指得出来：

1. **`core.service` 的行会直接出现在管理端侧栏的「平台」段。**
   `web/apps/admin-web/src/lib/platforms.ts` 的 `groupPlatforms()` 有一条
   「登记即出现」规则：目录里没有、注册表里有的 `service_type` 会被**自动补成
   一个平台条目**。把 admin-web 登记成 Service，侧栏立刻多出一个叫 `admin-web`
   的「平台」，除非再往 `NON_PLATFORM_SERVICE_TYPES` 排除名单里加一条——而那份
   名单存在的理由恰恰是「我们认识、并且已经确定它不是平台」。每登记一个前端
   就要改一次前端代码，等于登记簿不能自助登记。
2. **`core.service` 是 Connector / Connection 的挂载点**（ADR-004 / ADR-014）：
   一个 Service 意味着「平台会带着凭据去连它」。我们自己部署的前端站点没有
   Connector 也没有凭据要挂；让它成为 Service，会让「有 Service 就该有
   Connection」这条判断在整个注册表上不再成立。
3. **字段对不上**：Service 的动态事实是采集水位（`source_watermark` /
   `observed_at`，由 `registry.service.observe` 回写），前端站点的动态事实是
   「现在线上跑的是哪个版本」。两者不是同一类东西。

真正**同类**的先例是 `internal/platform/server`（XM-SERVER0 的服务器登记簿）：
纯记录、不装 Agent、不探测、L1 Action、专属 manage scope、读侧复用
`registry.read`。本片逐条照它做。

### 2.2 已有的东西里能接的与不能接的

| 查了什么 | 结论 |
|---|---|
| `/api/v1/ops/overview` 的 `build` 字段 | **接不上这一页**。它是**控制平面自己**（platform-api 二进制）的 Version/Commit，由 `-ldflags` 注入；它回答不了「admin-web 现在跑的是哪个版本」，更没有历史。`ChangesPage` 已经在用它，且已写明「不是发布历史」。 |
| `internal/platform/buildinfo` | 同上，两个全局变量而已。 |
| `internal/platform/registry` | 见 2.1，不扩展。 |
| `deploy/` 下的站点/域名清单 | **没有可作为真相源的清单**。`deploy/nginx/launch.conf` 是 staging 一键栈的单个 server 块（`server_name _`），`server-prod.conf.example` / `server-staging.conf.example` 是模板不是台账；域名散落在 `.env.example` 注释、CR 文档与测试常量里。所以域名只能**手工登记**——这也是 `core.server_domain`（XM-SERVER0）当初的同一个结论。 |
| `deploy/docker/web-app-config.sh` | **有用，而且用上了**：它定义了 `XM_WEB_AUTH_MODE` 只接受 `dev-header` / `oidc` / `local` 三个值（容器启动时校验，不认的直接拒绝启动）。「登录方式」这一列的枚举**逐字取自它**，不是现编的；`types_test.go` 有一条用例把两处钉在一起。 |
| `internal/platform/dbroles` | **刻意不改**，见第七节。 |

### 2.3 四格的最终归属

| 格 | 结论 | 依据 |
|---|---|---|
| 应用目录 | **接真数据** | 新表 `core.ext_app`。这是一个纯登记簿，与 `core.server_asset` 同一类。 |
| 页面配置 | **保持蓝图态** | 它是页面搭建器的概念（品牌/导航/公告/开关的可视化配置 + 实时预览）。平台没有这个对象。 |
| 页面组件 | **保持蓝图态** | 同上，组件库属于页面搭建器。 |
| 版本与发布 | **接真数据，但与冻结列头不是同一件事** | 见下。 |

**「版本与发布」这一格与任务书的判断一致，但依据不同，这里写清楚。**

任务书说这一格「能做实」。**能，但不是照冻结的列头做实的**。那张表的列头是
`配置版本 / 应用 / 创建人 / 阶段 / 测试结果 / 正式时间 / 回滚来源 / 状态`，
配上蓝图自己的流程说明「草稿 → 预览 → 待审核 → 测试环境 → 正式版本」——
**这整张表描述的是页面搭建器的配置版本流程**，与「页面配置」「页面组件」两格
是同一个不存在的对象。照它做实，等于先做页面搭建器。

真正能做实、而且产品负责人在定义「应用」时明确点名的，是**「当前发布的版本」**。
它需要一个来源，而平台今天没有任何地方记着「哪个前端在什么时候上了哪个版本」
（`ChangesPage` 已经写明：平台没有发布记录表，已发生的发布只在
`docs/handoffs/ACCEPTANCE-LOG.md` 与 `RELEASE-*.md` 里，尚不可查询）。

所以这一格做成了**两张表并排**：

1. **发布记录簿**（新表 `core.ext_app_release`，真数据）——记录**已经发生**的
   发布：哪个应用、什么时候、哪个版本 / 哪个提交、谁发的、是发布还是回滚。
2. **蓝图那张「配置版本」表**（冻结列头，表体「未接入」）——落款就地改写，
   写明它要的是页面搭建器的配置版本，不是上面那张簿。

⚠️ **发布记录簿不发布任何东西。** 平台没有、也不会有发布或回滚端点：发布与
迁移是 Platform Lifecycle Operation（宪法 2、3 条 / ADR-003），走版本化脚本 +
人工批准，不经 Action 通道。那为什么记录这件事是一个 Action？因为「往登记簿
里加一行」是一次平台配置写操作，而所有平台配置写操作必须经 Action（宪法 2
条）。**被记录的那次发布不经 Action，记录这件事必须经 Action**——两件事共用
「发布」两个字，不是同一件事。这条注释在包注释、迁移注释、Action 注释、契约
notes、前端 API 文件顶部各写了一份，因为它最容易被读反。

## 三、数据模型与迁移

`db/migrations/000053_ext_app_registry.{up,down}.sql`。

**编号改过一次**：任务书最初分配 `000050`，本片也是按 000050 做完并跑完门禁的；
2026-09-08 team-lead 通知 `000050` 已被 `plat-readonly-queries` 占用
（`000050_core_schema_migration_state.*`，当时还是未跟踪文件，别的工作树看不见），
本片改到 **`000053`**。文件名、`internal/platform/extapp/types.go` 里那处
「与 db/migrations/000053 的 CHECK 逐字同形」的引用、以及本文档全部一并订正；
全新库验证已按新号重跑（见 §十）。最终号段：

| 号 | 归属 |
|---|---|
| 000050 | `plat-readonly-queries` — `core.schema_migration_state` 视图 |
| 000051 | `ext-integration` |
| 000052 | `ext-publishing` |
| **000053** | **本片** |
| 000054 | `plat-scope-session`（若需要） |

### ⚠️ 改号带来的一个合并期陷阱（已实测，给验收线）

`cmd/migrate` 用的是 **golang-migrate v4**，它在 `public.schema_migrations` 里
只记**一个** `version`，不是「已应用集合」。于是：

> **任何已经单独跑过本分支的开发 / 测试库会停在 `version=53`；合并之后再
> `migrate up`，50 / 51 / 52 会被静默跳过——不报错、退出码 0，日志还写着
> 「无待应用迁移」。**

这不是推测，是在一次性探针库上实测出来的（探针库与临时目录跑完已删除）：

| 步骤 | 结果 |
|---|---|
| 用本分支的迁移目录 up 到底 | `version=53` |
| 往目录里补一个 `000051_gap_probe`，再 `up` | 日志「**无待应用迁移**」，`version` 仍 53，`core.gap_probe_51` **没建出来**（count=0） |
| 对照组：再补一个 `000054_gap_probe`，`up` | 正常应用，`version=54`，表建出来了（count=1）；而 51 那张**仍然是 0** |

对照组是必要的——只看第二步的话，「表没建出来」也可能是 `up` 整个坏了。

**影响范围**：

- **生产不受影响**：生产停在 `version=49`，四片合并后按 50→51→52→53 顺序一次
  跑完，没有跳号问题。
- **受影响的是本地/测试库**：谁在合并前用过 `ext-app`（或另外两片中号最大的
  那一个）的分支，谁的库就会缺号。处置办法是**重建那个库**
  （`scripts/dev/worktree-testdb.sh --drop` 再 `--print-url`），不要试图手工
  `migrate goto`——那会把 53 先 down 掉，而 down 是整链回滚。
- 这条与本片的改号**没有因果关系**：只要三片号段不连续地分头开发，谁的号最大
  谁就会造成这个现象。写在这里是因为本片正好是号最大的那个。

### `core.ext_app` — 前端应用登记簿

一行 = 一个我们自己部署的前端站点（在一个环境里）。

| 列 | 说明 |
|---|---|
| `app_key` | 稳定机器标识（admin-web、console……）。规则与 `core.service.instance_id` **同一条**正则，两张表里的键长得一样、能互相对照着读。 |
| `display_name` | 表格里显示的名字，可中文。 |
| `primary_domain` | **主机名**，不是 URL。可空（规划中的站点还没有域名）。 |
| `auth_mode` | `dev-header` / `oidc` / `local`，逐字取自 `XM_WEB_AUTH_MODE`。可空 = 未登记。 |
| `owner` | 负责人，必填。 |
| `status` | `planned` / `active` / `retired`。**登记簿口径，不是探活结果**——本包一次都不请求站点（同 `core.server_asset` 的三档）。 |
| `notes` / `environment` / `created_at` / `updated_at` | — |

约束：`(environment, app_key)` 唯一；`(environment, primary_domain)` **部分**唯一
（`WHERE primary_domain IS NOT NULL`——「没有域名」不是一个值，多个规划中的站点
都可以留空）；`environment` 外键指向 `core.environment`（宪法 15 条）。

**`primary_domain` 的形态本身是一道凭据泄漏闸**：它只接受主机名，于是
`https://user:pass@host/...` 这类把凭据写进地址的写法一个都过不了（`@`、`:`、
`/`、`?` 都不合法）。挡的是 `registry.validateEndpointCarriesNoCredential` 注释里
那条链——表单 → Action → 审计摘要 → 审计页回显，终点是一条**改不掉**的记录。
区别是那边要写一个专门的检查，这边靠字段形态就关掉了。领域正则与库层 CHECK
**逐字同形**（纵深防御；写法漂开的后果是「领域放行、库层拒绝」，那会以 500 的
形态出现，人看到「保存失败」却没有任何指向字段的说明）。

### `core.ext_app_release` — 发布记录簿

一行 = 「某个应用在某个时刻上了某个版本」这一件**已经发生**的事。

| 列 | 说明 |
|---|---|
| `app_id` | 外键 → `core.ext_app`，`ON DELETE RESTRICT`。 |
| `version` | 自由文本。**不强制语义化版本**：`deploy-local.sh` 今天把 `BUILD_VERSION` 写成环境名，强制只会逼人编一个假版本号。 |
| `commit_sha` | 可空，7~40 位小写十六进制（短 SHA 与全长 SHA 都收）。 |
| `kind` | `deploy` / `rollback`。回滚同样是「让某个版本上线」，所以 `version` 记的是**回滚到的那个版本**，不是被回滚掉的那个。 |
| `released_at` | 发布**真正发生**的时刻，可以是过去。 |
| `created_at` | **登记**时刻。与上面刻意分开：补记历史发布时两者差很远，而那正是「这条记录可信到什么程度」的线索。 |
| `released_by` | 执行那次发布的人，**可能不是来登记的人**——登记者由审计链的 actor 记录。 |

约束：`(app_id, version, released_at)` 唯一（挡表单双击与补记时抄两遍）；
`(app_id, released_at DESC)` 索引供「当前版本」查询。

**没有 `environment` 列**：环境挂在父行上，同 `core.server_service_note` 挂在
`server_asset` 下的形状——子表自称一个环境，就等于给了它一条与父行不一致的可能。

**没有 `is_current` 列**：「当前版本」是按 `released_at DESC` 现算的
（`ListAppsByEnvironment` 的 LEFT JOIN LATERAL）。存一列会与发布记录分叉，而它
唯一的来源就是那些记录。有一条集成用例专门钉住「取的是 released_at 最新的那条，
不是最后登记的那条」——补记历史发布时这两者会分叉。

## 四、Action 清单与定级依据

三个，全部 **L1 / `extapp.manage` / HUMAN-only / 三环境显式列举 / 不含
`environment` 参数**（环境取自调用者身份，宪法 15 条）。

| Action | 做什么 | 定级依据 |
|---|---|---|
| `extapp.app.set@1` | 登记 / 修改一个站点（整行替换，`app_id` 留空 = 新登记） | ADR-003 风险等级表第二行「修改低风险平台配置」。仓库内同类先例全是 L1：`server.asset.set@1`、`server.domain.set@1`、`finance.upstream_account.set@1`。不是 L2（「批量配置、启停低风险资源」）因为它一次只写一行、不批量、不启停、**不触碰任何第三方系统、不改变任何运行中的服务**。 |
| `extapp.app.retire@1` | 把站点标记为已下线（只改 status，`reason` 必填） | 同上。**它不会真的把站点关掉**——平台没有关停站点的通道，那是 Platform Lifecycle Operation。抬到 L2 会让「改一个记录字段」要走审批，而真正关站的那个动作反而不经过这里。 |
| `extapp.release.record@1` | 记录一次**已经发生**的发布 | 同上：写一行历史记录，不改变任何运行中的东西。 |

**关于「对 SERVICE 机器身份开放会让无人值守链路停摆」这条提醒**：本片三个
Action 都是 `PrincipalTypes: [HUMAN]`，**一个机器身份都没开**，所以不存在这个
风险。`extapp.release.record` 看起来最该由 CI / 部署脚本自动调，但今天平台里
**没有任何一条无人值守链路会调它**（GitHub Actions 自 2026-08-29 停摆，部署走
`deploy/scripts/deploy-local.sh` 由人在目标机上执行）——开放 SERVICE 换不来任何
自动化，只是先把一个写入面敞开。要接自动登记时该单独审一次，不是现在顺手放宽。
这条判断写在 `humanOnly` 的注释里。

**等级钉在两处**：Go 的 `RiskLevel`（`internal/platform/extapp/actions.go`）与
`contracts/actions/extapp.*.v1.json`。**没有门禁校验两处一致**（已确认：全仓
没有任何代码读取 `contracts/actions/*.json`，纯文档性质），所以改一处会静静留下
矛盾——改的时候两处都要改。

**不提供删除发布记录的动作**：一条能被删掉的历史记录不是历史记录。记错了的补救
是再记一条正确的，两条都留在簿上，谁在什么时候记的由审计链回答。契约里
`compensation_mode` 因此是 `MANUAL` 而不是 `AUTOMATIC`。

**不提供删除应用登记的动作**：删掉会让发布历史变成孤儿（外键是 `RESTRICT`，
库层也不让）。下线用 `retire`。

## 五、端点契约

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/ext/apps` | `registry.read` | 某环境下全部登记，**每行带上它当前跑的那个版本**（`current_release`，为 `null` = 没登记过任何发布）。 |
| GET | `/api/v1/ext/apps/releases` | `registry.read` | 某环境下全部发布记录，`released_at` 倒序。 |

两条都走既有的 `resolveEnvironment`（可选 `?environment=`，缺省取调用者身份，
跨环境读 403）。**这一对里没有发布端点，将来也不该有**。

`current_release` 为 `null` 时序列化成 JSON `null`，**不是空对象**——「没登记过
发布」与「版本是空的」是两件事（宪法 12 条同一条精神）。有一条 handler 用例配
对照组钉住这个，并做过变异验证。

## 六、scope 怎么处理的

- **写：新建 `extapp.manage`。** 任务书说「优先复用已有 scope」，这里**没有
  复用，理由写在这里以便推翻**：本仓库对「新登记簿」这一类切片最近一次的既定
  做法（XM-SERVER0）就是新建专属 scope，理由是「谁能改这个登记簿」应当是一道
  可以**单独审定、单独撤销**的授权面。逐个看候选项：
  - `registry.service.manage` 管的是被管系统实例，连带 Connector / Connection
    的挂载点；并进去等于「能登记我们自己的前端」与「能改被管系统的接入点」
    再也拆不开；
  - `server.manage` 管的是机房 / 采购 / 域名证书台账。前端站点跑在那些服务器上，
    但「谁能改采购台账」和「谁能改站点登记」不是同一个岗位。
- **读：复用既有的 `registry.read`**，不新建读侧 scope——能看服务清单与服务器
  登记簿的人本就该能看「我们自己有哪些前端站点」，三者是同一类知识面，泄漏面
  相当（同 XM-SERVER0 读侧的取舍）。这一条满足了「优先复用」的要求。
- `oidcauth.DefaultRoleScopeMap` 的 `admin` 角色新增 `extapp.manage`；
  `resolver_test.go` 的 `TestDefaultRoleScopeMapIsConservative` 新增两条断言
  （admin 含它 / staff 不含它）并写明理由——与 `server.manage` 同一档，
  **不是** `finance.platform_channel_binding.manage` 那类必须人工显式授予的
  高风险面。
- **`STAFF_ROLE_CATALOG` 没有改，而且不需要改。** 任务书说「新 scope 要同步
  `staff.ts` 的 `STAFF_ROLE_CATALOG`（有测试钉着两边一致）」——查了那两条测试
  （`oidcauth/rolemap_catalog_test.go`），它们钉的是 **role**（`staff` / `admin` /
  `fund-operator` 这类 Keycloak 粗粒度角色），不是 scope。本片新增的是一个
  **scope**，挂进已有的 `admin` 角色，没有新增角色，所以那两条断言不受影响
  （全量 `go test` 里它们是绿的）。
- **`oidcauth.platformScopePrefixes` 没有加 `extapp.`**，这是一次刻意的选择：
  那份清单今天没有 `server.` / `finance.` / `card.` / `sms.` / `alerts.` /
  `approval.` 里的任何一个，只加我这一个会让人以为它是穷举的。见第九节
  follow_ups 的扫一遍建议。

## 七、dbroles：**刻意未改**，与任务书的要求不同，依据在这里

任务书要求「新表要同步 `internal/platform/dbroles/` 的授权——本仓库曾因漏授权
导致生产功能失败 15 分钟」。**查完之后没有改，理由如下，请验收线复核这个判断。**

1. `contracts/database/role-policy.v1.json` 是一份**独立维护、经人工批准的快照
   契约**，`policy_test.go` 会把它与 Go 代码算出的「approved v1 inventory」逐项
   比对，缺项直接判 `OBJECT_MISSING`。往 `policy.go` 加对象而不同步走它的批准
   流程，会直接把 `TestCheckedInPolicyContractLoads` 打红——**这条治理机制本身
   就是在拦「改了库权限范围但没走审批」这类变更**，绕过它是错的。
2. 这不是新问题，也不是本片特有：那份 inventory 停在一个更早的发布基线上，
   `core.server_*`（000023）、`core.sms_*`（000041~48）、`core.action_approval`
   （000049）**一张都不在里面**。XM-SERVER0 的交接文档已经把这条完整记过一次，
   并且是「起初加了、然后主动 `git checkout --` 撤销」。本片沿用同一个结论。
3. **影响面**：当前 `deploy/compose/launch.yaml` 仍以 `${POSTGRES_USER:-xingmang}`
   给 API/worker 组装 DSN（owner 角色），DBR3 runtime cutover 未合入，所以这个
   缺口**不阻塞当前部署形态**。但在任何 DBR3 / 受限角色切换之前，它是必须先
   关闭的硬门禁——而那时要关的不止本片这两张表。
4. 任务书提到的「曾因漏授权导致生产功能失败 15 分钟」那次，是**开票线**
   （`invoice_app` 的表权限来自 compose 的 permissions 作业）的事故，与本仓库
   的 `dbroles` 快照契约不是同一套机制。两者容易混。

## 七之二、sqlc：本片保留 **0 个**生成文件

**本片一个 `gen/` 文件都没动，`sqlc.yaml` 也没动**——
`git diff --name-only 8c446e5..HEAD | grep -E "gen/|sqlc"` 无输出。

原因是 `internal/platform/extapp` **压根没走 sqlc**：Store 是手写 pgx，与
`internal/platform/approval`（本仓库上一次新建带表的模块，迁移 000049）同一个
选择，理由写在 `store.go` 的类型注释里——本包只有七条查询，为它新开一个 sqlc
输出块会把整仓的 `gen/*.go` 拖进一次重新生成，代价与收益不成比例。

### 但本片的迁移确实会**放大**那处已知漂移，这里给出实测数字

`sqlc.yaml` 的每个输出块都写着 `schema: "db/migrations"`，所以每个包的
`models.go` 是**全 schema 的**（`registry/gen/models.go` 里就有
`CoreServerAsset`、`CoreStaffAccount` 这些别的域的表）。于是新增一张表 =
8 个 `models.go` 都会想多一个 struct。

为了给「历史漂移单开一片」提供数字，我**跑了一次 `go tool sqlc generate` 做
测量，然后把 8 个文件全部 `git checkout` 还原**（还原后 `git status` 干净、
`git diff HEAD -- internal/platform/*/gen` 为空，已复核）：

- 全量重新生成的 diff：**8 个 `models.go`，每个 +488 / −9 行，合计
  +3832 / −72**；
- 其中**属于本片两张表的只有约 25 行/文件**（`CoreExtApp` 11 行 +
  `CoreExtAppRelease` 12 行 + 空行），其余约 463 行/文件是 **000023 之后
  累积的历史漂移**，与本片无关。

  **漂移起点是 000023 不是 000025**（team-lead 转述 `plat-readonly-queries` 的
  逐条 grep 验证：000021 的 `CoreStaffAccount` 在生成结果里，000023 的
  `CoreServerAsset` 不在），重新生成会补进 **27 个**结构体。它量到 +462 行/文件、
  我量到 +488 行/文件，差值正好是我这两张新表的约 25 行——两份数据互相印证，
  不必再测。

### 为什么本片一行生成结果都不留（与 `plat-readonly-queries` 那片不同）

那一片留了 `registry/gen/registry.sql.go` 的 85 行，因为它**真的新增了查询、
代码真的调用了生成的方法**。本片不一样：`CoreExtApp` / `CoreExtAppRelease`
这两个 struct **没有任何代码使用**（Store 手写 pgx，不 import `gen`）。留下
它们等于在 **8 个共享文件**里各放一段死代码，而那 8 个文件正是三个并行切片
最容易撞车的地方。所以正确动作是 **0 保留**。

### `scripts/ci-local.sh` 的 sqlc 一致性检查

第 170–186 行：`go tool sqlc generate` 之后
`git diff --exit-code -- internal/platform/*/gen`。**它今天本来就是红的**
（那处历史漂移），本片没有跑它，也**没有为了让它变绿而重新生成**——那正是
要避免的事。上面那次测量跑完就还原了。

**给做漂移同步那一片的人**：合并三个 `ext-*` 切片之后再生成，届时
`models.go` 里除了 000023~000049 的历史欠账，还应包含
`CoreExtApp` / `CoreExtAppRelease`（本片 000053）以及另外两片 000051 / 000052
的表——一次生成全部收进去，不要分三次。

## 八、前端

- `navigation.ts`：`/ext/app` 的 `built` 翻成 `true`。**`stage` 仍留「后置」**
  ——它说的是这一段在信息架构里的优先级，不是实装进度；实装进度由 `built`
  表达。`navStageHint()` 对 `built: true` 返回 `undefined`，所以侧栏标签正确地
  消失了。
- `router.tsx` 新增 `{ path: "ext/app", Component: ExtAppPage }`。**必须显式加**
  ——`placeholderRoutes` 只收 `!item.built` 的条目，翻了 `built` 就掉出来，不补
  路由会落到 `*` 兜底 404（与 XM-OPS-TAILS0 记录过的 `/jobs` 那次缺口一样）。
- **`PlaceholderPage.tsx` 的 `/ext/*` 横幅与占位文案由 team-lead 统一处理，
  本片未动**（`git diff 8c446e5..HEAD` 里没有这个文件）。三个并行切片的工作树
  各有一份副本，各改一遍必在同一段逻辑上撞车，所以由 team-lead 在三片都落地后
  一次性改对：`PlaceholderGate`（约 159–163 行，按 `path.startsWith("/ext/")`
  **无条件**挂横幅）改成只对仍是蓝图的页挂（届时只剩 `/ext/ai`），
  `PLACEHOLDER_COPY`（约 29–31 行）里那三条「只读蓝图」相应清理。
  **审读时请不要把这当成本片的遗漏。**
- 本片这一页因此**不依赖那次统一改动**：`/ext/app` 已经不走 `PlaceholderPage`
  这条路径（`built: true` → 掉出 `placeholderRoutes` → 走显式路由），所以那条
  横幅今天就已经不会挂到它身上——`router.test.tsx` 新增的路由挂载用例与
  `ExtAppPage.test.tsx` 各有一条断言钉住「`/ext/app` 上没有那句话」。
  那条「只读蓝图：仅预览、不保存、不发布、不执行」对本页是假话（本页真的会
  保存），**取而代之**的是 `ExtAppGate`：
  本页只做登记，不发布、不回滚、不重启任何站点；发布与回滚是 Platform Lifecycle
  Operation；状态列是登记值不是探活结果。
- 新文件：`api/extapp.ts`、`pages/ExtAppPage.tsx`、
  `components/ExtAppCatalogPanel.tsx`、`components/ExtAppReleasesPanel.tsx`。
- `blueprints/ext.ts`：**只改了 `EXT_APP_BLUEPRINT` 的 `source` 文案与文件头
  注释，列头 / 页签 id / 页签名 / 统计格标签一个字没动**（它们是冻结的设计产出，
  `blueprints.test.ts` 与 `navigation.ts` 逐条对账）。
  - 为什么在数据文件里就地改，而不是像 `ChangesPage` 那样在页面里覆写：那边
    `governance.ts` 同时被五个页面与占位页消费，改一处会波及别人；这里
    `EXT_APP_BLUEPRINT` 建成后只有 `ExtAppPage` 一个消费者，就地改比留一句
    假话再在唯一的消费者里盖掉更诚实（ChangesPage 的注释自己也请求过「把
    governance.ts 的五句一并订正」）。
  - 另外三页的 `NO_BACKEND` 常量一字未动。
- 「配置版本」这一列**留列不留数**：列在、每行显示「未接入」并带 title 说明。
  删列会与设计稿对不上，摆空值会被读成「这个应用还没有配置版本」。同 XM-0048
  上游表里那三列的处理。
- 「详情」做成**行展开**而不是详情页：一个站点的登记只有十来个字段，为它开一条
  路由等于多一个要维护的地址。

## 九、测试与变异验证明细

### 后端

- `internal/platform/extapp/types_test.go`（纯领域，无 I/O）：主机名只收主机名
  （12 种坏形状 + 4 种好形状的**对照组**）、可选字段留空、app_key 形态、
  枚举、`auth_mode` 三个取值与 `web-app-config.sh` 逐字一致、发布记录必填项与
  提交号形态（坏 5 种 + 好 3 种对照组）。
- `internal/platform/extapp/actions_test.go`：声明合法性、参数校验、
  `released_at` 只收带时区的 RFC3339（**错误码 + 文案逐字**）、审计摘要的
  「未登记字段不写键」（配对照组）。
- `internal/platform/extapp/store_integration_test.go`（真库）：CRUD 往返、
  整行替换真的清空未给字段、两处唯一索引（各配对照组）、库层 6 条 CHECK
  绕过领域校验直接写库仍被拒（**配一条「合法的一行必须插得进去」的对照组**，
  否则一张谁都写不进的表也能让那六条全绿）、外键 RESTRICT（配「删掉子行之后
  父行就能删」的对照组）、当前版本取 released_at 最新那条、环境隔离、倒序。
- `internal/platform/extapp/actions_integration_test.go`（真库）：新建/整行替换
  两条分支、域名归一到小写**并因此撞上唯一索引**、跨环境闸门三个动作各一条
  （**配同环境三条成功的对照组**）、下线只改状态不动别的字段、记录发布时父行
  不存在给的是可读错误而不是外键违例、`released_at` 留空兜底成现在 vs 显式给
  过去时刻原样存下、提交号归一。
- `internal/platform/httpapi/ext_apps_test.go`：两个端点的返回形状与 403、
  `current_release` 为 null 的序列化（配对照组）、跨环境读 403 **且不查库**
  （配同环境放行且查库的对照组）。

### 前端

- `router.test.tsx` 新增一组「应用与配置路由挂载」（2 条）：**真的导航到
  `/ext/app`**，断言渲染的是真实页面而不是兜底 404、不是占位页、不再挂
  「只读蓝图」横幅、不再有「未建」徽章；以及 `?sub=releases` 落到发布记录簿。
  这一组补的正是 XM-OPS-TAILS0 记过的那个坑：`built` 翻成 true 的那一刻这一页
  就掉出 `placeholderRoutes`，`ExtAppPage.test.tsx` 直接渲染组件、不经真实路由，
  两边各自绿掉，缺口留在中间。`okHandler` 同时补了 `/api/v1/ext/apps` 与
  `/api/v1/ext/apps/releases` 的默认空响应（**releases 那条必须排在前面**：
  它以 `/api/v1/ext/apps` 开头，反过来写会被静默吞掉）。
- `pages/ExtAppPage.test.tsx`（10 条）：四格页签、**不再挂只读蓝图横幅**、
  冻结列头 + 配置版本留列不留数、未登记发布 vs 已登记、两格蓝图态的落款、
  发布记录簿是真表且蓝图那张表仍在、「当前」徽章（配历史行不挂的对照组）、
  拼错 `?sub=` 不回落、URL 当域名时**就地拦下且不发请求**、记录发布走
  `extapp.release.record@1` 且没有任何 `deploy.*` / `release.*` 执行动作。

### 变异验证（7 项，全部「改条件」不「删代码」，且每项都确认了**没变红的对照组**）

| # | 变异 | 期望 | 结果 |
|---|---|---|---|
| 1 | `appSetDef` 的 `RiskLevel` L1 → L2 | 「三个动作直接执行、不落审批单」变红 | **红**，且红在 `actions_test.go:206`（「没走到 Handler」那一行），只有「登记站点」子用例红；对照组（L2 假动作落单）与另外两个动作仍绿 |
| 2 | `requireSameEnvironment` 判断改成 `if false`（恒放行） | 跨环境闸门三条变红 | **红**，三条都红在「期望被拒，实际返回 nil」；同一用例里的同环境对照组仍绿 |
| 3 | `appSetHandler` 去掉主机名的 `strings.ToLower` | 域名归一那条变红 | **红**，红在「大小写混排的主机名应当被归一后接受」 |
| 4 | LATERAL 的 `ORDER BY released_at DESC` 改成 `ASC` | 「当前版本取最新那条」变红 | **红**，红在 `store_integration_test.go:368`，其余用例全绿 |
| 5 | `optionalTimeParam` 加一条不带时区的解析回落 | 「必须带时区」那条变红 | **红**，红在 `actions_test.go:301` |
| 6 | 没有发布时把 `CurrentRelease` 填成一个空 `Release` 而不是 nil | 「没登记发布时是 null」变红 | **红**，红在 `store_integration_test.go:414`（正是那条缺席断言） |
| 7（前端 A/B/C） | A：把旧的只读蓝图横幅**加回来**（新门禁保留）；B：配置版本列改成渲染空值；C：没登记发布时渲染空版本号 | 各对应一条变红 | **A/B/C 各红一条**，另外 9 条全绿 |
| 8 | `router.tsx` 里把 `{ path: "ext/app" }` 改成 `"ext/app-elsewhere"`（**改路径不删行**，import 仍在用，编译不受影响） | 新增的两条路由挂载用例变红 | **红**，且**只有那两条**红；`router.test.tsx` 其余 160 条全绿 |

**过程中被对照组抓到一次真的恒真**：`TestRegistryWritesRunDirectly…` 最初写的
`humanCtx` 漏了 `Issuer`，`Principal.Validate()` 因此失败，内核在**风险闸之前**
就以 `PERMISSION_DENIED` 拒掉了三次调用——「一张审批单都没落」于是恒真，整组
用例什么都没测到。是那条 L2 对照组（期望落 1 张、实际落 0 张）把它翻出来的。
补了 `Issuer` 之后又发现三条会一路撞到 nil 连接池上 panic，于是改成**故意传一个
不合法的 `app_id`**——「app_id 不是合法 UUID」这句话在内核里没有第二个来源，
拿到它就等于拿到「这次调用被放行到了执行路径」的正向锚点。这两处修正的痕迹留在
测试注释里。

**另一次变异被判定为无效**：变异 6 最初写成把 `if relID != nil` 改成 `if true`，
结果是实现里解引用 nil 指针 **panic**——用例确实红了，但红在 panic 上，那条缺席
断言根本没跑到，**什么都没证明**。改成「在末尾把 nil 替换成一个空 `Release`」
之后才红在断言那一行。

### 一处收尾时自查出来的问题

写完之后逐句核对页面文案时发现：`BlueprintView.tsx` 把落款当**纯文本**渲染
（`<p>{tab.source}</p>`、`PageState description={table.source}`），所以我在
`blueprints/ext.ts` 与三个组件里写的 markdown 星号强调（`**留列不留数**` 这类）
会**原样显示在页面上**。已全部改成「」引号。`ExtAppPage.test.tsx` 里那条断言
也顺势加了一句 `expect(legend.textContent).not.toMatch(/\*\*/)`，钉住这件事。

顺带发现 `blueprints/governance.ts` 的 `TAB_SOURCE.database` 里也有一处
（「这一格等的**不是** Foundation-B」），是 XM-CHANGES0 留下的，**本片没有动它**
——那是另一页的文案，改它不属于这一片。已记进 follow_ups。

## 十、门禁

全部在 `wt-XM-EXT-APP` 本地跑通（Go 命令一律带八个代理变量的 `env -u` 前缀，
见 windows-toolchain-quirks）：

- `go build ./...` —— PASS
- `go vet ./...` —— PASS（退出 0）
- `XM_TEST_DATABASE_URL=$(bash scripts/dev/worktree-testdb.sh --print-url)`
  `go test -p 1 -count=1 ./...` —— **PASS，全仓无失败**（`-p 1` 没漏）
- `gofmt -l` 本片改动的每个 `.go` 文件 —— 干净（0 输出）
- `bash scripts/check-governance.sh` —— PASS（退出 0，无输出）
- **迁移在全新库上正向验证（改号后已重跑）**：新建 `xm_test_extapp_fresh53`，
  `go run ./cmd/migrate ... up` 跑完 `schema_migrations` = **`53 / dirty=f`**，
  `core.ext_app` 与 `core.ext_app_release` 两张表都在；随后跑
  `... down` 无报错（该 CLI 的 `down` 是整链回滚，不是单步），确认
  `000053_..._down.sql` 是有效 SQL；用完已 `DROP DATABASE`。
  本 worktree 的测试库也已 `--drop` 后重建——否则它停在旧的 `version=50`，
  改号后再 `up` 会撞「表已存在」。
- 前端（`--config.verify-deps-before-run=false`，node_modules 由 junction 镜像
  自主检出）：
  - `pnpm -r run typecheck` —— PASS（5 个包全 Done）
  - `pnpm -r run test` —— PASS：design-tokens 10 / ui-primitives 16 /
    ui-admin 261 / admin-web **1937**（136 个文件），全绿
  - `pnpm -r run build` —— PASS。**按任务书提示 grep 确认了 `admin-web build`
    真的跑了**：输出里有 `✓ 363 modules transformed` 与
    `dist/assets/index-*.js`；本次 `ui-storybook` 没有触发那个 libuv 拆卸崩溃
    （exit 3221226505），`pnpm -r` 没有中止。
- `gitleaks protect --staged` —— PASS（`no leaks found`）。
- `scripts/ci-local.sh` —— **未跑**。它内含 sqlc 一致性检查（第 170–186 行），
  而那一项因为仓库里那处历史漂移**今天本来就红**；跑它只会得到一个与本片无关
  的红灯，而让它变绿的唯一办法正是被明确禁止的「全量重新生成」。本片改为逐项
  跑它包含的其余门禁（`go vet` / `go test -p 1` / 迁移 / `check-governance.sh`），
  结果见上。

## 十一、not_run

- **真实浏览器 / Playwright**：未跑。vitest + jsdom 覆盖了列表渲染、Dialog 表单
  校验、Radix Select 选项选择、Action 提交与回执；本片没有横向滚动 / 粘性列 /
  复杂布局这类只有真浏览器布局引擎才测得出的东西。
- **Storybook 素材**：未加。本片没有新增 / 修改任何 `ui-admin` / `ui-primitives`
  组件，两个面板全部复用既有的 `DataTableV2` / `Dialog` / `FormField` / `Badge`。
- **生产 / staging 部署验证**：未跑（任务书要求不碰生产）。合入后需确认
  `go run ./cmd/migrate up` 在真实部署环境把 000053 应用上。
- **`contracts/actions/*.json` 的机器校验**：本仓库没有代码读取它们（已确认，
  纯文档），只用 `json.load` 逐个过了语法。

## 十二、risks

- **`extapp.manage` 与 `dbroles`**：见第七节。当前 owner-role 部署形态不受影响；
  DBR3 切换前必须先关。
- **发布记录簿要靠人记，不会自己长出行。** 平台不读部署流水线，也没有 CI 可读
  （GitHub Actions 停摆中）。没人记 = 空表 = 应用目录的「发布版本」列一直是
  「未登记发布」。空状态文案已经把这件事写明（「这张表是登记出来的，不会自己
  长出行——平台不读部署流水线」），但它仍是这一格最现实的失效模式。
- **「当前版本」按 `released_at` 判定，不按 `created_at`。** 两条记录 `released_at`
  完全相同时，谁是「当前」取决于 `created_at`（次级排序）。唯一索引挡住了
  `(app_id, version, released_at)` 完全相同的重复，但两个**不同版本**登记在同一
  时刻是允许的（集成用例里就有），那时「当前」是后登记的那条。
- **`primary_domain` 与 `core.server_domain` 没有打通。** 后者是注册商 / 证书
  台账（多为 apex 域名），前者是站点的具体主机名；要求前者必须先在后者登记会让
  站点登记被一个无关台账卡住。代价是同一个域名可能在两张表里各写一遍且不一致。
  见 follow_ups。
- **本片没有任何「探测」能力，状态列全靠登记值。** 一个已经挂掉的站点在这里
  仍然显示「在线」，直到有人来改。这与「服务器只做记录」是同一个取舍，页面上
  的门禁说明写明了这一点，但仍要提醒：**别把这一页当监控看**。
- **`?environment=` 只允许读自己环境**，所以生产的登记在 staging 后台看不到。
  这是既有 `resolveEnvironment` 的规则，不是本片新增的限制。

## 十三、follow_ups

1. **`AI能力管理` 这一页**：§5.4 明确「维持原裁定」。`router.test.tsx` 里
   「占位页有页头与『未建』徽章」「子页签进 `?sub=`」两条用例的样本已经搬到
   `/ext/ai`（这是第四次搬家）。如果哪天它也建了，那两条用例该做的是**删掉**，
   而不是再找一页顶上——那时 `PlaceholderPage` 这条路径就真的一个使用者都没有了。
2. **`oidcauth.platformScopePrefixes` 扫一遍**：那份「绝不允许出现在 Keycloak
   令牌里」的命名空间清单已经落后于现实（`server.` / `finance.` / `card.` /
   `sms.` / `alerts.` / `approval.` / `fund.` / `assurance.` 一个都不在里面）。
   本片没有单独加 `extapp.`，因为只加一个会让它看起来像穷举的。建议单开一片
   一次补齐，并加一条测试钉住「每个 `DefaultRoleScopeMap` 里出现过的 scope 前缀
   都在这份清单里」。
3. **发布记录的自动登记**：真要接的话，给它一个单独的、SERVICE 身份可用的
   Action 定义并连同凭据一起审一次，不要把现在这个的 `PrincipalTypes` 放宽。
4. **`ext_app.primary_domain` ↔ `core.server_domain` 的关联**（见 risks），
   如果产品侧确认需要「点这个站点跳到它的域名与证书登记」这类导航。
5. **页面搭建器**：「页面配置」「页面组件」两格与「版本与发布」里那张冻结的
   「配置版本」表，等的是同一个裁定——要不要做这个功能。裁定之前它们接什么
   都定不下来。
6. **`blueprints/governance.ts` 的一处 markdown 星号**：`TAB_SOURCE.database`
   里的「这一格等的**不是** Foundation-B」会在版本与发布页上原样显示两个星号
   （落款是纯文本渲染）。本片没有动它——那是另一页的文案。
7. **`internal/platform/*/gen` 的历史漂移同步**（单独一片，由 team-lead 安排）：
   见 §七之二。做那一片时本片的两个 struct 会一并被生成进去，本片什么都没漏。
8. **`ChangesPage` 的「发布与回滚」格与本页的关系**：那一格答的是「控制平面
   现在跑的是哪个提交」（后端二进制），本页答的是「各个前端站点上了哪个版本」。
   两者不重叠，但名字很像；如果将来要把控制平面自己也登记成一个「应用」，
   得先决定它的 `app_key` 与那条 build 读数谁是真相源。
