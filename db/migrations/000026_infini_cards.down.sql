-- 回滚 XM-CARD1 的投影表与操作台账。
--
-- 注意：回滚会丢掉操作台账，包括处于 unknown 状态、尚未收敛的记录。
-- 那些记录是「可能已经花了钱但不确定」的唯一凭证，丢了就只能去上游后台
-- 逐张卡人工核对。生产回滚前必须先导出 cards.card_operation。
DROP TABLE IF EXISTS cards.card_operation;
DROP TABLE IF EXISTS cards.infini_card_transaction;
DROP TABLE IF EXISTS cards.infini_card;
DROP SCHEMA IF EXISTS cards;
