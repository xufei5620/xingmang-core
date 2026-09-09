-- 回滚 XM-CARD-VISIBILITY 的按账号暂停开关。
--
-- 直接删表：这张表只装运营手动设置的开关，没有它同步就回到「全部账号都同步」
-- 的既有行为。回滚之前请确认没有账号正被暂停着——否则回滚等于把一个被人为
-- 停掉的账号悄悄放回同步循环。
DROP TABLE IF EXISTS cards.account_sync_pause;
