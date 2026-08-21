-- Retained-row volume only. The first observed day marks the current
-- historical floor; absence before that date must not be interpreted as zero
-- real usage.
SELECT date_trunc('day',created_at)::date AS usage_day,
       count(*)::bigint AS retained_rows
FROM usage_logs
WHERE created_at < date_trunc('day',now())
GROUP BY date_trunc('day',created_at)::date
ORDER BY usage_day;
