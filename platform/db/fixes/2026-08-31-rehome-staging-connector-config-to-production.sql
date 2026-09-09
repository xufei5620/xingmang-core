-- 变更单：console.solov.cc 切生产后，把 staging 时期通过后台 Action 录入的
-- 凭据登记（core.credential_ref）与接入模式（core.connector_config）改挂到
-- production 环境。
--
-- 背景：2026-08-30 生产切换保留了原数据库卷；credential_ref 以 ref 为主键、
-- 一条引用只属于一个环境（store.write 拒绝跨环境改写），而凭据文件卷
-- /run/xm/secrets 不分环境。结果是后台在 production 下重新粘贴同一引用会被
-- 拒绝（EXECUTION_FAILED ← ErrEnvironmentMismatch）。这些行的值就是运营在
-- 后台亲手录入的（endpoint / allowlist / 凭据引用 / mode=real），文件指纹未变，
-- 改挂环境不改变任何业务事实。
--
-- 性质：Platform Lifecycle Operation（获批数据修复，宪法 2/3 条例外）。
-- 审批：用户在会话中亲自批准后，由验收线在服务器执行；执行记录写入
-- docs/handoffs/ACCEPTANCE-LOG.md。只允许对下列 4 行执行一次；全部在一个
-- 事务内，任一计数不符即回滚。
BEGIN;

UPDATE core.credential_ref
   SET environment = 'production'
 WHERE environment = 'staging'
   AND ref IN ('secret://sub2api-prod/read-token', 'secret://newapi/readonly-token');

UPDATE core.connector_config
   SET environment = 'production'
 WHERE environment = 'staging'
   AND platform IN ('sub2api', 'newapi');

DO $$
DECLARE
    n_cred int;
    n_conn int;
BEGIN
    SELECT count(*) INTO n_cred FROM core.credential_ref
     WHERE environment = 'production'
       AND ref IN ('secret://sub2api-prod/read-token', 'secret://newapi/readonly-token');
    SELECT count(*) INTO n_conn FROM core.connector_config
     WHERE environment = 'production' AND platform IN ('sub2api', 'newapi');
    IF n_cred <> 2 OR n_conn <> 2 THEN
        RAISE EXCEPTION 'rehome 计数不符: credential_ref=% connector_config=%（期望各 2）', n_cred, n_conn;
    END IF;
END $$;

COMMIT;
