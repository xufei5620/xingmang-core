-- Aggregate-only production evidence. No user, order, email or credential row
-- is selected.
SELECT
  (SELECT count(*) FROM users) AS users,
  (SELECT count(*) FROM payment_orders) AS payment_orders,
  (SELECT count(*) FROM payment_orders
    WHERE status IN ('COMPLETED','REFUND_REQUESTED')) AS completed_payments,
  (SELECT count(*) FROM usage_logs) AS retained_usage_logs,
  (SELECT count(*) FROM billing_usage_entries) AS allocation_entries,
  (SELECT count(*) FROM payment_orders
    WHERE status IN ('COMPLETED','REFUND_REQUESTED')
      AND completed_at < (SELECT min(created_at) FROM usage_logs)
  ) AS completed_payments_before_usage_retention,
  (SELECT min(created_at) FROM users) AS first_user_at,
  (SELECT min(created_at) FROM payment_orders) AS first_payment_at,
  (SELECT min(created_at) FROM usage_logs) AS first_retained_usage_at;
