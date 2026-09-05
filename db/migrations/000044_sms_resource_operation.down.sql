DROP INDEX IF EXISTS sms.sms_resource_operation_idx;
ALTER TABLE sms.sms_resource DROP COLUMN IF EXISTS operation_id;
