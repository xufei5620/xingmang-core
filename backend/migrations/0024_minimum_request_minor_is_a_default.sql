-- XM-INV-SETTABLE-INVOICE-MINIMUM: the minimum invoice amount was meant to be
-- an administrator setting whose default is ¥200, but ¥200 was also enforced
-- as an absolute floor -- here in the CHECK, and again in
-- adminsettings.MinimumMinor and both frontend validations -- so the setting
-- could only ever move upward. The product owner could not lower it to run a
-- small-amount test, which is what surfaced this: "默认为200，如果有需要的时候
-- 我可以调整到5".
--
-- The default stays 20000 (¥200.00): a fresh installation still starts at the
-- business norm, and nothing about an existing row changes. Only the floor
-- moves, to "a positive amount", which is the one thing that is genuinely not
-- an administrator's call -- an invoice minimum of zero or less is not a
-- policy choice, it is a broken setting.
--
-- Deliberately NOT re-imposing a smaller arbitrary floor (¥1, ¥5): that would
-- recreate the same problem in miniature, and the next person needing a value
-- below it would be blocked for the same reason. Changes to this setting are
-- admin-only, carry optimistic-concurrency revisions and land in the audit
-- trail, which is where the real control over a wrong value belongs.

ALTER TABLE admin_settings
    DROP CONSTRAINT IF EXISTS admin_settings_minimum_request_minor_check;

ALTER TABLE admin_settings
    ADD CONSTRAINT admin_settings_minimum_request_minor_check
    CHECK (minimum_request_minor > 0);
