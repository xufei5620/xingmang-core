-- AUD2 is forward-only at runtime. This down file exists for disposable schema rebuilds;
-- it never runs as an application rollback and never touches audit.audit_event.
DROP TRIGGER IF EXISTS archive_terminal_receipt_no_truncate ON audit.archive_terminal_receipt;
DROP TRIGGER IF EXISTS archive_terminal_receipt_no_row_mutation ON audit.archive_terminal_receipt;
DROP TRIGGER IF EXISTS archive_put_receipt_no_truncate ON audit.archive_put_receipt;
DROP TRIGGER IF EXISTS archive_put_receipt_no_row_mutation ON audit.archive_put_receipt;
DROP TRIGGER IF EXISTS archive_operation_intent_no_truncate ON audit.archive_operation_intent;
DROP TRIGGER IF EXISTS archive_operation_intent_no_row_mutation ON audit.archive_operation_intent;
DROP TRIGGER IF EXISTS archive_segment_no_truncate ON audit.archive_segment;
DROP TRIGGER IF EXISTS archive_segment_no_row_mutation ON audit.archive_segment;
DROP FUNCTION IF EXISTS audit.reject_archive_catalog_mutation();
DROP TABLE IF EXISTS audit.archive_terminal_receipt;
DROP TABLE IF EXISTS audit.archive_put_receipt;
DROP TABLE IF EXISTS audit.archive_operation_intent;
DROP TABLE IF EXISTS audit.archive_segment;
