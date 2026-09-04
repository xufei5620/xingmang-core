-- 回滚卡面明文落库。
--
-- 注意：这会**删掉已经落库的卡号、CVV 与有效期**。回滚后要重新拉取的话，
-- 同步作业会对每张卡再调一次 /v2/cards/reveal。
DROP INDEX IF EXISTS cards.infini_card_pan_pending_idx;
ALTER TABLE cards.infini_card
    DROP COLUMN IF EXISTS pan,
    DROP COLUMN IF EXISTS cvv,
    DROP COLUMN IF EXISTS expiry_mmyy,
    DROP COLUMN IF EXISTS pan_fetched_at;
