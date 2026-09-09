-- XM-INV-SETTABLE-INVOICE-MINIMUM, third and final layer.
--
-- `invoice_requests.amount_minor >= 20000` has been in migration 0001 since the
-- beginning, from when ¥200 was a fixed rule rather than a setting. Two earlier
-- passes at making the minimum configurable missed it: RC89 relaxed the
-- migration 0002 CHECK, `adminsettings` and both frontend guards and claimed
-- "five places"; RC90 added `domain.MinimumRequestMinor`'s six guards. Neither
-- searched for the number itself, which is why this one survived twice.
--
-- What it cost: with the setting at ¥5.00, an account showing 可开票 ¥5.00
-- submitted, every check in the application passed, and the INSERT was then
-- refused by this constraint. Because a raw database error matches none of the
-- handler's sentinels it fell through to a bare HTTP 500 that logged nothing,
-- so the api's own log for that window held four lines and none of them was
-- about the request. Production returned that 500 three times before the cause
-- was found by reproducing it locally.
--
-- The floor here is now the same one everything else uses: a positive amount.
-- The real minimum is `admin_settings.minimum_request_minor`, enforced in the
-- application (`postgresstore.Submit`, `ledger.Service`, `application.Service`)
-- where it can produce a 422 that says which number was violated. A database
-- CHECK cannot know the configured value, so it must not pretend to police it.
--
-- `invoice_allocations.amount_minor > 0` (migration 0001 line 134) already had
-- the correct shape and is untouched.

ALTER TABLE invoice_requests
    DROP CONSTRAINT IF EXISTS invoice_requests_amount_minor_check;

ALTER TABLE invoice_requests
    ADD CONSTRAINT invoice_requests_amount_minor_check
    CHECK (amount_minor > 0);
