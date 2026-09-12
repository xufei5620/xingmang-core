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
证明其基础状态，其他容器健康仍严格。D 保留全部 12 步冻结副本读写验证、
扫描/上传/下载与 session 撤回，不能以基础就绪通过代替这些步骤。

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
外层 D 交接必须同时验证完整 D PASS、清理、同 HEAD/manifest、12 步结果以及
基础阶段的真实 accepted response；不得沿旧 `READY`/first-all-ready 字段推断。
