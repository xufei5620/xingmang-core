-- XM-SMS2 #6（ADR-022 决策 3）：号码记下是哪笔操作买的。
--
-- 要号流程按 request_id 回放时要找回「那次买到的全部号码」；成本核算（XM-SMS3）
-- 要把每个号对到花钱的那笔操作。sms_operation.resource_id 只指向第一个号，
-- 一次买 N 个时另外 N-1 个没人指着——所以反过来在号码上记操作。
-- 导入的号（sms.order.import）为空：它们不是我们这边买的。
ALTER TABLE sms.sms_resource
    ADD COLUMN IF NOT EXISTS operation_id uuid REFERENCES sms.sms_operation (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS sms_resource_operation_idx
    ON sms.sms_resource (environment, operation_id)
 WHERE operation_id IS NOT NULL;
