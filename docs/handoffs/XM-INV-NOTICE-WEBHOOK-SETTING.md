# XM-INV-NOTICE-WEBHOOK-SETTING：通知地址改为在管理端配置

- **status:** implemented，未发布。**含一个迁移（0028）**，前后端都改。
- **branch:** `ai/claude/XM-INV-NOTICE-WEBHOOK-SETTING`，**基于
  `ai/claude/XM-INV-NOTICE-UI`**（它又基于 NOTICE-VIEW）。合入顺序：
  SUBMIT-NOTICE → NOTICE-VIEW → NOTICE-UI → 本片。
- **产品负责人 2026-09-06：**「这个地址我希望的是在前端可以设置配置。如果写入
  服务器中，那不是想更换很麻烦？」

## 原来错在哪

XM-INV-SUBMIT-NOTICE 把 Webhook 地址放在宿主机一个 0600 文件里，靠
`INVOICE_NOTICE_WEBHOOK_FILE` 指过去。换群、改错、轮换，每一次都要 SSH 上去
改文件再重启 api。**运营会调的东西不该长在服务器上**——这条在星芒平台那边
早有共识（后台 + Action 管运营参数，env 只留"会不会花真钱"），开票这边没照做。

## 改成什么

存进库、在**管理端 → 设置 → 企业微信通知**里填。手法照抄同一页上的 SMTP
授权码：密文进库、页面永不回读、投递时现取。

- **地址每次投递现取**（`WeComSender` 从"构造期定死一个 endpoint"改成
  "每次 `Send` 调 `resolve(ctx)`"）。这是这一片的关键：改完地址，**下一条
  通知就用新的，不必重启**。
- **形状校验从进程启动挪到保存那一刻**。原来"填错就让 api 拒绝启动"听着严格，
  实际上是把错误推迟到没人看的时候；现在保存时就报，人正好在页面前。
- **投递循环无条件挂载**。原来是"配了文件才挂"，而地址已经不是启动期常量了。
  没配地址时投递会以"地址取不到"失败并记进发件箱——那正是运营该看到的事实
  （申请详情里那一格会显示失败原因），比进程里悄悄没有这个循环诚实。
  失败按指数退避，第 8 次停住，不会把队列跑干。

## 三处关于凭据的讲究

**一、独立的表，不加一列到 `admin_setting_secrets`。** 那张表的
`smtp_secret_ciphertext` 是 `NOT NULL`，而 `ClearSMTPSecret` 走的是
`DELETE FROM admin_setting_secrets`——挂上去的话，**清一次 SMTP 口令就会把通知
地址一起抹掉**。两个凭据的生命周期本来就无关。

**二、展示面只给指纹，不给地址的任何片段。** 整个 URL 就是凭据（企微把鉴权
key 放在查询参数里），露出 key 的前几位就是露出凭据的前几位。所以存的是
`sha256(地址)` 的十六进制前 16 位——**可以拿去和"我刚才粘的那个"比对，却反推
不出地址**。这与星芒平台凭据页显示指纹前缀而非值前缀是同一条纪律。
指纹列还有一条 `CHECK (fingerprint ~ '^sha256:[0-9a-f]{16}$')`，防的是将来
有人绕过封装直接写库。

**三、要确认"配对没有"，用「发送测试消息」。** 消息到没到那个群，比在页面上
看一段前缀可靠得多——正因为有这个按钮，我们才敢在展示上一个字符的地址都不回读。

保存/清除各留一条审计（`admin_settings.notice_webhook.set` / `.clear`），
**审计的 before/after 存的是指纹与"未配置"**，地址从不进那个函数。

## 文件

- 新增：`backend/migrations/0028_notice_webhook_setting.sql`、
  `backend/internal/adminsettings/notice_webhook_test.go`、
  `backend/internal/adminsettings/notice_webhook_integration_test.go`、
  `web/src/lib/http-api.notice-webhook.test.ts`
- 修改（后端）：`internal/notify/wecom.go`（现取地址 + 导出
  `ValidateWebhookAddress`）、`internal/adminsettings/{types,service,postgres,memory}.go`、
  `internal/httpapi/server.go`（三条路由 + 设置响应多一格）、`cmd/api/runtime.go`
- 修改（前端）：`web/src/types.ts`、`web/src/lib/{api-contract,http-api,mock-api}.ts`、
  `web/src/App.tsx`（设置页新增一张卡片）
- 修改（部署与文档）：`deploy/docker-compose.prod.yml`、
  `deploy/.env.production.example`（删掉 `INVOICE_NOTICE_WEBHOOK_FILE`）、
  `docs/handoffs/XM-INV-SUBMIT-NOTICE.md`（启用一节重写）

## 测试

- `notice_webhook_test.go`（服务层，6 条）：形状不对被拒**且错误不回显地址**；
  指纹是地址的稳定函数、trim 后再算、不同地址不同指纹、里面没有地址片段；
  投递取回的就是存进去的；展示接口与投递接口是两条路；清除之后投递 fail
  closed（不回退到旧值）；缺 actor 不许写。
- `notice_webhook_integration_test.go`（**真库**）：迁移建表；没配时不算错误
  且投递取不到；保存→读回→覆盖保存仍是一行；**库里的指纹列不含地址**；
  **审计的 before/after 不含地址**且两次保存两条 set；清除后行没了、投递
  fail closed、多一条 clear 审计；另有一条钉住指纹列的 CHECK——把地址写进
  指纹列必须被数据库拒绝。
- `notify_test.go`：新增"地址每次投递现取"（三次发送要 resolve 三次）与
  "取不到地址时 fail closed 且不转述底层错误"；原来的构造期校验测试改为测
  `ValidateWebhookAddress`。
- `http-api.notice-webhook.test.ts`（前端，5 条）：后端没有这个字段时算未配置、
  `configured` 只认布尔 `true`；已配置时带出指纹与更新信息且里面没有地址；
  保存打 admin 的 PUT 且**地址进 body 不进 URL**（URL 会进访问日志、Referer、
  浏览器历史）；清除是 DELETE、测试是 POST 到 `/test`。

**做过变异验证**：把保存改成把地址拼进 query string，「地址进 body 不进 URL」
那条如期变红。

门禁：后端 `go build ./...`、`go vet ./...`、`go test -p 1 -count=1 ./...` 全绿
（带 `INVOICE_TEST_DATABASE_URL`）、`gofmt`（LF 归一后）干净；
前端 `tsc --noEmit`、`vitest run`（15 文件 196 条）、`npm run build` 全过；
`gitleaks protect --staged` 无泄漏；compose YAML 可解析。

## 上线

随下一个 RC 一起发（**含迁移 0028**）。发完之后：管理端 → 设置 →
企业微信通知，粘贴地址 → 保存 → 发送测试消息。此前攒下的通知会在下一轮投递
里补发出去。

## follow_ups

- 没有"轮换提醒"：不知道这个地址多久没换过。凭据轮换周期是全局待补的字段
  （星芒平台凭据页也缺），到时候一起做。
- 测试消息发的是**已保存的**地址，也就是必须先保存才能测。先测后存需要把
  地址明文再传一次，多一次暴露面，不值得。
- 平台侧那三个 WeCom 地址（告警/卡片/接码）仍在平台凭据页，与这一个不在同
  一处。这是 ADR-018 的通道隔离所致，不是疏漏：开票系统自己发通知，就用
  自己的凭据。
