-- check_crm_member.sql — READ-ONLY check that the crm_member role (line-oa-api)
-- cannot do staff work. Run after roles_crm_member.sql (L8 §3.1):
--
--   psql -U postgres -d crm -f check_crm_member.sql
--
-- Every row must show ok = t. The script changes nothing (SELECT only).
-- A table that does not exist yet shows ok = t with note 'table missing'.

\set ON_ERROR_STOP on

WITH expected(table_name, privilege, allowed) AS (
  VALUES
    -- staff-only tables: no access at all
    ('staff_roles',              'SELECT', false),
    ('staff_roles',              'INSERT', false),
    ('uploaded_files',           'SELECT', false),
    ('uploaded_files',           'INSERT', false),
    ('member_notes',             'SELECT', false),
    ('member_tags',              'SELECT', false),
    ('member_tags',              'INSERT', false),
    -- append-only evidence
    ('point_ledgers',            'SELECT', true),
    ('point_ledgers',            'INSERT', true),
    ('point_ledgers',            'UPDATE', false),
    ('point_ledgers',            'DELETE', false),
    ('consent_records',          'INSERT', true),
    ('consent_records',          'UPDATE', false),
    ('consent_records',          'DELETE', false),
    ('code_redemption_attempts', 'INSERT', true),
    ('code_redemption_attempts', 'UPDATE', false),
    ('code_redemption_attempts', 'DELETE', false),
    ('member_tier_histories',    'UPDATE', false),
    ('member_tier_histories',    'DELETE', false),
    -- product codes: batches are read-only (no export / activate / void);
    -- codes are only marked used, never created or deleted
    ('product_code_batches',     'SELECT', true),
    ('product_code_batches',     'INSERT', false),
    ('product_code_batches',     'UPDATE', false),
    ('product_code_batches',     'DELETE', false),
    ('product_codes',            'INSERT', false),
    ('product_codes',            'DELETE', false),
    ('test_members',             'INSERT', false),
    ('test_members',             'DELETE', false),
    -- loyalty rules / settings: read-only (cannot change point rules)
    ('tiers',                    'UPDATE', false),
    ('tiers',                    'INSERT', false),
    ('earn_rules',               'UPDATE', false),
    ('earn_rules',               'INSERT', false),
    ('settings',                 'UPDATE', false),
    ('settings',                 'INSERT', false),
    ('consent_documents',        'INSERT', false),
    ('consent_documents',        'UPDATE', false),
    ('promotions',               'UPDATE', false),
    ('categories',               'UPDATE', false),
    ('reward_stock_caches',      'UPDATE', false),
    -- coupons / rewards: lock + counters only, no create / delete
    ('coupons',                  'INSERT', false),
    ('coupons',                  'DELETE', false),
    ('rewards',                  'INSERT', false),
    ('rewards',                  'DELETE', false),
    ('redemptions',              'DELETE', false),
    ('member_coupons',           'DELETE', false),
    -- members: no hard delete (anonymize runs in the backoffice)
    ('members',                  'DELETE', false),
    ('member_line_accounts',     'DELETE', false),
    -- LINE outbox is sent / retried by the backoffice worker only
    ('line_outboxes',            'INSERT', true),
    ('line_outboxes',            'UPDATE', false),
    ('line_outboxes',            'DELETE', false),
    -- PDPA requests are completed by staff only
    ('data_requests',            'INSERT', true),
    ('data_requests',            'UPDATE', false),
    ('data_requests',            'DELETE', false)
),
table_checks AS (
  SELECT
    'table crm.' || e.table_name || ' ' || e.privilege AS check_name,
    CASE
      WHEN to_regclass(format('crm.%I', e.table_name)) IS NULL THEN true
      ELSE has_table_privilege('crm_member', format('crm.%I', e.table_name), e.privilege) = e.allowed
    END AS ok,
    CASE
      WHEN to_regclass(format('crm.%I', e.table_name)) IS NULL THEN 'table missing'
      WHEN e.allowed THEN 'expected: granted'
      ELSE 'expected: denied'
    END AS note
  FROM expected e
),
truncate_checks AS (
  -- TRUNCATE must not be granted on any table
  SELECT
    'no TRUNCATE on any crm table' AS check_name,
    NOT EXISTS (
      SELECT 1 FROM pg_tables t
      WHERE t.schemaname = 'crm'
        AND has_table_privilege('crm_member', format('crm.%I', t.tablename), 'TRUNCATE')
    ) AS ok,
    'expected: denied' AS note
),
role_checks AS (
  SELECT 'role is not superuser / createdb / createrole / bypassrls' AS check_name,
         NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolbypassrls) AS ok,
         'role attributes' AS note
  FROM pg_roles r WHERE r.rolname = 'crm_member'
  UNION ALL
  SELECT 'no CREATE on schema crm',
         NOT has_schema_privilege('crm_member', 'crm', 'CREATE'),
         'cannot create or migrate objects'
  UNION ALL
  SELECT 'does not own any crm table',
         NOT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'crm' AND tableowner = 'crm_member'),
         'owner can bypass grants'
  UNION ALL
  SELECT 'not a member of another role',
         NOT EXISTS (
           SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.member
           WHERE r.rolname = 'crm_member'
         ),
         'inherited grants'
  UNION ALL
  SELECT 'no CREATE on database ' || current_database(),
         NOT has_database_privilege('crm_member', current_database(), 'CREATE'),
         'cannot create schemas'
)
SELECT check_name, ok, note FROM role_checks
UNION ALL SELECT check_name, ok, note FROM truncate_checks
UNION ALL SELECT check_name, ok, note FROM table_checks
ORDER BY ok, check_name;  -- failures (ok = f) sort first; any f row is a privilege leak
