ALTER TABLE sms.provider_status ADD CONSTRAINT provider_status_provider_known CHECK (provider IN ('sms62', 'hero_sms'));
ALTER TABLE sms.sms_order       ADD CONSTRAINT sms_order_provider_known       CHECK (provider IN ('sms62', 'hero_sms'));
ALTER TABLE sms.sms_resource    ADD CONSTRAINT sms_resource_provider_known    CHECK (provider IN ('sms62', 'hero_sms'));
ALTER TABLE sms.sms_resource    ADD CONSTRAINT sms_resource_token_shape CHECK (
    (provider = 'sms62' AND provider_token <> '')
    OR (provider = 'hero_sms' AND provider_token = '')
);
ALTER TABLE sms.sms_operation   ADD CONSTRAINT sms_operation_provider_known   CHECK (provider IN ('sms62', 'hero_sms'));
ALTER TABLE sms.sms_code        ADD CONSTRAINT sms_code_provider_known        CHECK (provider IN ('sms62', 'hero_sms'));
ALTER TABLE sms.sms_email       ADD CONSTRAINT sms_email_provider_known       CHECK (provider IN ('hero_sms'));
