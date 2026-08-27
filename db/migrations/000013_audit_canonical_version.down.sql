-- 仅供本地开发重置（规格 §5.7）。生产回滚走备份恢复与前进式修复迁移。
--
-- ⚠️ 回滚会让 v2 写入的那些行**无法校验**：它们的哈希是按长度前缀编码算的，
-- 而回滚后的代码只会用 v1 编码重算 → 全部报 hash_mismatch。
-- 也就是说这条 down 只在「还没有任何 v2 行」时是无损的。
ALTER TABLE audit.audit_event DROP CONSTRAINT IF EXISTS audit_event_canonical_version_known;
ALTER TABLE audit.audit_event DROP COLUMN IF EXISTS canonical_version;
