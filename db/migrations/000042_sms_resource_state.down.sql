DROP INDEX IF EXISTS sms.sms_resource_state_idx;
ALTER TABLE sms.sms_resource DROP COLUMN IF EXISTS state;
