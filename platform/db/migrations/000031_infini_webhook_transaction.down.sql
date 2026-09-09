DROP INDEX IF EXISTS cards.webhook_event_transaction_idx;
ALTER TABLE cards.webhook_event
    DROP COLUMN IF EXISTS transaction_status,
    DROP COLUMN IF EXISTS transaction_type,
    DROP COLUMN IF EXISTS related_transaction_id,
    DROP COLUMN IF EXISTS transaction_id;
