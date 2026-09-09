ALTER TABLE cards.infini_card
    DROP COLUMN IF EXISTS subscription_cycle,
    DROP COLUMN IF EXISTS subscription_amount_text;
