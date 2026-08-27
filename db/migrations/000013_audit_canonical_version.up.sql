-- XM-R009 审计链 canonical 编码版本化（Codex 冷审 #35，Issue #75）。forward-only（规格 §5.7）。
--
-- ── 修的是什么 ────────────────────────────────────────────────────────────
-- v1 的 canonical 是 `key=value\n` 直接拼接，**值不转义**。于是一个能写进
-- 任意文本的字段（reason 是用户填的）可以伪造出另一个字段：
--
--   事件 A：reason = "巡检" + 换行 + "approval_id=APR-1"，approval_id = ""
--   事件 B：reason = "巡检"，                             approval_id = "APR-1"
--
-- 两者的 canonical 字节序列**完全相同** → 同一个 SHA256 → 链上无法区分。
-- 一个未经审批的操作因此可以伪装成已审批的那一条。
--
-- ── 为什么要加这一列 ──────────────────────────────────────────────────────
-- 换编码会让既有链上每一条的重算哈希都对不上。不能就地改：那等于把历史链
-- 全部判为「已被篡改」——一次修复把唯一的证据链毁掉，比原来的漏洞更糟。
--
-- 所以逐行记住「这一条是用哪版编码算的哈希」，校验时按版本选编码：
--   1 = 上线时的 `key=value\n` 拼接（**冻结，永不再改**，只用于校验历史行）
--   2 = 长度前缀 `key=<字节数>:value\n`（新写入一律用它）
--
-- 这一列**不进 canonical**，所以给历史行加上它不会改变任何既有哈希。
-- 校验时它是输入，不是被校验的内容——一个改了 canonical_version 的攻击者
-- 只会让那一行用错编码、重算哈希对不上，当场暴露为 hash_mismatch。
--
-- ── 为什么 DEFAULT 1 而不是回填 ───────────────────────────────────────────
-- audit.audit_event 上有 `audit_event_no_update ... DO INSTEAD NOTHING`
-- （迁移 000003）：任何 UPDATE 都是**静默空操作**。写一条 UPDATE 回填会
-- 「成功」执行、一行不动，然后所有历史行按 v2 校验、全部报 hash_mismatch。
-- 列默认值由 DDL 提供，不经 DML，不受那条规则影响。
ALTER TABLE audit.audit_event
    ADD COLUMN canonical_version smallint NOT NULL DEFAULT 1;

-- 只认已知版本：写进一个 3 会让校验端找不到对应编码而整条链无法验证，
-- 那是一种「看起来存下来了、实际上验不了」的坏行——库层直接拒掉。
-- 新增版本时同步放宽这条约束（前进式迁移）。
ALTER TABLE audit.audit_event
    ADD CONSTRAINT audit_event_canonical_version_known
        CHECK (canonical_version IN (1, 2));

COMMENT ON COLUMN audit.audit_event.canonical_version IS
    '算 event_hash 时用的 canonical 编码版本：1=上线时的未转义拼接（冻结，仅校验历史行）；2=长度前缀。本列不参与哈希。';
