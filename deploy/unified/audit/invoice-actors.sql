\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
-- No email, profile, evidence ciphertext, document payload, token or session identifier is selected.
WITH actor_refs AS (
  SELECT reviewed_by AS id,'invoice_requests.reviewed_by' AS source FROM invoice_requests WHERE reviewed_by IS NOT NULL
  UNION ALL SELECT issued_by,'invoice_requests.issued_by' FROM invoice_requests WHERE issued_by IS NOT NULL
  UNION ALL SELECT uploaded_by,'invoice_documents.uploaded_by' FROM invoice_documents
  UNION ALL SELECT admin_id,'payment_candidate_reviews.admin_id' FROM payment_candidate_reviews
  UNION ALL SELECT proposed_by,'payment_candidate_decisions.proposed_by' FROM payment_candidate_decisions
  UNION ALL SELECT approved_by,'payment_candidate_decisions.approved_by' FROM payment_candidate_decisions WHERE approved_by IS NOT NULL
  UNION ALL SELECT resolved_by,'eligibility_freezes.resolved_by' FROM eligibility_freezes WHERE resolved_by IS NOT NULL
  UNION ALL SELECT u.id,'audit_events.admin_actor'
    FROM audit_events a JOIN invoice_users u ON a.actor_id=u.id::text WHERE a.actor_type='admin'
), grouped_refs AS (
  SELECT id,source,count(*) AS count FROM actor_refs GROUP BY id,source
), identities AS (
  SELECT u.id,u.oidc_issuer AS issuer,u.oidc_subject AS subject,u.status,u.platform,
    COALESCE((SELECT sum(count) FROM grouped_refs r WHERE r.id=u.id),0) AS operation_refs
  FROM invoice_users u
  WHERE u.platform IS NULL OR EXISTS (SELECT 1 FROM grouped_refs r WHERE r.id=u.id)
), migrations AS (
  SELECT id,object_id,before_hash,after_hash,created_at
  FROM audit_events
  WHERE action='identity.oidc_binding.migrated' AND object_type='invoice_user'
)
SELECT jsonb_build_object(
  'schema','xingmang.identity-audit.invoice/v1','database',current_database(),
  'captured_at',now(),'transaction_read_only',current_setting('transaction_read_only'),
  -- Non-user operator IDs and orphan admin IDs need owner review; do not emit raw actor strings.
  'unresolved_admin_audit_actors',(SELECT count(*) FROM audit_events a
    WHERE a.actor_type='admin' AND NOT EXISTS(SELECT 1 FROM invoice_users u WHERE u.id::text=a.actor_id)),
  'identities',COALESCE((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM identities i),'[]'::jsonb),
  'operation_refs',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id,r.source) FROM grouped_refs r),'[]'::jsonb),
  'migration_records',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM migrations m),'[]'::jsonb)
);
COMMIT;
