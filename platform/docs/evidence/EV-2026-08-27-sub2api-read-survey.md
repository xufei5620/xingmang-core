# EV:Sub2API 只读普查 + XM-0017 兼容性核对(2026-08-27)

- 来源:Opus 勘察代理对 K:/sub2api-src @ efb46db(0.1.183)的只读普查(全文四部分见会话)
- 平台客户端基线是 0.1.133;本次跨 50 个 patch 版逐个 git diff 实证

## XM-0017 兼容性核对结论(7 个在用端点)
- [不变] /health、/admin/system/version、/admin/dashboard/stats、/admin/users、/admin/dashboard/trend、/admin/accounts(共 5+1)
- 🔴[破坏性] /admin/payment/dashboard:金额从标量 float 变为 CurrencyAmounts=map[币种]float。
  故障链:rawAmount 拿到 `{"CNY":..}` 字面量→decimalToMinorUnits 失败→bad_response。
  **最阴险:空实例/无充值日联调全绿,一有真实收入才炸。**
  修:upstream.go Amount 改 map[string]rawAmount 按 Currency 取,取不到标 IsPartial。
- 🔴[新中间件] admin 组新增 AdminComplianceGuard:未确认合规全部 GET 返 423。
  修:client.go classifyStatus 加 `code==423→KindAuth`;接入前先 POST /admin/compliance/accept。
- [死代码] version 的 commit/build_type 在 0.1.133 本就不存在,fingerprintExtra 恒为"|",清理。

## 现有指标语义纠错(P0)
sub2api.channels.balance 读的是 accounts.extra 里运营手填的本地额度(quota_limit/used),
被 IsAPIKeyOrBedrock+limit>0 双门卡住,订阅型账号静默空集——名不副实。
改名 accounts.quota_used/limit(注明仅 apikey/bedrock);真渠道余额走 0.1.183 新端点
/cn-providers/accounts/:id/balance(逐账号,≥15min 轮询)。

## 利润核算(比 NewAPI 好:三分口径)
- total_cost(清单价) / actual_cost(真扣余额=收入) / account_cost(平台对渠道成本的**建模估算**,非真实上游账单)
- 4 个端点同时带 actual+account 可算毛利;/dashboard/trend 缺 account_cost → **逐日毛利折线做不到**(唯一需只读 DSN 的场景)
- 收入侧 /payment/dashboard 是毛收入(不扣退款)且按币种分桶,禁隐式加总

## 红旗(采集必丢)
明文:api_key.key、accounts.credentials/extra、proxy.password、plugins 解密配置、prompt_audit full_prompt;
邮箱全线明文;5 个 mock 端点(/users/:id/usage、/dashboard/realtime、/redeem-codes/stats、
/groups/:id/stats、/proxies/:id/stats)返回假数据 200,写契约黑名单。

## 只读 DSN 天花板
仅"逐日毛利折线"值得走 DSN(usage_dashboard_daily 有 account_cost 而端点没有);
若走:只读副本+只读角色+default_transaction_read_only、白名单到 5 张聚合表、金额列 ::text 取出
(比 HTTP 更干净,规避 float);限定这一组,其余走 HTTP。

## 采集优先级
①修 channels.balance(纠错) ②渠道可用性/ops/account-availability(一调用五指标零PII)
③毛利 usage/stats+user-breakdown ④订阅消耗 groups/usage-summary+subscriptions/progress
⑤收入口径修正+回填 0.1.183 版本矩阵

## 接入前置(两件)
1) POST /admin/compliance/accept 2) 不采 5 个 mock 端点
