ALTER TABLE cards.infini_card_transaction
    DROP COLUMN IF EXISTS settled_at,
    DROP COLUMN IF EXISTS transaction_currency,
    DROP COLUMN IF EXISTS transaction_amount_text;
ALTER TABLE cards.infini_card
    DROP COLUMN IF EXISTS issue_pay_amount_text,
    DROP COLUMN IF EXISTS issue_fee_text;
