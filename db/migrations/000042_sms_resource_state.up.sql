-- XM-SMS2（ADR-022 决策 4）：号码的统一状态。
--
-- status 列是上游原话（Hero 是 1/2/3/4/6/7/8/10，62 是「正常」），页面上没有统一
-- 含义。state 是平台自己的五态：waiting_code / code_received / finished /
-- cancelled / expired。两列并存：原话用于排查，统一状态用于判断。
ALTER TABLE sms.sms_resource
    ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT '';

-- 回填已有行。Hero 按官方状态码映射（与代码里 MapHeroStatus 同一张表），
-- 62 一律待收码；本地已经取到过码的一律已收码——码是我们自己的事实，比上游
-- 状态更可信。
UPDATE sms.sms_resource r
   SET state = CASE
       WHEN EXISTS (SELECT 1 FROM sms.sms_code c WHERE c.resource_id = r.id) THEN 'code_received'
       WHEN r.provider = 'hero_sms' AND r.status = '4'  THEN 'code_received'
       WHEN r.provider = 'hero_sms' AND r.status = '6'  THEN 'finished'
       WHEN r.provider = 'hero_sms' AND r.status = '7'  THEN 'expired'
       WHEN r.provider = 'hero_sms' AND r.status IN ('8', '10') THEN 'cancelled'
       ELSE 'waiting_code'
   END
 WHERE r.state = '';

CREATE INDEX IF NOT EXISTS sms_resource_state_idx
    ON sms.sms_resource (environment, state);
