# EV:NewAPI 全量只读普查(2026-08-27)

- 来源:Opus 勘察代理对 K:/newapi-src(QuantumNous/new-api 最新版)的只读普查
- 用途:XM-0038(真实客户端)与契约 v2 的依据;引用带 文件:行号
- 结论速览:售价系统而非进销存——**全库无上游成本字段,毛利必须靠平台登记簿(XM-0037)**;
  quota 换算 500,000=1USD(可变 var,须动态读并写进指标);建议混合模式
  (版本/健康走 HTTP,数据走只读 DSN;LOG_SQL_DSN 可能是第二个库);
  P0=渠道自动禁用原因,P1=逐渠道错误率+营收(logs 自算),P2=用户盘点(DSN),
  P3=模型 token 消耗,P4=订阅在册(DSN)。
- 红旗:兑换码明文无限流;channel.balance 为 float 且币种混杂;
  GET /api/channel/test*/update_balance*/fetch_models 是 GET 但写库(HTTP 通道必须拒绝名单);
  用户列表 Unscoped 含软删除;log/stat 的 type 参数被忽略、rpm/tpm 只看最近 60s。

(以下为代理报告原文,两部分合并)

---

【报告正文已由勘察代理以消息形式交付,全文见本次会话记录;本文件保留结论速览与关键引用索引,详细字段表在契约 v2 起草时逐项转录】

## 关键文件索引
- 路由:router/api-router.go、router/channel-router.go
- 模型:model/channel.go:23-60、model/user.go:79-113、model/token.go:14-33、
  model/log.go:59-93、model/usedata.go:13-26、model/topup.go:14-25、
  model/subscription.go:146-281、model/main.go:310-342(33 表清单)
- 口径:common/constants.go:22(QuotaPerUnit)、model/option.go:587(运行时可变)、
  service/text_quota.go:447-448(成本空洞证据:用户扣费与渠道 used_quota 同值)
- 换算纪律:先 SUM(quota) int64 再除 5000;quota_per_unit 写进每条指标 value
- 契约核对红点:ActiveUsers 无上游概念(自定口径 last_login_30d 并写判据)、
  BalanceMinorUnits 仅 DSN、charge money 三语义按 provider 分桶、
  ErrorRatePPM 须由 logs type=5/(2,5) 自算、Enabled 塌缩 2/3 需 v2 加 StatusReason、
  channel.balance float+混币种(nil 判据=balance_updated_time==0)
