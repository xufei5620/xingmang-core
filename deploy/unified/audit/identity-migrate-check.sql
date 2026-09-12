\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
-- Exact owner-supplied tuples. No email/name matching or decryption.
WITH inputs AS (
  SELECT :'from_issuer'::text AS from_issuer, :'from_subject'::text AS from_subject,
         :'to_issuer'::text AS to_issuer, :'to_subject'::text AS to_subject
), matched AS (
  SELECT u.id, u.status, u.platform, u.created_at,
    CASE WHEN (u.oidc_issuer,u.oidc_subject)=(i.from_issuer,i.from_subject) THEN 'source' ELSE 'target' END AS side,
    encode(sha256(convert_to(u.oidc_issuer || E'\n' || u.oidc_subject,'UTF8')),'hex') AS identity_hash,
    (octet_length(u.email_ciphertext)>0) IS TRUE AS email_aad_rebind_needed
  FROM invoice_users u CROSS JOIN inputs i
  WHERE (u.oidc_issuer,u.oidc_subject)=(i.from_issuer,i.from_subject)
     OR (u.oidc_issuer,u.oidc_subject)=(i.to_issuer,i.to_subject)
), summaries AS (
  SELECT m.*,
    (SELECT count(*) FROM auth_sessions a WHERE a.invoice_user_id=m.id) AS sessions_total,
    (SELECT count(*) FROM auth_sessions a WHERE a.invoice_user_id=m.id AND a.revoked_at IS NULL) AS sessions_live,
    (SELECT count(*) FROM external_accounts a WHERE a.invoice_user_id=m.id) AS external_accounts,
    (SELECT count(*) FROM invoice_profiles p WHERE p.invoice_user_id=m.id) AS profiles,
    (SELECT count(*) FROM funding_lots f WHERE f.invoice_user_id=m.id) AS funding_lots,
    (SELECT count(*) FROM invoice_requests r WHERE r.invoice_user_id=m.id) AS requests,
    (SELECT count(*) FROM invoice_requests r WHERE r.reviewed_by=m.id) AS reviewed_requests,
    (SELECT count(*) FROM invoice_requests r WHERE r.issued_by=m.id) AS issued_requests,
    (SELECT count(*) FROM invoice_documents d WHERE d.uploaded_by=m.id) AS uploaded_documents
  FROM matched m
), witnesses AS (
  SELECT a.id,a.object_id,a.before_hash,a.after_hash,a.created_at
  FROM audit_events a CROSS JOIN inputs i
  WHERE a.object_type='invoice_user' AND a.action='identity.oidc_binding.migrated'
    AND a.object_id IN (SELECT id::text FROM matched)
    AND a.before_hash=encode(sha256(convert_to(i.from_issuer || E'\n' || i.from_subject,'UTF8')),'hex')
    AND a.after_hash=encode(sha256(convert_to(i.to_issuer || E'\n' || i.to_subject,'UTF8')),'hex')
)
SELECT jsonb_build_object(
  'schema','xingmang.identity-migrate.readonly/v1',
  'database',current_database(),'captured_at',now(),
  'transaction_read_only',current_setting('transaction_read_only'),
  'source_count',(SELECT count(*) FROM matched WHERE side='source'),
  'target_count',(SELECT count(*) FROM matched WHERE side='target'),
  'rows',COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.side,s.id) FROM summaries s),'[]'::jsonb),
  'migration_witnesses',COALESCE((SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM witnesses w),'[]'::jsonb),
  'writes_performed',false
);
COMMIT;
