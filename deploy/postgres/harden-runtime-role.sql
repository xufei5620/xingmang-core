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

REVOKE DELETE, TRUNCATE ON TABLE
  source_events,
  funding_lots,
  invoice_requests,
  invoice_allocations,
  invoice_documents,
  email_outbox,
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
