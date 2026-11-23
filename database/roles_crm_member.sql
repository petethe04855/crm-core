-- crm_member — least-privilege role for line-oa-api (the public LIFF API).
--
-- Run once after backoffice-api has created the CRM schema, and re-run after a
-- migration adds a table the customer side needs (the grant list below skips
-- tables that do not exist yet).
--
--   psql -U postgres -d crm -v member_password='<real password>' -f roles_crm_member.sql
--
-- Never commit the real password. The API reads it from CRM_DB_PASSWORD.
--
-- Rules:
--   * line-oa-api never migrates and never creates objects.
--   * Staff-only tables (staff_roles, uploaded_files, member_notes, member_tags)
--     are deliberately absent: crm_member has no access to them.
--   * point_ledgers is append-only: SELECT/INSERT only, no UPDATE/DELETE.
--   * SELECT ... FOR UPDATE needs the UPDATE privilege on Postgres, so a table
--     the usecases lock (coupons and rewards when claiming/redeeming) needs
--     UPDATE even if the customer never edits it; their counters
--     (claimed_count, redeemed_count) are updated in the same transaction.
--   * There are no ALTER DEFAULT PRIVILEGES: every new customer-facing table
--     must be added to the list below, so access is granted explicitly.

\set ON_ERROR_STOP on

\if :{?member_password}
\else
  \echo 'ERROR: pass the role password with -v member_password=...'
  \quit
\endif

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_member') THEN
    CREATE ROLE crm_member;
  END IF;
END
$$;

ALTER ROLE crm_member WITH
  LOGIN
  NOSUPERUSER
  NOCREATEDB
  NOCREATEROLE
  NOINHERIT
  PASSWORD :'member_password';

-- The database name defaults to "crm"; adjust if CRM_DB_NAME differs.
GRANT CONNECT ON DATABASE crm TO crm_member;
GRANT USAGE ON SCHEMA crm TO crm_member;

-- Reset then re-grant, so re-running this script always converges on the list.
REVOKE ALL ON ALL TABLES IN SCHEMA crm FROM crm_member;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA crm FROM crm_member;

DO $$
DECLARE
  t record;
BEGIN
  FOR t IN
    SELECT *
    FROM (VALUES
      -- L0 / existing tables
      ('members',               'SELECT, INSERT, UPDATE'),
      ('member_line_accounts',  'SELECT, INSERT, UPDATE'),
      ('tiers',                 'SELECT'),
      ('point_ledgers',         'SELECT, INSERT'),
      ('point_lots',            'SELECT, INSERT, UPDATE'),
      ('settings',              'SELECT'),
      ('promotions',            'SELECT'),
      ('coupons',               'SELECT, UPDATE'),   -- FOR UPDATE + claimed_count
      ('member_coupons',        'SELECT, INSERT, UPDATE'),
      ('categories',            'SELECT'),
      ('rewards',               'SELECT, UPDATE'),   -- FOR UPDATE + redeemed_count
      ('reward_stock_caches',   'SELECT'),
      ('redemptions',           'SELECT, INSERT, UPDATE'),
      ('earn_rules',            'SELECT'),
      -- L1: consent + email OTP
      ('consent_documents',     'SELECT'),
      ('consent_records',       'SELECT, INSERT'),
      ('otp_requests',          'SELECT, INSERT, UPDATE'),
      -- L3: profile addresses
      ('member_addresses',      'SELECT, INSERT, UPDATE, DELETE'),
      -- L2: signup writes the first tier history row (L5 reads it)
      ('member_tier_histories', 'SELECT, INSERT'),
      -- L4: product codes (the full code is never readable after export)
      ('product_code_batches',  'SELECT'),
      ('product_codes',         'SELECT, UPDATE'),
      ('code_redemption_attempts', 'SELECT, INSERT'),
      ('test_members',          'SELECT'),
      -- L6: LINE messaging
      ('line_outboxes',         'SELECT, INSERT'),  -- INSERT ... RETURNING id needs SELECT
      ('line_webhook_events',   'SELECT, INSERT, UPDATE'),
      -- L7: notification settings + PDPA requests (staff complete them)
      ('member_notification_prefs', 'SELECT, INSERT, UPDATE'),
      ('data_requests',         'SELECT, INSERT')
    ) AS x(table_name, privileges)
  LOOP
    IF to_regclass(format('crm.%I', t.table_name)) IS NOT NULL THEN
      EXECUTE format('GRANT %s ON crm.%I TO crm_member', t.privileges, t.table_name);
    END IF;
  END LOOP;
END
$$;

-- Inserts need the backing sequences (member ids, ledger ids, ...).
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA crm TO crm_member;
