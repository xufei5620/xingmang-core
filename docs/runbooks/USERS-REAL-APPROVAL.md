# Runbook：用户详情 real 接入的证据采集与审批（XM-USERS-REAL-EVIDENCE0）

> 目标：让 `docs/approvals/SUB2_REAL_APPROVAL.md`、`NEWAPI_REAL_APPROVAL.md`、
> `REQLOG_USERREF_APPROVAL.md` 三份审批单，除了"接触真实凭据/真实数据"这一步
> 与"审阅人签字"这一步之外，全部变成不需要工程师介入的固定操作。
>
> 背景：`docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md`
> §0/§10 冻结的三个独立审批事件，对应
> `docs/superpowers/plans/2026-08-28-platform-user-read-v2.md` 的 Task 4
> （Sub2API real GetUser）、Task 5（NewAPI real GetUser）、Task 8（reqlog 稳定
> UserRef）。本 runbook 只覆盖"怎么产出与核验证据、审批怎么生效"，**不**实现
> 这三个 Task 本身。

## 0. 一图流程

1. 验收线（有权访问服务器与已登记凭据的人）在服务器上构建
   `cmd/evidence-capture` 并运行采集命令（第 1、2 节）。
2. 命令在本地写出一份带 sha256 校验的脱敏证据目录，不上传、不联网发送给任何
   第三方。
3. 验收线把证据目录提交进本仓库（`docs/evidence/users-real/...` 本身随分支
   一起提交；是否连同真实脱敏样本一起提交，还是只提交 README + 哈希、样本单独
   走内部审阅渠道，由产品与安全按公司数据处置政策决定——本 runbook 不替这个
   决定背书）。
4. 审阅人（产品 + 安全）按第 3 节核验哈希与内容，在对应的
   `docs/approvals/*_APPROVAL.md` 里勾选 checklist、填写决定、签字，提交。
5. 之后（不在本片范围内）：实现 Task 4/5/8 的工程师在其 PR/Handoff 里引用
   已签字的审批文件与证据路径/哈希；验收线在合入前核对一致——见第 4 节，
   这是今天实际存在的"门禁"，不是一个运行期开关。

## 1. 采集在哪里跑，凭据在哪里

### 1.1 Sub2API / NewAPI（`capture` 子命令）

`cmd/evidence-capture capture` 只发起只读 GET 请求，读的是**将来 Task 4/5 real
reader 也会读的同一个上游只读接入点**——因此复用的是与
`docs/runbooks/SWITCH-SUB2API-REAL.md` / `SWITCH-NEWAPI-REAL.md`
**同一套**端点、白名单、CredentialRef 概念，不是另立一套：

| 参数 | 含义 | 参考 |
|---|---|---|
| `--endpoint` | 上游实例根地址，必须 `https://` | 同 `XM_SUB2API_ENDPOINT`/`XM_NEWAPI_ENDPOINT` |
| `--allowlist` | 允许连接的主机（缺省取 `--endpoint` 的主机名） | 同 `XM_SUB2API_TARGET_ALLOWLIST` |
| `--credential-ref` | `secret://<scope>/<name>` 引用，**不是明文 token** | 同 `XM_SUB2API_CREDENTIAL_REF` |
| `--secret-root` | 文件型凭据库根目录，缺省 `$XM_SECRET_ROOT` 或 `/run/xm/secrets` | 同 platform-worker 的 `connectorSecretsChain` |

**凭据本身在哪**：由运营/验收线通过平台后台把凭据写入 `XM_SECRET_ROOT` 对应的
共享卷（`<root>/<scope>/<name>`，见 `cmd/platform-worker/secret_chain.go` 的
装配注释）——这与已经在用的 v1 ListUsers real 接入是**同一份注册流程**，如果
对应平台已经按 `SWITCH-SUB2API-REAL.md`/`SWITCH-NEWAPI-REAL.md` 配过 real
凭据，直接复用同一个 `--credential-ref`；如果还没配过，先照那两份 runbook
把凭据登记到位（本工具不提供登记凭据的功能，只读）。

`--credential-ref` **绝不能是命令行上的明文 token**——`cmd/evidence-capture`
的 flag 定义里没有"直接传 token"这个选项，只有引用。

Sub2API 额外前提：登记给平台的那个 admin 账号，必须已经在 Sub2API 自己的后台
完成一次合规确认（等价于 `POST /api/v1/admin/compliance/accept`，见
`SWITCH-SUB2API-REAL.md`"前置"一节）。**这一步必须由人在 Sub2API 后台亲自
点，`cmd/evidence-capture` 永远不会替你发这个 POST**——没做这一步时，工具会
收到 HTTP 423 并在证据 `README.md` 里明确写"BLOCKED: AdminComplianceGuard"，
不会假装采集成功。

### 1.2 reqlog（`reqlog` 子命令）

`cmd/evidence-capture reqlog` **不连接任何服务器**，只读本地文件——需要先把
两份文件从记录代理所在的服务器复制一份到运行本工具的机器：

| 文件 | 记录代理上的位置（见 `docs/runbooks/REQLOG-RECORDER.md`） |
|---|---|
| 索引数据目录 | `/root/reqlog/data`（按 `YYYYMMDD` 分子目录，每个子目录一个 `index.jsonl`） |
| token 映射 | `/root/reqlog/tokenmap.json` |

复制方式不限（`scp`/`rsync`/其他），只要求：复制下来的是**只读副本**，本工具
不会、也不需要写回原始位置。

## 2. 采集命令

先构建二进制（与 `REQLOG-RECORDER.md` 第 1 步同一套工具链要求：Go 1.27）：

```bash
cd /path/to/xingmang-platform
go build -trimpath -o evidence-capture ./cmd/evidence-capture
```

### 2.1 干跑（零配置，随时可跑，确认这个工具到底会发出什么请求）

```bash
./evidence-capture capture --platform sub2api --dry-run
./evidence-capture capture --platform newapi --dry-run
./evidence-capture reqlog --dry-run
```

不需要凭据、不需要 `--data-dir`，不发起任何网络/文件 I/O，只打印固定的请求
计划——这份计划是编译进二进制的常量（`cmd/evidence-capture/plan.go` 的
`planFor`），不是运行时可配置的，审阅人可以直接读源码确认这份计划与打印出来
的一致。

### 2.2 正式采集

```bash
./evidence-capture capture \
  --platform sub2api \
  --endpoint https://<你的 Sub2API 实例> \
  --allowlist <实例主机名> \
  --credential-ref secret://sub2api-prod/read-token \
  --out docs/evidence/users-real

./evidence-capture capture \
  --platform newapi \
  --endpoint https://<你的 NewAPI 实例> \
  --allowlist <实例主机名> \
  --credential-ref secret://newapi-prod/read-token \
  --out docs/evidence/users-real

./evidence-capture reqlog \
  --data-dir /path/to/local/copy/of/reqlog/data \
  --tokenmap /path/to/local/copy/of/reqlog/tokenmap.json \
  --out docs/evidence/users-real/reqlog
```

每次运行会在 `<--out>/<UTC 时间戳>/` 下写一份完整证据目录（脱敏样本、
`version.json`/`retention_and_association.json`、`SHA256SUMS`、`README.md`），
不覆盖已有目录（重复运行会得到新的时间戳子目录）。`--sample-size`（capture
默认 5，硬上限 20）/`--retention-days`（reqlog 默认与
`connectors/reqlog.RetentionDays` 一致）等参数的作用与边界见对应
`-h` 输出或源码注释；不需要为了"多拿点数据"调大 `--sample-size`——这个工具的
定位是形状证据，不是导出。

## 3. 审阅人如何核验

1. 打开证据目录的 `README.md`，通读——它会说明这次采集请求了什么、上游返回
   了什么状态、哪些字段被保留/丢弃、脱敏规则是什么、这份证据该喂给哪个
   `docs/approvals/*.md` 与哪个 `docs/evidence/EV-*.md`。
2. 核验哈希：

   ```bash
   cd docs/evidence/users-real/<platform>/<timestamp>/
   sha256sum -c SHA256SUMS
   ```

   （Windows 无 `sha256sum` 时，用
   `Get-FileHash <file> -Algorithm SHA256` 逐个比对 `SHA256SUMS` 里的值。）
3. 打开 `*.redacted.json`/`*.redacted.jsonl`，肉眼确认：邮箱形如
   `xx***@真实域名`（域名保留，本地名打码）；id/用户名是 `u_`/`name_`/`rec_`/
   `pfx_` 前缀的十六进制串（不是能认出具体是谁的原始值）；金额/quota 字段的
   数字位已被替换成 `0`（或因 JSON 不允许数字前导零而首位是 `1`）；不出现
   任何完整 token、完整 IP（IPv4 应形如 `a.b.c.x`，IPv6 应只保留前 48 位）。
4. 把上述结果填进对应审批单的"证据"表格与 checklist（三份审批单已经把每个
   要填的字段列成表格，不需要另外新起草）。
5. 决定：批准/驳回，签字，提交（审批单文件本身随这次提交一起进仓库——宪法
   第 19 条要求决定必须回写仓库才算数）。

## 4. 审批如何让门禁生效

**先说清楚今天的实际情况，再说明将来怎么接上：**

写这份 runbook 时，Task 4/5/8 描述的 real reader 代码本身**还不存在**——
`connectors/platformusers` 只有 `fake_v2.go` 实现 v2 的
`GetUser`/`DailyUsage`/`ListKeyMetadata`，没有 `sub2api_v2.go`/`newapi_v2.go`；
`connectors/reqlog` 的 `RequestLogSummary` 也还没有设计文档 §6.1 描述的
`User *UserRef` 字段（均已由 `docs/handoffs/slices/XM-USERS-V2-real-detail.md`
核实）。**因此，今天不存在任何环境变量或数据库配置项能把这三项 real 能力
"打开"——不是因为有个开关把它关掉了，是因为实现它的代码还没有被写出来。**

容易混淆的一点：`platformusers` 确实已经有一个 real/fake 切换
（`XM_PLATFORM_USERS_MODE` 环境变量缺省值 + `core.connector_config` 数据库表
按平台/环境覆盖，通过后台的 `connector.config.set@1` Action 修改，见
`docs/handoffs/slices/XM-USERS-REAL.md`）。但那个开关只控制 **v1 `ListUsers`**
（用户清单页）。`cmd/platform-api/platformusers.go` 的 `dynamicUsersClient`
明确把 `GetUser`/`DailyUsage`/`ListKeyMetadata`/`V2Capabilities`/
`V2KeyCapabilities` 这五个 v2 方法**转发到 fake 端**，不论
`XM_PLATFORM_USERS_MODE` 是 `real` 还是 `fake`（该文件原注释："本任务明确不
实装 real 端 v2，保持既有行为，不加新的 production 闸——那道闸只针对本片
新加的 `ListUsers` 纪律"）。也就是说，即使运营已经把某个平台切到 real，用户
详情页的"近 7 天消费趋势""API Key"等 v2 面板此刻依然是 Fake 数据——这正是
本片存在的原因：Task 4/5/8 需要独立、明确的审批与证据，不能被这个已有开关
顺带打开。

**将来 Task 4/5/8 实现时应当怎么接上本审批**：设计文档 §5.1 的纪律原文是
"Sub2/New real 只有在各自 UserDetailReader 与 contracttest 同片完成后才声明"
对应 capability。也就是说，这道门禁不是运行期的一个布尔值，而是**代码审查
时点**：实现 `sub2api_v2.go`/`newapi_v2.go`/reqlog UserRef 的那个 PR，必须在
提交信息或 Handoff 里引用对应 `docs/approvals/*.md` 文件与本工具产出的证据
路径/哈希；验收线（人工）在合入 `release/v0.1-launch` 前核对这些引用是否与
已签字的审批单一致，不一致就 `REJECT`（见
`docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.1 的验收信号约定）。这与
`DAILY_USAGE_APPROVAL`/`KEY_SCOPE_APPROVAL` 已经走过的路径完全一致——那两项
审批同样没有对应的运行期环境变量，是验收线在合入那个 PR 时核对批准记录是否
存在（见同一份 handoff §7.2 的记录格式，三份 `docs/approvals/*.md` 文件的
"决定"一节都照抄了这个格式）。

若未来确实需要一个运行期的 kill switch（比如上线后发现某个字段解析有问题，
需要不经重新部署就临时关掉 real v2 GetUser），那是 Task 4/5/8 实现时需要
一并设计的内容，不是本片能替它决定的——本片只交付审批能落地所需的证据工具
与文书，不实现、不预判 Task 4/5/8 的技术方案。

## 5. 安全说明（本工具自身的护栏，供审阅人验证而不是照单全收）

- **零 CLI 明文凭据**：`--credential-ref` 只接受 `secret://<scope>/<name>`
  引用；工具用 `internal/platform/secrets` 解析，明文只在拼请求头的那一瞬
  存在，不落任何文件、日志、错误信息（同 `connectors/platformusers/client.go`
  的既有纪律）。
- **固定请求计划**：capture 子命令能发出的请求，只有 `plan.go` 里硬编码的
  每平台两条（版本 + 用户列表页）；没有任何 flag 能让它请求别的路径。
- **两层只读闸**：请求集合本身固定之外，`internal/platform/connector` 的
  `ReadOnlyTransport`（与平台既有 real 连接器同一份实现）在传输层机械拒绝
  非 GET/HEAD 方法、非白名单主机、重定向。
- **已知 mock/写端点硬拒绝**：`plan.go` 的 `mustNotBeMockRoute` 独立于
  上面两层，额外拒绝已证实是 mock 或伪装成 GET 的写端点（见
  `EV-2026-08-27-sub2api-read-survey.md`/`-newapi-read-survey.md` 的"红旗"
  一节与 `connectors/newapi/upstream.go` 的 `writeDisguisedAsGetRoutes`）。
- **写盘前的最终扫描**：每份脱敏文件在落盘前都会再过一次
  `scanForbidden`（`redact.go`），独立于逐字段脱敏规则，扫描最终 JSON 文本
  里是否残留邮箱形状的字符串或 `secret`/`token`/`password` 等禁词；命中就
  拒绝写出整份证据并以非零退出码结束，而不是写出一份可能不安全的文件。
- **reqlog 子命令不读正文**：只解析 `index.jsonl`（元数据），从不打开对应
  的 `<id>.json.gz`（完整请求/响应正文）；索引行里的 `preview`/`end_note`
  两个可能带正文片段的字段被无条件丢弃，不会出现在任何输出里。
- **一次性、不可逆的伪名化**：id/用户名/token 前缀的脱敏用的是本次运行随机
  生成的 salt 做一次性哈希（`redact.go` 的 `hashUserID`/`hashName`），同一
  次运行内同一个真实值哈希结果一致（方便审阅人核对"列表页第一行"与"详情
  样本"是不是同一条），不同次运行之间不可比对；salt 本身写在证据
  `README.md` 里，**不是**凭据，只是让哈希在本次证据范围内保持一致的种子。

这些护栏都有对应的单元测试（`cmd/evidence-capture/*_test.go`），测试本身
只用录制好的合成 fixture，不连接任何真实网络。
