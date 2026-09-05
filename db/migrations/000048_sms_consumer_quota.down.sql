DROP TABLE IF EXISTS sms.consumer_quota;
DROP INDEX IF EXISTS sms.sms_operation_principal_idx;
ALTER TABLE sms.sms_operation DROP COLUMN IF EXISTS principal_id;
