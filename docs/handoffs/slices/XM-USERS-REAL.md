# XM-USERS-REAL · platform-api 用户清单接通真实 Sub2API / NewAPI

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-USERS-REAL`
- implementation commit: `c81c255`（本分支 HEAD，单提交）
- base: `release/v0.1-launch`
- worktree: `K:/星芒统一控制平台/acceptance/wt-users-real`

## summary

`XM_PLATFORM_USERS_MODE=real` 现在真的能连上 Sub2API 与 NewAPI，不再对
`ListUsers` 一律返回 `not_supported`。

### connectors/platformusers（真实客户端）

按“接入清单”六项逐一核对上游**源码**（只读参考，未做任何改动）：

1. **分页形态**：两个上游都只有 `page`/`page_size`（Sub2API）与 `p`/`page_size`
   （NewAPI）的 offset 分页，没有真正的不透明高一致性游标。契约要求游标，
   所以按“接入清单第 1 项”降级：`NextCursor` 是偏移量的十进制编码
   （`listing.go` 的 `paginateUsers`），并在代码注释里写明这是 offset 分页的
   固有限制（两次读取之间若有用户被增删，边界上的一条可能重复或漏掉）。
2. **字段名**：
   - Sub2API：`GET /api/v1/admin/users`（`backend/internal/handler/admin/user_handler.go`
     的 `List` + `backend/internal/handler/dto/types.go` 的 `User`/`AdminUser`）——
     `id`/`email`/`username`/`status`（`"active"`/`"disabled"`，仅两值）/
     `balance`（float64 美元）/`last_active_at`（可空 RFC3339）。
   - NewAPI：`GET /api/user/`（**尾斜杠不能省**，`router/api-router.go` 注册的是
     `"/"`；`controller/user.go` 的 `GetAllUsers` + `model/user.go` 的 `User`）——
     `id`/`username`/`display_name`/`email`/`status`（int，1=启用/2=停用）/
     `quota`（int）/`last_login_at`（unix 秒）/`DeletedAt`（**没有 json tag**，
     键是大驼峰 `"DeletedAt"`；`Unscoped` 查询含软删除用户，已按此字段过滤）。
3. **金额单位**：Sub2API 是 `decimal(20,8)` 美元字符串/浮点字面量，走
   `amount.go` 的 `decimalToMinorUnits`（定点、零 float）；NewAPI 是整数
   `quota`，换算基数 `quota_per_unit` 来自 `/api/status`、**运行期可变**，
   每次 `ListUsers` 都现读，换算走 `quotaToMinorUnits`（先 SUM 再除，误差
   不按人数累积）。两边记账币种都是 **USD**（不是 fake 样本用的 CNY）。
4. **逐用户充值/消费**：v1 契约给不出，`PeriodRecharge`/`PeriodConsumed`/
   `Last30dConsumed` 恒为 `UnknownAmount()`，不填 0。
5. **状态枚举**：`ParseUserStatus` 补了 NewAPI 的整数状态 `"2"`（停用）；
   Sub2API 的 `"active"`/`"disabled"` 已被既有分支覆盖。
6. **联系方式**：两边的用户结构体都核对过全字段，**没有手机号**，只有
   email 一项；已走既有 `MaskEmail` 打码，明文不出连接器。

**没有转发 Query/Status 给上游**：Sub2API 的 `search` 与 NewAPI 的 `keyword`
都同时匹配邮箱（`model/user.go` 的 `SearchUsers`：
`username LIKE ? OR email LIKE ? OR display_name LIKE ?`）——把契约层已打码
的邮箱又暴露成一个“搜索命中即证明这个邮箱存在”的预言机。改为翻全量、在
本机按 `contract.go` 的 `User` 字段过滤/排序/分页（`listing.go`，与
`fake.go` 同一套语义），`UserPage.TotalBalance`/`ActiveToday` 本来就要求
“全体用户”口径，这个决定同时解决了这两件事。

**分页预算**：Sub2API 每页 1000、上限 20 页；NewAPI 每页 100（上游硬顶）、
上限 50 页——与 `connectors/sub2api`、`connectors/newapi` 两个已上线的
metering 连接器同一预算。预算耗尽时 `Snapshot.Watermark` 追加
`;truncated`，且 `TotalCount` 退化为 `Unknown`（下界不冒充总数，宪法 12
条）；`TotalBalance`/`ActiveToday` 仍报出部分合计（与契约 `Totals` 的
“合计是下界也要报”同一条理由）。

`RealConfig`/`NewRealClient` 的构造期护栏（https、CredentialRef 形态、非空
allowlist、SecretProvider 非空）保持不变；新增 `WithBaseTransport`/
`WithClock` 两个 `Option`（契约测试用，生产装配不需要传）。

### cmd/platform-api（每次请求解析生效配置）

`buildPlatformUsers` 不再在 real 模式直接 `os.Exit`；每个平台拿到的是
`dynamicUsersClient`，每次 `ListUsers`/`GetUser`/`DailyUsage`/
`ListKeyMetadata` 调用都重新 `jobs.NewPgConnectorConfigSource(pool).Get(...)`
（30s 缓存）读一次 `core.connector_config`，行里非空字段覆盖进程级
`XM_PLATFORM_USERS_MODE` 缺省——与 worker 的
`NewDynamicSub2APIClientFactory`/`NewDynamicNewAPIClientFactory` 同一条纪律。
运营在后台（`connector.config.set@1` Action，XM-CRED0 已提供）把某个平台切
成 real、填端点/白名单/凭据引用，platform-api **不需要重启**就能生效。

`production` 环境下生效模式仍是 `fake` 时，`ListUsers` 返回
`connector.KindNotSupported`（`errors.Is` 能认出 `jobs.ErrConnectorProductionFake`，
与 worker 同一枚哨兵），绝不在生产返回样本数据（宪法 12 条）。库读不到时
按进程级缺省处理（记一条 warn 日志），不是硬错误；配置行的 `mode` 列解析
失败时报错而不是静默回落（回落会让人以为后台切换生效了，其实没有）。

新增 `platformUsersSecretProvider`：platform-api 此前**只写不读**
`XM_SECRET_ROOT`（凭据登记只落文件，由 worker 读）；本片比照
`cmd/platform-worker/secret_chain.go` 的 `connectorSecretsChain` 装一个
审计过的文件优先 `SecretProvider`（没有那份文件里的旧版 env 兜底——real
模式是全新功能，没有需要兼容的历史环境变量）。

**修了一处会被点开详情页才发现的回归**：`dynamicUsersClient` 只声明了
`ListUsers` 时，`internal/platform/platformusers.NewService` 的构造期类型
断言（`c.(UserDetailReader)` 等）会全部失败，fake 模式下原本能用的“用户
详情/日用量/Key 元数据”v2 功能会静默变成 501。已让 `dynamicUsersClient`
转发 `GetUser`/`DailyUsage`/`ListKeyMetadata`/`V2Capabilities`/
`V2KeyCapabilities` 到 fake 端；解析出的生效模式是 real 时这三个方法
统一 `not_supported`（本任务明确不实装 real 端 v2，保持既有行为，不加新的
production 闸——那道闸只针对本片新加的 `ListUsers` 纪律）。

### 部署

服务器 `.env` 把 `XM_PLATFORM_USERS_MODE` 改成 `real` 即可；`XM_SECRET_ROOT`
沿用现有值（platform-api 与 worker 现在都从同一目录读值）。真正生效哪个
平台走 real、连哪个端点、用哪个凭据引用，由运营在后台的“连接器配置”页面
（`connector.config.set@1`）按平台/环境各填一行决定，不需要额外的进程级
环境变量。

## files_changed

- `connectors/platformusers/client.go`（RealClient 实装：HTTP 传输、鉴权、
  错误分类、ListUsers 主流程）
- `connectors/platformusers/upstream.go`（新增：两个上游的路由/信封/字段
  形状，`fetchSub2APIUsers`/`fetchNewAPIUsers`）
- `connectors/platformusers/amount.go`（新增：定点金额解析 + quota 换算）
- `connectors/platformusers/listing.go`（新增：真实客户端的过滤/分页/合计，
  与 fake.go 同语义但不共享实现，避免牵动已被测试锁定的 fake.go）
- `connectors/platformusers/contract.go`（`ParseUserStatus` 补 NewAPI 的
  `"2"`；包顶注释去掉过期的 DRAFT 措辞）
- `connectors/platformusers/contract_test.go`（移除两条会变成真实网络 I/O
  的旧骨架测试，指向 realclient_test.go）
- `connectors/platformusers/realclient_test.go`（新增：httptest 假上游 +
  契约套件 + 形状映射 + 错误分类 + allowlist/重定向/截断）
- `cmd/platform-api/platformusers.go`（`buildPlatformUsers` 改造、
  `dynamicUsersClient`、`platformUsersSecretProvider`）
- `cmd/platform-api/platformusers_test.go`（新增：dynamicUsersClient 的
  resolve 决策表、production+fake 闸、v2 转发回归）
- `cmd/platform-api/main.go`（装配 `platformUsersDeps`）
- `cmd/platform-api/config.go`（`SecretRoot` 字段注释更新：不再是只写）

## tests_run

- `go build ./...`：PASS。
- `go vet ./...`：PASS（无输出）。
- `gofmt -l .`：本片改动的文件全部干净；仅 `internal/platform/httpapi/finance_test.go`
  被列出——这是**改动前就存在、本片未碰**的既有问题（XM-CRED0-backend 的
  handoff 里已经记录过同一条）。
- `go test ./...`：全仓 PASS（含 `connectors/platformusers`、
  `cmd/platform-api`、`internal/platform/platformusers`、
  `internal/platform/jobs`、`internal/platform/httpapi` 等相关包）。
- `bash scripts/check-governance.sh`：PASS（exit 0）。
- `gitleaks detect --no-git`：对 `connectors/platformusers`、
  `cmd/platform-api` 两个目录扫描，均 `no leaks found`（测试固件里的凭据
  引用/token 用的是 `secret://platformusers-test/...`、
  `test-only.invalid-token` 这类明显占位符）。

## not_run / risks

- **未对真实实例验证过**：本片的字段形状是对着 `K:/sub2api-src`、
  `K:/newapi-src` 的**源码**逐字段核对出来的，不是对着一次真实观测——与
  `connectors/sub2api`、`connectors/newapi` 两个已上线连接器上线时的同一条
  免责声明。真实凭据到位后第一次读取要核对的重点：
  - Sub2API 的 admin 组是否也像 `connectors/sub2api` 遇到的那样挂了
    `AdminComplianceGuard`（423，需要先跑一次
    `POST /api/v1/admin/compliance/accept`）——本片的 `classifyStatus`
    已经把 423 归为 `auth`，但没有专门测试这条（那是 sub2api metering
    连接器已踩过的坑，`platformusers` 走的是同一批 admin 路由，大概率也会
    撞上）；
  - NewAPI 的 `quota_per_unit` 是否真的是 500000 这类“正常”值；
  - 两边 `status` 取值集合是否真的只有已核对的那几个（万一某个实例自定义
    了额外状态码，会落进 `StatusUnknown`，这是安全的默认，但值得在
    RUNBOOK 验证清单里提一句）。
- **未做端到端的生产环境验证**（服务器上实际把某个 `core.connector_config`
  行切成 real、观察 `/api/v1/platform/users` 返回真实数据）——本片只有单元
  与 httptest 集成测试覆盖。
- `dynamicUsersClient` 构造 `RealClient` 时不注入自定义 `http.RoundTripper`
  （生产不需要信任自签证书）；因此
  `TestDynamicUsersClientRealRowEndToEnd`（cmd/platform-api）只验证
  `resolve()` 解出的配置字段与数据库行一致，**没有**验证 dynamicUsersClient
  经真实 TLS 握手打到假上游拿到数据——那条端到端的 HTTP 映射由
  `connectors/platformusers/realclient_test.go` 单独覆盖（它能注入
  `WithBaseTransport`）。两组测试合起来覆盖了完整链路，但没有一个单一
  测试从 `dynamicUsersClient` 一路打到 httptest 服务器。
- Sub2API 侧没有解析每用户 API Key（`TokenPrefix` 恒为空串）：列表端点
  不预置 `api_keys`，需要额外一次 `GET /:id/api-keys` 才能拿到，本片判断
  “逐用户查一次”的成本对列表读取不划算，保持契约允许的“上游没给”状态。
  如果运营需要这一列，需要一个专门的 follow-up。
- NewAPI 的 `used_quota`（全时段累计已耗用额度）**没有**映射进
  `PeriodConsumed`：它是全量累计值，不随 `Period` 筛选变化，语义上不等于
  “所选区间的消费额”，映射过去会是一个悄悄错误的数字，所以保持
  `PeriodConsumed.Known=false`（详见 upstream.go 顶部注释）。v2 若要做出
  真正的按区间消费，需要另一条上游能力（大概率是逐日 usage 表），不在本
  片范围。
- 没有实现客户端侧重试/退避：与 `connectors/sub2api`、`connectors/newapi`
  两个已上线真实客户端同一现状（它们也没有——重试是这两个连接器所在的
  River 后台任务层的职责，而 `platformusers.ListUsers` 是同步的 HTTP 请求
  路径，失败直接把错误翻译给前端，由人手动重试/刷新页面）。

## follow_ups

- 真实凭据到位后，按上面“not_run/risks”第一条逐项核对，尤其是 423 合规门
  与 quota_per_unit 的真实取值，写进
  `docs/inventory/managed-systems.yaml`（若不存在对应条目就新建）。
- 如果运营需要“今日充值/今日消费”这类逐用户区间流水，需要先确认两个上游
  有没有可用的按用户+按日 API（大概率没有，需要另立 evidence 文档说明缺
  口），再决定 v2 契约怎么补——这是原型 warnbar 本来就留白的部分，本片
  没有改变这个结论，只是把 v1 能给的那部分（余额、状态、最后活跃）接通了。
- 值得考虑给 `RealConfig` 加一个可选 `Timeout` 字段（目前固定
  `defaultRequestTimeout = 15s`，与 sub2api/newapi 一致）；如果两个上游的
  用户量差异很大，固定超时可能不够用，但目前没有证据表明需要调整，先不
  加这个配置面。
