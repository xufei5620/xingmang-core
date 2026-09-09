# XM-REQLOG-TOKENMAP-V2 · reqlog tokenmap 导出补选上游用户 ID（CR-0008）

## status

READY（详情见下方 not_run / risks——验收标准第 1 条需要连生产服务器的会话
验证，本次开发机上跑不了，见 not_run）

## branch / commit / base

- branch: `ai/claude/XM-REQLOG-TOKENMAP-V2`
- base: `f1a6de3`（`release/v0.1-launch` 当前 tip，2026-09-03）
- worktree: `K:/星芒统一控制平台/acceptance/wt-cr0008-tokenmapv2`
- implementation commit: `6eba823`（代码+测试）；本文档与 CR-0008 状态更新
  在后续 docs 提交

## scope

依据 `docs/change-requests/CR-0008-reqlog-tokenmap-upstream-user-id.md`
的"变更范围"（1～5 条）与 `docs/adr/ADR-020-请求日志记录器的上游库只读
接入.md`（2026-09-03 被接受）的"决策·二"机械防护要求，实现：

1. **导出 SQL 各加一列**（`cmd/reqlog-recorder/tokenmap.go`）：两条查询
   在既有 `JOIN` 上各多 `SELECT` 一列 `u.id`，逐字符对齐团队交接指示给出
   的 SQL 文本，不改 `JOIN`/`WHERE`、不新增语句、不新增连接目标。SQL 抽成
   命名常量 `newapiTokenMapQuery`/`sub2apiTokenMapQuery`，供机械防护测试
   做静态字符串断言。
2. **`parsePsqlTSV` 改解析三列**（前缀、标识、上游 ID），同时填两份目标：
   v1（`map[string]string`，格式与改动前逐字节相同）与 v2
   （`map[string]tokenMapV2Entry`）。上游 ID 列缺失时 v1 不受影响，v2 对应
   条目 `UserID` 为空字符串（"映射不到"路径的输入，不是解析错误）。
3. **新写并行文件 `tokenmap.v2.json`**（`writeTokenMapV2`）：与
   `tokenmap.json` 同一次刷新写出，`{schema_version:2, entries:{prefix:
   {username, source, user_id}}}`；同一套可配置权限位/属组
   （`TokenMapV2Path` 新增 flag `--tokenmap-v2-path` / 环境变量
   `XM_REQLOG_RECORDER_TOKENMAP_V2`，默认 `/root/reqlog/tokenmap.v2.json`，
   留空则不写）；v1 文件字节级不变；v2 失败只记警告，不影响 v1 已经成功
   写盘这件事。**未做临时文件+rename 的原子写**——v1 文件今天本来就不是
   原子写（直接 `os.WriteFile`），v2 保持与 v1 同样的写法，不引入 v1 没有
   的新写盘策略（团队交接指示"if the current file is written that way"
   的字面结论：不是）。
4. **消费侧（`connectors/reqlog`）**：
   - `contract.go`：`RequestLogSummary` 新增 `User *platformusers.UserRef`，
     `ListFilter` 新增 `User *platformusers.UserRef`（要求 `Platform` 与
     `Source` 相等）；`Username`/`TokenPrefix` 不删除、行为不变。
   - `file_client.go`：`FileConfig` 新增可选字段 `TokenMapV2Path`；新增
     `loadTokenMapV2`/`resolveUserRef`（文件缺失/解析失败/schema_version
     不是 2/前缀未登记/`user_id` 为空/`source` 与记录不一致/`UserRef.
     Validate()` 不过，全部统一判"映射不到"，`User` 为 `nil`——与
     `resolveUsername` 同一条容错纪律，不是新错误类别）；
     `summaryFromRecord` 填充 `User`，`matchedSummaries`/`RequestContent`
     两条读路径都接了 v2 映射。
   - `fake.go`：`ValidateFilter` 新增 `User.Platform` 与 `Source` 一致性
     校验；`matchesFilter` 新增按 `User`（Platform+ID）精确过滤。Fake 本身
     从不产出非 `nil` 的 `User`（没有 v2 样本数据的概念），这是有意的——
     "非 nil"这一态由文件后端的专属测试覆盖（见 tests_run）。
5. **evidence-capture**（`cmd/evidence-capture/reqlog.go`）：新增可选
   `--tokenmap-v2` 标志；提供时按 CR-0008 的 v2 schema 解析，计算
   `carries_source_user_id`（v2 存在且 `schema_version=2` 且至少一条
   `user_id` 非空才为 `true`）与相应 `finding` 文案；未提供时逐字节保持
   CR-0008 之前的行为（`false` + 原 finding 文案）。`README.md` 模板
   （`readme.go`）同步改成按实际读到的 v2 数据描述"头条发现"，不再对已经
   解决的问题保留过时文案。显式提供但读/解析失败时**报错退出**
   （`exitFailed`），不是静默退回"当作没提供"——运营明确要求了这份文件，
   给不出就该让他知道。
6. **runbook**（`docs/runbooks/REQLOG-RECORDER.md`）：新增
   "CR-0008：tokenmap.v2.json"一节，记录 v2 文件的默认路径、
   `cmd/platform-api` 侧的接入现状（已完成部分见下）、**`deploy/compose/
   server-prod.yaml` 尚未新增对应挂载**（明确留给后续切片，见
   deviations）、以及验收标准第 1 条的服务器验证命令（jq 脚本，只读，本次
   未执行）。同时按 ADR-020 决策·一·2/3 要求，在"已知限制"一节补充了这条
   通道"连接主体现状"（不是专用只读角色）与"凭据落地现状"（宿主机 root
   身份 + Docker socket 访问权，不经 `SecretProvider`/`CredentialRef`）的
   如实说明。

## deviations（与 CR-0008/团队交接指示的偏离，随手记）

- **`cmd/platform-api/reqlog.go` 的改动不在 CR-0008"目标资源"的显式列表
  里**（该列表只列了 `tokenmap.go`/`file_client.go`/`contract.go`/
  `evidence-capture/reqlog.go`/runbook 五处）。我加了一条对称的最小改动
  （新环境变量 `XM_REQLOG_TOKENMAP_V2` → `FileConfig.TokenMapV2Path`，7 行
  + 测试），理由：没有这一步，`FileConfig.TokenMapV2Path` 在真实部署里
  永远拿不到值，"populate the upstream user id into the UserRef"（团队
  交接指示原话）在生产环境无法达成，只能停留在契约/单测层面。这个改动
  完全向后兼容（默认空串，行为与改动前逐字节相同），风险很低，我判断
  值得做，但明确标出它超出了 CR 文本的字面枚举范围，供审阅时确认是否
  接受。
- **`deploy/compose/server-prod.yaml` / `.env.example` 没有改**：这一步
  比 platform-api 的改动更进一步（改生产部署配置、新增只读挂载），
  CR-0008"变更范围"第 5 条明确把部署步骤限定在"记录代理二进制的重建/
  替换/属组核对"，没有提 compose。判断这一步留给后续切片更稳妥（等
  `tokenmap.v2.json` 真的在服务器上产出、验证过 99% 那条之后再接容器
  挂载，避免"配置已经生效但文件还没产出"这种中间态）。已在 runbook 里
  写清楚接入步骤（新增哪个 env var、哪条 volume 挂载），供后续切片直接
  抄。
- **ADR-020 决策·二要求的"表/列封闭列举"守卫，实现成了更严格的"任意
  标识符封闭列举"**（不止表/列，连 SQL 关键字/函数名/别名/字符串字面量
  片段都要显式在允许列表里）——团队交接指示第 4 条原话是"the table/column
  allowlist is closed"，比 ADR-020 原文"只引用...列出的表名"更宽，我按
  更严格的交接指示实现，多出的严格度不改变判据的方向（新增表/列一样会
  被挡住），只是连带挡住了"顺手拼进一个新函数调用"这类更细的改动。

## files_changed

修改：

- `cmd/reqlog-recorder/tokenmap.go`：两条 SQL 加列，抽成命名常量；
  `parsePsqlTSV` 改三列解析、同时填 v1/v2；新增 `writeTokenMapV2`、
  `tokenMapV2File`/`tokenMapV2Entry` 类型。
- `cmd/reqlog-recorder/tokenmap_test.go`：更新 `parsePsqlTSV` 相关测试；
  新增 ADR-020 机械防护测试（无写关键字、单语句无事务、标识符封闭列举）；
  新增 v2 相关解析测试（缺列、trim 空白）。
- `cmd/reqlog-recorder/config.go`：新增 `TokenMapV2Path` 字段、
  `DefaultTokenMapV2Path` 常量、`--tokenmap-v2-path` flag。
- `cmd/reqlog-recorder/config_test.go`：新增默认值与环境变量覆盖断言。
- `connectors/reqlog/contract.go`：`RequestLogSummary.User`、
  `ListFilter.User` 新增字段；导入 `connectors/platformusers`。
- `connectors/reqlog/fake.go`：`ValidateFilter`/`matchesFilter` 支持
  `User` 过滤与一致性校验。
- `connectors/reqlog/file_client.go`：`FileConfig.TokenMapV2Path`；
  `loadTokenMapV2`/`resolveUserRef`；`summaryFromRecord` 签名新增
  `tokenMapV2` 参数，两条调用点同步。
- `connectors/reqlog/file_client_test.go`：新增
  `TestUserRefPopulatedFromTokenMapV2`、`TestUserRefFilterNarrowsResults`。
- `connectors/reqlog/file_fixture_test.go`：新增
  `tokenMapV2Fixture`/`writeTokenMapV2Fixture`。
- `connectors/reqlog/contracttest/suite.go`：新增共享子测试
  `testUserRefNilWhenUnassociated`（"用户身份未关联时为nil"），RunSuite
  从 21 项增到 22 项。
- `cmd/evidence-capture/reqlog.go`：新增 `--tokenmap-v2` flag、
  `tokenMapV2File`/`tokenMapV2FileEntry`/`loadLocalTokenMapV2`/
  `tokenMapV2Evidence`/`computeTokenMapV2Evidence`/
  `reqlogTokenMapShapeFinding`；`tokenMapShape` 输出改用计算值；dry-run
  文案同步。
- `cmd/evidence-capture/reqlog_test.go`：新增 v2 相关单元测试与三条
  端到端测试（carries=true、carries=false-but-present、显式提供但读取
  失败应 `exitFailed`）。
- `cmd/evidence-capture/readme.go`：`renderReqlogReadme` 签名新增 v2
  参数，"头条发现"一节按实际 v2 数据条件渲染。
- `cmd/platform-api/reqlog.go`：`reqlogConfig.TokenMapV2Path`；
  `reqlogConfigFromEnv` 读 `XM_REQLOG_TOKENMAP_V2`；
  `newReqlogFileClient` 透传给 `FileConfig`（见 deviations）。
- `cmd/platform-api/reqlog_file_test.go`：新增默认值/环境变量覆盖断言。
- `docs/runbooks/REQLOG-RECORDER.md`：新增"CR-0008：tokenmap.v2.json"
  一节；第 6 步补一句指向该节的提示；"已知限制"第一条补 ADR-020 决策·一
  ·2/3 要求的连接主体/凭据落地现状说明。
- `docs/change-requests/CR-0008-reqlog-tokenmap-upstream-user-id.md`：
  状态改为 implemented-pending-verification，"执行记录"补两条。

新增：

- `cmd/evidence-capture/testdata/reqlog/tokenmap.v2.sample.json`（占位
  样本，`schema_version=2`，两条条目各带一个占位 `user_id`）。
- `cmd/evidence-capture/testdata/reqlog/tokenmap.v2.sample.no-user-id.json`
  （占位样本，`user_id` 全空，用于验证"存在但给不出 ID"不该被误判成
  `carries_source_user_id=true`）。
- `docs/handoffs/slices/XM-REQLOG-TOKENMAP-V2.md`（本文件）。

## tests_run

全部在 `K:/星芒统一控制平台/acceptance/wt-cr0008-tokenmapv2` 跑，
`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY
-u all_proxy -u NO_PROXY -u no_proxy`（本机代理坑，见项目记忆
`windows-toolchain-quirks`）。

- `go build ./...`：exit 0。
- `go vet ./...`：exit 0。
- gofmt：本次改动/新建的全部 `.go` 文件（15 个）用
  `"$(go env GOROOT)/bin/gofmt" -l <文件...>` 逐个核对，三处（
  `connectors/reqlog/contract.go`、`connectors/reqlog/file_fixture_test.go`、
  `cmd/evidence-capture/reqlog_test.go`）因结构体字段对齐问题被判"未格式化"
  （真实问题，不是 CRLF 假警报——已用 `gofmt -d` 核对过 diff 内容），
  `gofmt -w` 精确到这三个文件后复核干净；其余 12 个文件本来就干净。
- `go test -p 1 -count=1 ./...`（全量）：
  - 第一次：全绿（含 `cmd/evidence-capture` ok, 0.521s）。
  - 第二次：`cmd/evidence-capture` 的
    `TestRunCaptureSub2APIEndToEndWritesRedactedEvidence` 失败
    （`dial tcp 127.0.0.1:13663: ... connectex ...`，httptest TLS 服务器
    连接失败）——单独隔离重跑（`-run
    TestRunCaptureSub2APIEndToEndWritesRedactedEvidence`）**通过**。这条
    测试不属于本次改动触及的代码（Sub2API 证据采集，与 tokenmap/reqlog
    无关），两次运行结果不一致 + 隔离重跑通过，判定为本机代理 TUN 抖动
    造成的既有 flake（项目记忆里已有多条同类记录：
    `httptest.NewTLSServer` 类用例对代理环境敏感），不是本次改动引入的
    回归。
- `connectors/reqlog` 契约套件专项验证：`TestFakeSatisfiesContract` 与
  `TestFileClientSatisfiesContract` 各 22 项子测试全部通过（21 项既有 +
  1 项新增"用户身份未关联时为nil"），未回归。
- `TestUserRefPopulatedFromTokenMapV2`（v2 关联生效、未登记前缀仍为
  nil）、`TestUserRefFilterNarrowsResults`（按 `User` 精确过滤、不存在的
  `User` 返回空页而非报错、`User.Platform` 与 `Source` 不一致被拒绝）：
  通过。
- `cmd/reqlog-recorder` 专项：`TestTokenMapQueriesContainNoWriteKeywords`、
  `TestTokenMapQueriesAreSingleStatementsNoTransaction`、
  `TestTokenMapQueriesOnlyReferenceAllowlistedIdentifiers`（ADR-020 决策·二
  的三条机械防护）：通过。
- `cmd/evidence-capture` 专项：`TestRunReqlogEndToEndWithTokenMapV2CarriesUserID`
  （`carries_source_user_id=true` 且不泄漏原始 user_id/身份）、
  `TestRunReqlogEndToEndWithTokenMapV2NoUserID`（v2 存在但全空 →
  仍为 `false`）、`TestRunReqlogTokenMapV2ReadFailureIsFailed`
  （显式给了 `--tokenmap-v2` 但文件不存在 → `exitFailed`）：通过。
- `gitleaks git --no-banner --log-opts="release/v0.1-launch..HEAD" .`：
  见下方补记（commit 后单独跑，结果回填于此/或 CR-0008 状态段落）。

## not_run

- **CR-0008 验收标准第 1 条**（服务器上产出一份 `tokenmap.v2.json` 后，
  抽样核对：前缀命中的记录中至少 99% 能解出非空 `User`）——本次实现在
  开发机上跑，没有到生产服务器/生产 NewAPI、Sub2API 数据库的连接，无法
  执行。**服务器验证命令**（只读，不改任何数据）已写进
  `docs/runbooks/REQLOG-RECORDER.md`"CR-0008：tokenmap.v2.json"一节的
  "验收标准第 1 条的服务器验证命令"小节，摘录如下，供能连服务器的会话
  直接执行：
  ```bash
  # 前提：记录代理已按该 runbook 第 1～5 步升级、至少刷新过一轮
  sudo test -s /root/reqlog/tokenmap.v2.json && \
    sudo jq -e '.schema_version == 2 and (.entries | type == "object")' /root/reqlog/tokenmap.v2.json
  sudo jq '
    (.entries | length) as $total |
    ([.entries[] | select(.user_id != null and .user_id != "")] | length) as $with_id |
    {total: $total, with_id: $with_id,
     pct: (if $total == 0 then 0 else ($with_id * 100.0 / $total) end)}
  ' /root/reqlog/tokenmap.v2.json
  ```
  `pct` 应 `>= 99`。
- 未在真实 NewAPI/Sub2API 数据库上跑过两条新 SQL——两条查询语句只加了
  一列已经 `JOIN` 到的 `u.id`，风险面很小，但"跑起来真的能拿到值"这件事
  没有服务器无法核实（`u.id` 理论上不可能为 NULL，因为是等值 JOIN 的
  关联键，但这是推理不是观测）。
- 未验证 `deploy/reqlog/reqlog-recorder.service` 重启后新二进制确实产出
  了 `tokenmap.v2.json`（同上，需要服务器）。
- Windows 开发机上没有跑 `TestWriteOnePermissionBits` 之外的任何权限相关
  用例（既有跳过，见 `storage_test.go` 里的既有 skip 逻辑，不是本次新增
  的空白）。

## risks

- **v2 文件写失败被吞掉不报错**（`writeTokenMapV2` 失败只 `logger.Error`，
  不影响 v1 已经成功写盘）：这是有意设计（v1 是今天唯一被消费的产出，
  不该因为 v2 失败被拖累），但意味着"v2 一直没产出"这件事只能靠看日志
  发现，不会有任何告警/健康检查信号。建议后续切片补一条对
  `reqlog_recorder_tokenmap_v2_write_failed`/
  `reqlog_recorder_tokenmap_v2_marshal_failed` 的日志监控规则。
- **`resolveUserRef` 对 `entry.Source` 与记录 `Source` 不一致的处理是
  "静默判映射不到"，不是记警告**——与 `resolveUsername` 对后缀不匹配的
  处理同一条纪律（该函数同样不记警告），但如果未来这种不一致大量出现
  （比如 v2 文件损坏或被错误的脚本改写），不会有任何可观测信号，只会
  表现为"User 字段莫名其妙全是 nil"。
- **`deploy/compose/server-prod.yaml` 未更新**（见 deviations）：在这一步
  补上之前，即便记录代理已经在生产写 `tokenmap.v2.json`，容器化
  `platform-api` 也读不到——功能上安全（`User` 恒为 nil，是既有容错
  路径），但如果有人只看了 CR-0008 状态是
  "implemented-pending-verification" 就以为整条链路已经生产可用，会
  产生误判。已在 runbook 里显式标注这一状态。

## follow_ups

1. **补 `deploy/compose/server-prod.yaml`/`.env.example` 的
   `XM_REQLOG_TOKENMAP_V2`/`XM_REQLOG_HOST_TOKENMAP_V2` 只读挂载**（见
   runbook 与 deviations），建议在记录代理已经在生产产出
   `tokenmap.v2.json` 并核对过验收标准第 1 条之后再做，避免"配置已生效
   但源文件还没产出"的中间态。
2. **在能连服务器的会话里跑 not_run 一节列出的验收标准第 1 条验证**，
   并把结果回写进 `docs/approvals/` 或 `docs/evidence/` 下的证据目录
   （按 `REQLOG_USERREF_APPROVAL`/`USERS-REAL-APPROVAL.md` 同一套纪律）。
3. **ADR-020"决策·三"退出路径**：待 `connectors/platformusers` 的 real
   reader 在两个上游都能稳定填出 `TokenPrefix`（今天至少一侧仍留空）时，
   评估把这条 `docker exec` 直连例外退役，改走 Connector 化的身份读取。
   不在本 CR/本切片范围内，只是把 ADR-020 已经写明的触发条件在此处
   重申一遍，供后续排期参考。
4. **`writeTokenMapV2` 失败的可观测性**（见 risks 第一条）：建议补告警
   规则或健康检查项。
