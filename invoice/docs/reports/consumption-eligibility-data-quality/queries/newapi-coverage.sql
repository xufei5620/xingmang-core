-- Aggregate-only production evidence. Log type 2 is the New API consumption
-- event type. No user, order, email, content or credential row is selected.
SELECT
  (SELECT count(*) FROM users) AS users,
  (SELECT count(*) FROM top_ups) AS top_ups,
  (SELECT count(*) FROM top_ups WHERE status='success') AS successful_top_ups,
  (SELECT count(*) FROM logs WHERE type=2) AS consumption_logs,
  (SELECT count(*) FROM logs
    WHERE type=2 AND created_at < (SELECT min(create_time) FROM top_ups)
  ) AS consumption_logs_before_first_topup,
  (SELECT coalesce(sum(used_quota),0) FROM users) AS users_used_quota,
  (SELECT coalesce(sum(quota),0) FROM logs WHERE type=2) AS consumption_log_quota,
  (SELECT coalesce(sum(quota),0) FROM quota_data) AS quota_rollup_total,
  (SELECT min(created_at) FROM users) AS first_user_epoch,
  (SELECT min(created_at) FROM logs WHERE type=2) AS first_consumption_epoch,
  (SELECT min(create_time) FROM top_ups) AS first_topup_epoch;
