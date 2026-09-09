# XM-ALERT-WECOM · 告警投递新增企业微信群机器人渠道

## status

READY

## branch / commit / base

- branch: `ai/claude/XM-ALERT_WECOM`
- implementation commit: `94f9af2`（本分支 HEAD，单提交，base 之上）
- base: `release/v0.1-launch@ef0a85f`
- worktree: `K:/星芒统一控制平台/acceptance/wt-alert-wecom`

## summary

用户原话：「告警投递……之前是通知到企业微信」。本片在 `internal/platform/alerts`
既有的 Telegram / 自建 Webhook 两条投递渠道之外，新增第三条：企业微信群机器人
Webhook（`msgtype=markdown`）。

### 与既有两条渠道的关系与取舍

- **消息形状**：POST `{"msgtype":"markdown","markdown":{"content":…}}`，
  content 含环境、严重度、规则、指标键、详情（当前值落在这里）与两个
  时间戳（首次发现 / 最近发现）。只用 `> ` 引用块语法，不用加粗/颜色/标题
  级别——原因与 `FormatMessage`（Telegram 纯文本）完全一致：Title/Detail/
  RuleKey/SourceMetricKey 由上游数据决定，可能含 markdown 特殊字符，带标记
  就得转义，漏转义的后果是消息排版错乱甚至发不出去。
- **长度上限按字节**：企微 content 上限 4096 **字节**，不是字符。新增
  `truncateBytes`（与既有服务 `notify_error` 的 `truncate` 按 rune 截断
  是两个函数，互不影响，各自的调用方对长度单位的要求不同）。
- **成功/失败判定**：HTTP 200 且 `errcode=0` 才算成功；非 0 时把 errcode
  与 errmsg（脱敏后）一起放进错误，运维不用翻代码就知道上游拒收原因。
- **凭据形态是本片与前两条渠道最大的分歧点**：企微群机器人的鉴权 key 直接
  嵌在 Webhook 地址的查询参数里（`.../webhook/send?key=xxx`），**整个地址
  就是凭据**（PROJECT-CONSTITUTION 第 7 条：凭据只经 CredentialRef）。这
  与自建 `WebhookNotifier`（地址允许来自静态环境变量 `XM_ALERT_WEBHOOK_URL`）
  不同，反而更接近 Telegram（token 经 CredentialRef、由 SecretProvider 在
  发送那一瞬现场给出）——但装配链路又与 Telegram 现在还在用的纯 env
  Provider（`alertSecretsFromEnv`）不同：企微地址走的是 Sub2API/NewAPI/
  成本采集已经在用的**文件优先链**（`connectorSecretsChain`，XM-CRED0），
  这样运营才能在后台「设置→凭据」页直接粘贴地址、worker 不重启即生效。
  Telegram 尚未迁移到这条链路，本片**没有**顺手改它，避免在一个不相关的
  改动里触碰一条已经在生产上跑且有完整测试覆盖的路径；这是一处刻意的
  不一致，留在 follow_ups 里。
- **配置纪律与现有两渠道一致**：ref 拼错、或配了 ref 却没有对应
  SecretProvider（装配缺失）→ worker 拒绝启动；三个渠道的变量全部留空
  → 允许，告警照常评估落库，每轮打一条 `alert_notify_skipped` warn。
  ref 配了但地址还没在后台粘贴（文件里还没有这个值）**不是**启动错误
  ——这一点严格照抄 Telegram Bot Token 的语义：值是否解析得出要等发送
  那一刻才知道，构造阶段只校验引用形状与 Provider 是否存在。
- **投递状态复用既有机制**：`WeComNotifier` 实现与 `TelegramNotifier`/
  `WebhookNotifier` 相同的 `Notifier` 接口，`MultiNotifier`/`Reconciler`
  按渠道数量与失败列表统一处理（任一渠道成功即算 delivered）。已确认
  `web/apps/admin-web/src/pages/AlertsPage.tsx` 与相关 `api/*.ts` 不含任何
  渠道名硬编码，投递状态/错误原因的展示对新渠道无需改动。
- **URL 泄漏防护踩了一个真问题**：初版按「完整地址整串出现才算泄漏」来
  `redact`，但 `TestWeComNotifierNeverLeaksWebhookURL` 的「上游把请求 URL
  回显进错误描述」用例（模拟网关只回显 `r.URL.RequestURI()`，不含
  scheme/host）当场就红了——因为回显文本里根本不包含完整地址整串，按整串
  匹配漏判。修法是把解析出的 `RawQuery`（企微鉴权 key 所在处）也单独作为
  一个敏感片段传给 `redact`，两个片段任一出现都要被抹掉。测试先红后绿，
  过程见下方 tests_run。

## files_changed

- `internal/platform/alerts/notify.go`：新增 `WeComNotifier`/`WeComOptions`/
  `NewWeComNotifier`/`FormatWeComMarkdown`/`truncateBytes`/相关 payload 类型；
  三处 `redact` 调用改为传入 `[endpoint, RawQuery]` 两个敏感片段。
- `internal/platform/alerts/notify_test.go`：新增 12 个 WeCom 测试
  （正常路径、5 类泄漏场景、errcode 报告、超时、每次现解析、配置不全、
  凭据缺失、非 https 拒绝、markdown 截断、字段完整性）。
- `internal/platform/alerts/reconcile.go`：`alert_notify_skipped` 的 hint
  文案补上 `XM_ALERT_WECOM_WEBHOOK_REF`。
- `internal/platform/jobs/alert_evaluate.go`：`AlertNotifierConfig` 新增
  `WeComWebhookRef`/`WeComSecrets`；`newAlertNotifier` 新增装配分支与更新
  后的文档注释。
- `internal/platform/jobs/alert_evaluate_test.go`：扩展三个既有测试
  （失败闭合矩阵、全空放行、三渠道齐配）+ 新增单独覆盖企微渠道的用例。
- `internal/platform/jobs/client.go`：`jobs.Config` 新增
  `AlertWeComWebhookRef`/`AlertWeComSecrets`；worker 装配处透传；
  `alert_notifier_not_configured` 的 hint 文案同步。
- `cmd/platform-worker/alerts.go`：新增 `alertWeComSecretsFromEnv`
  （复用既有 `connectorSecretsChain`，文件优先 + env 兜底）。
- `cmd/platform-worker/config.go`：解析 `XM_ALERT_WECOM_WEBHOOK_REF`。
- `cmd/platform-worker/config_test.go`：新增
  `TestConfigFromEnvReadsAlertWeComWebhookRef`、`TestAlertWeComSecretsFromEnv`
  （照 `TestSub2APISecretsFromEnv` 的模式：文件优先、env 兜底、文件出现后
  即压过 env、未登记引用不回退）。
- `cmd/platform-worker/main.go`：装配 `config.AlertWeComSecrets`、启动失败
  时的 `error_code`、启动结构化日志新增 `alert_wecom_configured` 字段
  （只打是否配置，不打地址本身）。
- `internal/platform/credentials/expected.go`：登记
  `secret://alerts/wecom-webhook`（platform=alerts），使其出现在
  「设置→凭据」页的预期清单里。
- `internal/platform/credentials/store_test.go`：
  `TestExpectedRefsAreValidAndStable` 的清单条数断言 5→6。
- `deploy/compose/launch.yaml`：worker 服务透传
  `XM_ALERT_WECOM_WEBHOOK_REF`/`XM_ALERT_WECOM_WEBHOOK`（均默认空）。
  `server-prod.yaml` 未改——它对告警渠道没有任何 `:?` 覆盖，沿用
  launch.yaml 的透传即可（已用 `docker compose config` 核实）。
- `deploy/compose/.env.example`：新增两个变量的注释与说明；投递渠道小节
  的「这三组」措辞改成「三个渠道」以涵盖新增的企微一项。
- `docs/modules/alerts/README.md`：投递渠道 ASCII 示意图、环境变量表、
  「配错就拒绝启动」小节、「凭据不泄漏」小节、相关文件表、四条铁律里的
  凭据条款，同步补上企业微信渠道。

## tests_run

全部在 worktree 根目录执行，Windows 上 `httptest` 相关用例必须清空代理
变量，否则会被本机 HTTP(S)_PROXY 拦截成片报错（本机既有环境坑，与本片
改动无关）：

```
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY \
  -u all_proxy -u NO_PROXY -u no_proxy go build ./...
```
→ PASS（无输出）

```
env -u HTTP_PROXY ... go vet ./...
```
→ PASS（无输出）

```
env -u HTTP_PROXY ... go test -p 1 -count=1 ./...
```
→ PASS，43 个包 `ok`，0 个 `FAIL`，exit 0。其中
`internal/platform/alerts`（含全部新增 WeCom 用例，12 个新测试全绿，
含一次先红后绿的泄漏回归——见上文 URL 泄漏防护那段）、
`internal/platform/jobs`（企微装配矩阵）、`cmd/platform-worker`
（文件优先链装配）、`internal/platform/credentials`（预期清单）均单独
复核过详细输出。集成测试（`XM_TEST_DATABASE_URL` 相关，含
`TestEndToEndNotifyFailurePersistsWithoutLeakingToken` 等）按仓库既有约定
在无测试库时跳过，未在本片新起真库验证——WeCom 走的是与 Telegram/Webhook
完全相同的 `Notifier`/`MultiNotifier`/`Reconciler` 编排路径，未新增写库
逻辑，判定风险低。

```
"$(go env GOROOT)/bin/gofmt" -d <每个改动过的 .go 文件>
```
→ 全部空 diff（PATH 上的 gofmt 是旧工具链、与 go.mod 钉的版本判定不一致，
已改用 GOROOT 下匹配 go.mod 版本的 gofmt 复核，避免假警报）。

```
env -u HTTP_PROXY ... bash scripts/check-governance.sh
```
→ PASS，exit 0（含 gitleaks；新增的测试固件全部使用
`key=test-only`/`key=test-only-file`/`key=leak-canary-…` 等明显占位值，
未触发历史踩过的 generic-api-key 误报）。

```
MSYS_NO_PATHCONV=1 DATABASE_PASSWORD=placeholder-only \
  docker compose -f deploy/compose/launch.yaml config --quiet
```
→ PASS，exit 0；并用不带 `--quiet` 的版本核对渲染结果，确认
`XM_ALERT_WECOM_WEBHOOK_REF`/`XM_ALERT_WECOM_WEBHOOK` 默认渲染为空字符串。
另确认 `server-prod.yaml` 对告警渠道无任何 `:?` 覆盖（`grep XM_ALERT` 零命中），
故不需要为本片改动该文件。

```
git diff --check
```
→ PASS（无空白错误）。

## not_run / risks

- **未连过真实企微端点**：所有测试固件都是 `httptest.NewTLSServer` 假端点，
  从未对 `qyapi.weixin.qq.com` 发过请求，也没有真实 Webhook 地址可用（宪法
  24/27 条：AI 不碰真实第三方凭据、不发生产请求）。真实连通性验证需要运营
  在服务器上把现有企微群机器人 webhook 值经后台「设置→凭据」页录入后，
  由验收线在 staging/生产触发一条真实告警观察是否送达——这一步本片有意
  留白，是给用户/验收线的动作，不是遗漏。
- **未新起真实数据库做端到端集成测试**：`internal/platform/alerts` 里
  `XM_TEST_DATABASE_URL` 门控的集成用例（含专门测「投递失败不泄漏凭据」的
  `TestEndToEndNotifyFailurePersistsWithoutLeakingToken`）按既有约定跳过。
  WeCom 复用的是与 Telegram 完全相同的 Reconciler/Store 编排，未改动任何
  SQL/迁移，纯单元测试覆盖已经把 `WeComNotifier` 本身的泄漏面测穷举了，
  判断风险可接受；如验收线希望补一轮真库回归，按仓库既有的真库集成测试
  配方（建 scratch 库、灌迁移、挂 `XM_TEST_DATABASE_URL`）即可复现。
- **Telegram 仍是纯 env 装配、未迁移到文件优先链**：本片刻意保持这处不
  一致（见 summary），避免在一个不相关的改动里触碰一条已在生产上跑的路径。
  如果后续要把「设置→凭据」页对 Telegram Bot Token 也生效，需要一个独立的
  小改动去把 `alertSecretsFromEnv` 换成 `connectorSecretsChain` 风格的装配，
  并补相应测试——见下方 follow_ups。
- **未做「按严重度路由渠道」**：三条渠道（Telegram/Webhook/企微）目前仍是
  全部规则共用同一组已配置渠道，这是 README 里 Foundation-A 边界原本就
  声明的既有缺口，本片未扩大也未缩小这个范围。

## follow_ups

1. **部署步骤（留给用户/验收线）**：登录服务器管理后台「设置→凭据」页，
   粘贴现有企业微信群机器人 Webhook 地址（`https://qyapi.weixin.qq.com/
   cgi-bin/webhook/send?key=<真实 key>`），ref 建议使用
   `secret://alerts/wecom-webhook`；随后在对应环境的 `.env` 里设置
   `XM_ALERT_WECOM_WEBHOOK_REF=secret://alerts/wecom-webhook` 并重建
   `platform-worker`（值本身不进任何 `.env` 提交或文档，只经后台文件卷）。
   触发一条真实告警（或临时调低某项阈值）确认企微群收到消息、
   告警页 `notify_status=delivered`。
2. 若产品侧希望 Telegram Bot Token 也能从「设置→凭据」页管理（而不是只能
   写在服务器 `.env` 里），需要把 `alertSecretsFromEnv` 迁移到
   `connectorSecretsChain` 风格的文件优先装配——本片未做，是有意保持的
   最小改动面。
3. Foundation-B 阶段如果要做「按严重度路由渠道」（critical 走企微/
   Telegram、warning 只落库或只走 Webhook），三条渠道现在的装配方式
   （`newAlertNotifier` 里各自独立判断是否启用）已经具备按渠道单独开关的
   基础，只是还没有按严重度过滤的逻辑。
