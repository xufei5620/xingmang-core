DROP TABLE IF EXISTS alerts.upstream_version_ack;

ALTER TABLE alerts.alert
    DROP CONSTRAINT IF EXISTS alert_trigger_count_positive;

ALTER TABLE alerts.alert
    DROP COLUMN IF EXISTS first_opened_at,
    DROP COLUMN IF EXISTS trigger_count;
