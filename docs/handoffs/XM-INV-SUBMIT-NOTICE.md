# XM-INV-SUBMIT-NOTICE：用户提交开票申请后，推一条企业微信通知

- **status:** implemented，未发布。**对现有环境零改动**：没配
  `INVOICE_NOTICE_WEBHOOK_FILE` 时投递循环整个不挂载。
- **branch:** `ai/claude/XM-INV-AUTOLOGIN`（发布线），worktree `K:/发票/wt-XM-INV-AUTOLOGIN`。
- **产品负责人 2026-09-06：** 「开票这边用户提交了开票应该需要有一个通知发到
  Webhook 企业微信那边」。
- **消息格式**遵循平台线 XM-NOTIFY-ENVELOPE 的约定（见平台仓库
  `docs/modules/notify/README.md`）：每条消息自带域徽标、严重度、环境、类型
  编号与处理入口，因为运营把同一个 Webhook 地址填给了告警、卡片、接码、开票
  四个来源，它们落在同一个群里。

## 一处与原设计不同，先说明

平台线的信封设计稿 §6 原本写的是「平台侧只读轮询开票线的
`GET /api/v1/admin/invoice-requests?status=pending_review`」。**没有按那条做**，
理由在动手时就浮出来了：

1. 那条路要先冻结 CR-0002（跨线只读契约，状态仍是「待开票线确认」），再实现
   XM-0029（真实只读客户端，今天 `connectors/invoice` 只有契约与 Fake）。两件
   都不是这一片能自己完成的，产品负责人要的功能会被一道治理闸挡住。
2. 那条路要平台以 admin 身份调开票的管理端接口——而 ADR-018 的通道隔离正是
   为了让平台**没有**开票的管理员身份。
3. 提交是开票系统自己处理的一次写入，它在那一刻就知道全部事实。轮询是把一个
   已知的事件变成一个要去发现的状态，还要额外的水位与去重。

所以改成：**开票系统在提交那一刻自己入队、自己投递**，用自己的 Webhook 凭据。
不新增任何跨线面，不需要 CR。

代价是消息渲染器在两个仓库各有一份（平台 `internal/platform/notify`，开票
`backend/internal/notify`）。这是刻意的：两个系统隔着 ADR-018 的边界，为了共用
80 行而拉一条依赖，会让开票系统的发布跟着平台的版本走。共用的是**格式约定**，
由两边各自的测试各自钉住。

**CR-0002 仍然需要**，只是不再挡这条通知：看板与「待审核积压 > 12h」这类
**跨系统聚合**仍要平台能读开票数据。两者不冲突——提交通知是事件源头的事，
积压告警是对状态的周期评估。

## 做了什么

**发件箱而不是「提交时顺手发个 HTTP 请求」**（迁移 `0027_invoice_notice_outbox.sql`）：
提交是用户的一次写入，推送是一次跨网络调用。放在同一条同步路径上，企微慢一秒
用户就慢一秒；企微挂了，提交要么跟着失败（荒谬），要么静默丢一条通知（没人
知道丢了）。发件箱把两件事分开——入队与提交**同事务**（通知行存在当且仅当申请
真的提交成功了），投递由后台循环带重试完成，成败都留痕。手法照抄本仓库既有的
`email_outbox`，不复用那张表：它是发票邮件专用的（收件人哈希、文档、模板版本），
字段一个都不适用。

**表里不存正文。** 要发的内容（单号、金额、状态、来源、提交时刻）在投递那一刻
从 `invoice_requests` 现取。两个理由：业务数据不复制第二份，通知永远不会与记录
漂移；更要紧的是，申请里的抬头、税号、银行账号、地址、电话在
`profile_snapshot_ciphertext` 里加密存着，通知只读那几列非 PII 字段，于是
**任何 PII 都不会因为「要发通知」而落进一张新表**。

**投递循环**（`backend/internal/notify`）：取一批、逐条投递、成败各自落库；单条
失败不影响同批其它条，整轮也不因此失败（下一轮重试）。取件把 `next_attempt_at`
推后 5 分钟当租约——投递中途崩掉的那一行会自己回到队列。失败按次指数退避
（30s 起、1800s 封顶），第 8 次后停在 `failed`：一条两小时前的「有人提交了开票
申请」已经不是通知而是历史，而真正的故障（地址填错、机器人被移出群）不会因为
多试几次就好；行留着，看得见它失败了。

**凭据**：整个 Webhook 地址就是凭据（企微把鉴权 key 放在查询参数里），从**文件**
读、不从环境变量读，与本仓库其它凭据同一手法。地址形状在**构造期**校验：填错
让 api 拒绝启动并说明原因（错误里不含地址本身）——一个填错的地址应该在进程起来
时就暴露，而不是等到第一条通知该发的时候，那时候没人在看日志。

## 文件

- 新增：`backend/migrations/0027_invoice_notice_outbox.sql`、
  `backend/internal/postgresstore/notice_outbox.go`（+ 集成测试）、
  `backend/internal/notify/{notify.go,wecom.go,worker.go}`（+ 测试）
- 修改：`backend/internal/postgresstore/store.go`（`Submit` 事务内入队）、
  `backend/cmd/api/runtime.go`（条件挂载投递循环）、
  `deploy/docker-compose.prod.yml`（透传变量）、`deploy/.env.production.example`

## 测试

- `backend/internal/notify`：信封逐字段；**「通知载荷不得携带开票 PII」**（用反射
  钉住 `InvoiceNotice` 的字段集合——将来有人加字段必须先撞到这条）；金额不经
  float；未知状态原样显示；按字节截断且头部与编号保留；多行值不能跳出引用块；
  地址形状校验且错误不回显凭据；HTTP 200 且 errcode≠0 算失败且不转述上游原文；
  worker 的成功/失败/取件失败三条路径。
- `backend/internal/postgresstore`：取件带出申请自身字段；租约不重复取件且到期
  自动回队；已送达终态、失败重排；到达次数上限停在 failed；同一申请同一事件
  只入队一次。
- 全量：`go test -p 1 -count=1 ./...`（带 `INVOICE_TEST_DATABASE_URL`）。

## 启用（生产，需产品负责人执行）

1. 把企业微信群机器人的 Webhook 地址写进宿主机一个 0600 文件，例如
   `/root/invoice-system/config/notice-webhook`。
2. `deploy/docker-compose.prod.yml` 的 api 服务 `volumes` 加一行：
   `- ${INVOICE_NOTICE_WEBHOOK_HOST_FILE}:/config/notice-webhook:ro`
   （不预先写这行：compose 的挂载没有「可选」，写了就必须存在，而这一片的前提
   是对现有环境零改动。）
3. `.env.production` 里设
   `INVOICE_NOTICE_WEBHOOK_HOST_FILE=/root/invoice-system/config/notice-webhook`
   与 `INVOICE_NOTICE_WEBHOOK_FILE=/config/notice-webhook`。
4. 重启 api。此前攒下的通知会一起补发出去（发件箱一直在入队）。

## follow_ups

- ~~管理端还没有「通知发件箱」的只读视图~~ —— 已由 **XM-INV-NOTICE-VIEW** 补上：
  `GET /api/v1/admin/invoice-requests/{id}/notices`（见
  `docs/handoffs/XM-INV-NOTICE-VIEW.md`）。
- 只有 `request.submitted` 一种事件。审核通过、开具完成要不要也推，等产品负责人
  定——每多一种就多一份群消息，太吵会让人把整个群静音，那比不发还糟。
- 「待审核积压 > 12h」仍属平台侧告警规则，依赖 CR-0002 冻结。
