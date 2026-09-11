\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
-- Input is the complete effective runtime role-scope map, not a list of role names guessed from defaults.
WITH config AS (
  SELECT :'role_scope_map_json'::jsonb AS role_map
), checked AS (
  SELECT role_map,
    1 / CASE WHEN jsonb_typeof(role_map)='object' AND role_map<>'{}'::jsonb
      THEN 1 ELSE 0 END AS valid
  FROM config
), staff AS (
  SELECT a.id, a.roles, a.disabled, a.must_change_password, a.must_enroll_totp,
    COALESCE(a.locked_until > now(), false) AS locked,
    (COALESCE(a.totp_secret_ref,'')<>'' AND a.totp_enrolled_at IS NOT NULL) AS totp_registered,
    EXISTS (
      SELECT 1 FROM unnest(a.roles) AS ar(role)
      -- unicode.IsSpace / Go strings.TrimSpace characters, not only ASCII space.
      JOIN jsonb_each(c.role_map) AS rm(role, scopes) ON rm.role=btrim(ar.role,
        U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
      CROSS JOIN LATERAL jsonb_array_elements_text(rm.scopes) AS permission(scope)
      WHERE permission.scope='finance.read'
    ) AS finance_read
  FROM core.staff_account a CROSS JOIN checked c WHERE c.valid=1
)
SELECT jsonb_build_object(
  'schema','xingmang.identity-audit.platform/v1',
  'database',current_database(), 'captured_at',now(),
  'transaction_read_only',current_setting('transaction_read_only'),
  'role_scope_map', (SELECT role_map FROM checked),
  'finance_read_total', count(*) FILTER (WHERE finance_read),
  'finance_read_totp_registered',count(*) FILTER (WHERE finance_read AND totp_registered),
  'finance_read_enabled_total',count(*) FILTER (WHERE finance_read AND NOT disabled),
  'finance_read_enabled_totp_registered',count(*) FILTER (WHERE finance_read AND NOT disabled AND totp_registered),
  'staff',COALESCE(jsonb_agg(jsonb_build_object(
    'id',id,'roles',roles,'disabled',disabled,'must_change_password',must_change_password,
    'must_enroll_totp',must_enroll_totp,'locked',locked,'totp_registered',totp_registered,
    'finance_read',finance_read) ORDER BY id),'[]'::jsonb)
) FROM staff;
COMMIT;
