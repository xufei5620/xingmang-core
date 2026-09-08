-- 按外键的反方向删：publish_record 引用 draft/channel，draft_asset 引用
-- draft/asset，draft_revision 引用 draft。顺序写错会撞外键。
DROP TABLE IF EXISTS publishing.publish_record;
DROP TABLE IF EXISTS publishing.draft_asset;
DROP TABLE IF EXISTS publishing.asset;
DROP TABLE IF EXISTS publishing.draft_revision;
DROP TABLE IF EXISTS publishing.draft;
DROP TABLE IF EXISTS publishing.channel;
-- schema 本身只在空了之后才删；有别的东西建在里面就留着，不连坐。
DROP SCHEMA IF EXISTS publishing RESTRICT;
