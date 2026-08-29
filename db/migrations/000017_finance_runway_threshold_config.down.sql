DROP TRIGGER IF EXISTS runway_threshold_history_no_truncate ON finance.runway_threshold_history;
DROP TRIGGER IF EXISTS runway_threshold_history_no_row_mutation ON finance.runway_threshold_history;
DROP FUNCTION IF EXISTS finance.reject_runway_threshold_history_mutation();
DROP TABLE IF EXISTS finance.runway_threshold_history;
DROP TABLE IF EXISTS finance.runway_threshold_config;
