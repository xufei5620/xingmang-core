ALTER TABLE sms.sms_operation DROP CONSTRAINT IF EXISTS sms_operation_kind_known;
ALTER TABLE sms.sms_operation ADD CONSTRAINT sms_operation_kind_known CHECK (kind IN (
    'purchase', 'cancel', 'finish', 'replace', 'reactivate', 'prolong'
));
ALTER TABLE sms.sms_operation DROP COLUMN IF EXISTS email_id;
DROP TABLE IF EXISTS sms.sms_email;
ALTER TABLE sms.sms_resource
    DROP COLUMN IF EXISTS operator,
    DROP COLUMN IF EXISTS price_text,
    DROP COLUMN IF EXISTS verification_type,
    DROP COLUMN IF EXISTS subtype,
    DROP COLUMN IF EXISTS country_phone_code;
