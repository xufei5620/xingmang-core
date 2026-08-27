# EV:上游余额→真实成本 可得性研究(2026-08-27)

- 来源:Opus cost-recon 代理对 K:/newapi-src、K:/sub2api-src@efb46db 的只读研究
- 触发:产品负责人指出上游余额可查=真实成本(渠道列表"已使用/剩余"、查询余额菜单)
- 全文见会话;本文件固化结论与采集方案

## 一句话结论
"查余额做差分=真实成本":**Sub2API 基本走得通(Grok/Kimi/DeepSeek payg 可对账),
NewAPI 基本走不通**(60 种渠道类型仅 10 种支持查余额,且不含 Claude/Gemini/xAI 主力;
余额默认死水)。两上游都只存最新余额不存长期历史——**差分快照必须平台自建**。

## 可得性分档
- 可对账:Sub2API Grok(xAI 官方 billing prepaid_balance/monthly_used,USD)、
  Kimi payg(CNY)、DeepSeek payg(CNY+USD);NewAPI DeepSeek/SiliconFlow/Moonshot/OpenRouter(需先开定时刷新)
- 仅参考:NewAPI 转售商类(AIProxy/API2GPT/Custom/OpenAI 旧账单 API)
- 不可得:Sub2API 订阅/套餐类(Claude/Codex/Gemini/coding,只有用量%,成本=固定月费边际≈0)、
  NewAPI ~50 种(Claude/Gemini/xAI/Vertex/AWS 等 default 未实现)

## 关键机制
- NewAPI:channel.balance(USD)+balance_updated_time+used_quota 平台读得到(仅 Omit key);
  used_quota=售价非成本(text_quota.go:448 同 summary.Quota);自动刷新需 CHANNEL_UPDATE_FREQUENCY
  非空否则死水;平台不要自己调 update_balance(写权限+会禁用渠道);无充值/历史表
- Sub2API:**管理员 token 读 /admin/accounts 的 extra 字段即拿到全部余额快照,零外呼纯读**
  (grok_billing_snapshot、{provider}_balance*,redact 只剔 3 个 Ollama 键);
  历史仅留 1 天,日聚合不保 balance;upstream_billing_probe 是费率倍率非美元余额

## 采集方案(推荐:读上游已存余额自建差分,不做平台直连)
- 读取:Sub2API 轮询 /admin/accounts 读 extra 快照(主力,纯只读);
  NewAPI 轮询 /api/channel/ 读 balance(需先开定时刷新,覆盖率低)
- 差分:余额型 cost=max(0,bal[t-1]-bal[t]),正跳变判充值不计负成本;
  累计型(grok monthly_used)同账期内差分,骤降=账期重置
- 去重:Sub2API 按 account.id 唯一;NewAPI 渠道级余额+共享账号需按 type+base_url 归并
- 币种:存原生+汇率归一 USD

## 边界
订阅制上游拿不到边际成本(固定月费);"剩余$0"三义(没查过/用光/类型不支持,靠更新时间+类型+禁用原因区分);
NewAPI 主力上游全盲;币种混杂;新鲜度取决于上游刷新频率(NewAPI 默认死水)

## 待与 SoloAI 现行核算对照(soloai-cost-study 进行中)
若 SoloAI 已在做余额差分,平台照搬其方法;若 SoloAI 用成本单价表估算,则平台 XM-0037 登记簿照其结构
