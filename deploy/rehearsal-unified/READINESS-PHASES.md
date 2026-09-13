# 就绪的基础阶段与来源追平阶段

本判据按负责人本轮授权替换“D/E 均在 300 秒内所有来源新鲜度全绿”。
原普通 `/readyz` 的 200/503、业务提交保护、11 闩短路顺序保持不变。

`GET /readyz?report=full` 是只读机器报告。HTTP200 仅表示采样成功；必须核验
`report_schema=xingmang.readiness-evaluation/v1`、原 body 状态、三模块和完整
invoice 字段。HTTP 错误、连接错误、缺字段及未评估不是来源过期证明。
新报告只输出固定闩键、状态和原因枚举，不含来源 ID、路径、DSN 或原始错误。

`invoice.checks` 必须恰为以下 11 键，每项仅有 `status`：

`database`, `admin_settings`, `invoice_issuer`, `clamav_daemon`,
`clamav_signatures`, `pdf_scanner`, `source_health_query`, `source_ingest`,
`eligibility_health_query`, `eligibility_projection`, `source_streams`。

状态为 `ready/not_ready/not_evaluated`。原短路后的闩显式未评估，不能推断通过。
最后 source_streams 的完整报告还要核结构、两平台、启用来源五流、版本、
blocked、dead/contained、标志与原因的一致性后，才能使
`source_non_freshness_status=ready`。原 pending/contained 业务容忍不改变。

`source_freshness` 有 `status/reasons`；固定理由为 heartbeat、watermark 各自的
`source_heartbeat_expired`, `source_watermark_expired`,
`source_heartbeat_missing`, `source_watermark_missing`,
`source_heartbeat_future`, `source_watermark_future`。
它按真实时戳、同次时钟和原精确 policy duration 计算；保持原五分钟时钟容忍。
旧 `STREAM_STALE` 同时覆盖过期和未来异常，不能直接当过期白名单。
旧 active rescan grace 可能令原 readiness 绿但 watermark 仍过期；新追平阶段
必须使用显式 freshness，不能借 grace 宣称已恢复。

D 的 300 秒窗口从新栈启动请求起一次计时，包括两项目启动/等待、容器核对
和 HTTP 采样。前 10 闩、platform/projection 模块与 source 非新鲜度状态必须
绿；freshness 允许绿，或仅非空、无重复的 heartbeat/watermark 两种 expired。
missing、future、其他错误、未知值、未评估全部拒绝。原 API 严格 healthcheck
不改；这一阶段仅该已固定身份的 API 可处于 starting/unhealthy，由完整报告
证明其基础状态，其他容器健康仍严格。来源新鲜时，D 保留原完整 12 步冻结
副本提交、审核、扫描/上传/下载与 session 撤回，报告 `full-write`。

仅实际 `server-rehearsal` 的受核冻结副本可使用来源过期覆盖：原 300 秒
`BASE_READY` 的已接受原始 response/SHA、同 HEAD/manifest/owner 与即时完整
typed 报告均须通过原判定器，且原因只能为明确的 heartbeat/watermark expired。
SUB 合成资金批次还须是 verified、未退款、实际已消费的 wallet，其 HTTP 响应
必须为 `source_unavailable / SOURCE_NOT_READY / available_minor=0`。随后使用
本次合成用户和资料实际提交，只接受 HTTP503 + `SOURCE_SYNC_UNAVAILABLE`。
前后读取 SUB/NEW 合成用户申请集合，并重新核验冻结数据库身份、owner guard，
只读断言这两用户申请计数均为零且两合成批次金额/预留/已开金额不变。

此分支将两步写链替换为 `sub.submit-blocked`、`invoice.unchanged`，报告
`financial_coverage=blocked-write` 和 `document_coverage=not_exercised_source_expired`，
明确未执行审核、上传或下载，不冒充完整开票成功。身份、TOTP、隔离、后台、
旧入口拒绝、前后 readiness 和 session 撤回仍执行。其他 503、HTTP/连接错误、
缺字段、future、not_evaluated、资金/申请变化均失败；不能单靠聚合过期理由
替代 SUB 批次和实际业务拒绝证明。原生产金额/新鲜度门禁、水位和数据库不改。

E 在首次请求启动来源采集器前创建一个 900 秒追平 deadline。它不等成功连接
后才计时，不在基础就绪、切流量或每次 poll 时重置。基础状态仍须在原 300 秒
窗口内通过；之后允许原 nginx 切流量，再在同一个 900 秒窗口内要求原完整
readiness、全部模块、显式 freshness 及全部容器健康通过，随后执行原 public5
smoke 并 COMMITTED。追数据期间提交保护继续 fail closed。任意失败仍进入原
自动 rollback；超时后不会阻塞 cleanup/rollback，也没有自动重试或人工救援。
COMMITTED 后的 30 分钟只读观察由外部负责人流程记录，不增加自动回滚条件。

`candidate-readiness.json` 记录 `phase=non_freshness`、300 秒、真实原始采样
文件/SHA、UTC/monotonic、`status=BASE_READY` 及 `expected_source_expiry`。
这个状态不声称来源已经新鲜。E 的 `source-freshness.json` 单独记录
`phase=source_freshness`、900 秒和唯一启动 trigger，只有完整追平才 `READY`。
外层 D 交接必须同时验证 D PASS、清理、同 HEAD/manifest、对应覆盖的完整步骤
以及基础阶段真实 accepted response。过期覆盖不得标记为提交、扫描或下载通过，
也不追认之前已经失败的 D。E 内部冻结预检沿用此规则；正式 E public smoke
仍只读，900 秒完整追平要求不变；不得沿旧 `READY`/first-all-ready 字段推断。
