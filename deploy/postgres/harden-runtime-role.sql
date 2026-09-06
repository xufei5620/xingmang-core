BEGIN;

ALTER ROLE invoice_app
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS
  CONNECTION LIMIT 20;
ALTER ROLE invoice_app SET statement_timeout='15s';
ALTER ROLE invoice_app SET lock_timeout='5s';
ALTER ROLE invoice_app SET idle_in_transaction_session_timeout='15s';
SELECT format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database())\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO invoice_app', current_database())\gexec

REVOKE CREATE ON SCHEMA public FROM PUBLIC, invoice_app;
GRANT USAGE ON SCHEMA public TO invoice_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO invoice_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO invoice_app;

REVOKE ALL ON TABLE schema_migrations FROM invoice_app;
GRANT SELECT ON TABLE schema_migrations TO invoice_app;

REVOKE ALL ON TABLE invoice_eligibility_policy FROM invoice_app;
GRANT SELECT ON TABLE invoice_eligibility_policy TO invoice_app;

REVOKE UPDATE, DELETE, TRUNCATE ON TABLE audit_events FROM invoice_app;
GRANT SELECT, INSERT ON TABLE audit_events TO invoice_app;

REVOKE UPDATE, DELETE, TRUNCATE ON TABLE oidc_backchannel_logout_events FROM invoice_app;
GRANT SELECT, INSERT ON TABLE oidc_backchannel_logout_events TO invoice_app;

-- console_assertion_nonces (migration 0018) is the single-use judge for
-- console assertions: PostgresConsoleAssertionNonceStore.ConsumeNonce claims
-- a nonce with INSERT ... ON CONFLICT DO NOTHING (which needs no UPDATE
-- privilege, unlike DO UPDATE) and reads nothing back, and DeleteExpired
-- sweeps rows an hour past expiry. DELETE therefore stays granted -- the one
-- difference from oidc_backchannel_logout_events above, whose retention job
-- runs as the owner. UPDATE would let a claimed nonce be rewritten and
-- TRUNCATE would clear every claim at once, so both are revoked: a replay
-- must never become possible by editing this table through the runtime role.
REVOKE UPDATE, TRUNCATE ON TABLE console_assertion_nonces FROM invoice_app;
GRANT SELECT, INSERT, DELETE ON TABLE console_assertion_nonces TO invoice_app;

-- invoice_notice_outbox (migration 0027) follows email_outbox: the worker
-- claims with UPDATE and settles with UPDATE, the submission transaction
-- INSERTs, nothing ever deletes a notice row -- a delivered row is the audit
-- trail of "that notice went out". notice_webhook_setting (0028) keeps the
-- default SELECT/INSERT/UPDATE/DELETE because ClearNoticeWebhook deletes the
-- singleton row on purpose.
REVOKE DELETE, TRUNCATE ON TABLE
  source_events,
  funding_lots,
  invoice_requests,
  invoice_allocations,
  invoice_documents,
  email_outbox,
  invoice_notice_outbox,
  source_ingest_state,
  source_ingest_batches,
  source_ingest_events,
  payment_candidate_reviews,
  refund_cases
FROM invoice_app;

REVOKE UPDATE, DELETE, TRUNCATE ON TABLE
  source_cutover_manifests,
	source_events,
	source_ingest_batches,
	payment_candidate_reviews,
  source_usage_events,
  source_credit_events,
  balance_reconciliation_checkpoints,
  balance_checkpoint_evaluations,
  balance_carry_forward_proofs,
  balance_carry_forward_evaluations
FROM invoice_app;
GRANT SELECT, INSERT ON TABLE
  source_cutover_manifests,
	source_events,
	source_ingest_batches,
	payment_candidate_reviews,
  source_usage_events,
  source_credit_events,
  balance_reconciliation_checkpoints,
  balance_checkpoint_evaluations,
  balance_carry_forward_proofs,
  balance_carry_forward_evaluations
TO invoice_app;

REVOKE UPDATE, TRUNCATE ON TABLE consumption_allocations FROM invoice_app;
GRANT SELECT, INSERT, DELETE ON TABLE consumption_allocations TO invoice_app;

REVOKE UPDATE, DELETE, TRUNCATE ON TABLE source_economic_scan_cycle_events FROM invoice_app;
GRANT SELECT, INSERT ON TABLE source_economic_scan_cycle_events TO invoice_app;

REVOKE DELETE, TRUNCATE ON TABLE
  source_economic_stream_watermarks,
  source_account_eligibility_state,
  source_account_stream_watermarks,
	funding_lot_consumption_state,
	eligibility_freezes,
	source_economic_scan_cycles
FROM invoice_app;

COMMIT;
