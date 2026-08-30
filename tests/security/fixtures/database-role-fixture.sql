-- DBR1 disposable role/ACL fixture.
--
-- This file is executed byte-for-byte by scripts/test-database-roles.ps1 as
-- the bootstrap superuser inside a freshly-created PostgreSQL 18 database.
-- Infrastructure names (Compose project, volume and database) are randomised
-- by the harness; role/object names below intentionally stay fixed so the
-- policy is exercised against the production shape.  No password or DSN is
-- stored here.

\set ON_ERROR_STOP on
SET client_min_messages = warning;
SET search_path = pg_catalog, public;

-- Capability roles are NOLOGIN.  Login identities are deliberately separate
-- and receive access only through their capability membership.
CREATE ROLE xm_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD NULL;
CREATE ROLE xm_api_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE xm_worker_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE xm_lifecycle_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE xm_ops_read NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE xm_backup_read NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE xm_api_a LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD NULL;
CREATE ROLE xm_worker_a LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD NULL;
CREATE ROLE xm_lifecycle_a LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD NULL;
CREATE ROLE xm_ops_a LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD NULL;
CREATE ROLE xm_backup_a LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD NULL;

ALTER ROLE xm_ops_a SET default_transaction_read_only = on;
ALTER ROLE xm_backup_a SET default_transaction_read_only = on;

GRANT xm_api_runtime TO xm_api_a WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT xm_worker_runtime TO xm_worker_a WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT xm_lifecycle_runtime TO xm_lifecycle_a WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT xm_ops_read TO xm_ops_a WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
GRANT xm_backup_read TO xm_backup_a WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;

-- The database is random per run; resolve it from current_database() instead
-- of interpolating a caller-controlled identifier into this fixture.
SELECT format('ALTER DATABASE %I OWNER TO xm_migrator', current_database()) \gexec
SELECT format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format(
    'GRANT CONNECT ON DATABASE %I TO xm_migrator, xm_api_runtime, xm_worker_runtime, xm_lifecycle_runtime, xm_ops_read, xm_backup_read',
    current_database()
) \gexec

CREATE SCHEMA core;
CREATE SCHEMA action;
CREATE SCHEMA audit;
CREATE SCHEMA ops;
CREATE SCHEMA alerts;
CREATE SCHEMA finance;

-- Keep public owned by PostgreSQL's virtual database-owner role.  Application
-- roles get no public schema usage; worker receives it only for River objects.
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO xm_worker_runtime;
GRANT USAGE ON SCHEMA core, ops, alerts, finance TO xm_api_runtime;
GRANT USAGE ON SCHEMA core, ops, alerts, finance, public TO xm_worker_runtime;
GRANT USAGE ON SCHEMA core, ops, alerts, finance TO xm_lifecycle_runtime, xm_ops_read, xm_backup_read;

CREATE TABLE core.environment (
    id text PRIMARY KEY,
    description text NOT NULL DEFAULT ''
);
INSERT INTO core.environment (id, description) VALUES ('staging', 'DBR1 disposable fixture');

CREATE TABLE core.service (
    id uuid PRIMARY KEY,
    service_type text NOT NULL,
    instance_id text NOT NULL,
    environment text NOT NULL REFERENCES core.environment(id),
    endpoint text NOT NULL,
    owner text NOT NULL,
    status text NOT NULL
);
CREATE TABLE core.connector (
    id uuid PRIMARY KEY,
    key text NOT NULL,
    version text NOT NULL
);
CREATE TABLE core.connection (
    id uuid PRIMARY KEY,
    connector_id uuid NOT NULL REFERENCES core.connector(id),
    service_id uuid NOT NULL REFERENCES core.service(id),
    environment text NOT NULL REFERENCES core.environment(id),
    credential_ref text NOT NULL,
    status text NOT NULL
);

CREATE TABLE action.action_run (
    id uuid PRIMARY KEY,
    action_id text NOT NULL,
    principal_id text NOT NULL,
    environment text NOT NULL REFERENCES core.environment(id),
    status text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now()
);
CREATE RULE action_run_no_delete AS ON DELETE TO action.action_run DO INSTEAD NOTHING;
CREATE RULE action_run_no_update AS ON UPDATE TO action.action_run DO INSTEAD NOTHING;

CREATE TABLE audit.audit_event (
    id uuid PRIMARY KEY,
    sequence bigint NOT NULL,
    principal_id text NOT NULL,
    environment text NOT NULL REFERENCES core.environment(id),
    result text NOT NULL,
    prev_hash text NOT NULL,
    event_hash text NOT NULL
);
CREATE RULE audit_event_no_delete AS ON DELETE TO audit.audit_event DO INSTEAD NOTHING;
CREATE RULE audit_event_no_update AS ON UPDATE TO audit.audit_event DO INSTEAD NOTHING;

CREATE TABLE audit.chain_root (
    id uuid PRIMARY KEY,
    from_sequence bigint NOT NULL,
    to_sequence bigint NOT NULL,
    root_hash text NOT NULL,
    signature text NOT NULL,
    key_id text NOT NULL,
    exported_at timestamptz,
    export_target text NOT NULL DEFAULT ''
);

CREATE OR REPLACE FUNCTION audit.chain_root_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $body$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.from_sequence IS DISTINCT FROM OLD.from_sequence
       OR NEW.to_sequence IS DISTINCT FROM OLD.to_sequence
       OR NEW.root_hash IS DISTINCT FROM OLD.root_hash
       OR NEW.signature IS DISTINCT FROM OLD.signature
       OR NEW.key_id IS DISTINCT FROM OLD.key_id THEN
        RAISE EXCEPTION 'chain_root protected columns are immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$body$;
CREATE TRIGGER chain_root_immutable
    BEFORE UPDATE ON audit.chain_root
    FOR EACH ROW EXECUTE FUNCTION audit.chain_root_guard();

CREATE TABLE ops.metric_observation (
    id uuid PRIMARY KEY,
    metric_key text NOT NULL,
    environment text NOT NULL REFERENCES core.environment(id),
    status text NOT NULL,
    value_json jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE SEQUENCE ops.metric_observation_sample_id_seq;
CREATE TABLE ops.metric_observation_sample (
    id bigint NOT NULL DEFAULT nextval('ops.metric_observation_sample_id_seq'),
    metric_key text NOT NULL,
    environment text NOT NULL REFERENCES core.environment(id),
    status text NOT NULL,
    PRIMARY KEY (id)
);
ALTER SEQUENCE ops.metric_observation_sample_id_seq OWNED BY ops.metric_observation_sample.id;

CREATE TABLE alerts.alert (
    id uuid PRIMARY KEY,
    rule_key text NOT NULL,
    status text NOT NULL
);
CREATE TABLE alerts.alert_silence (
    id uuid PRIMARY KEY,
    rule_key text NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL
);

CREATE TABLE finance.upstream_account (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    balance_minor bigint NOT NULL DEFAULT 0
);
CREATE SEQUENCE finance.upstream_account_id_seq;
ALTER SEQUENCE finance.upstream_account_id_seq OWNED BY NONE;
CREATE TABLE finance.profit_daily (
    id uuid PRIMARY KEY,
    day date NOT NULL,
    revenue_minor bigint NOT NULL,
    cost_minor bigint NOT NULL
);

-- Minimal River shape: worker needs table DML, sequence USAGE and enum USAGE;
-- PUBLIC must not receive the type privilege.
CREATE TYPE public.river_job_state AS ENUM ('available', 'running', 'completed');
CREATE TABLE public.river_job (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    state public.river_job_state NOT NULL,
    kind text NOT NULL
);
CREATE SEQUENCE public.river_probe_seq;
CREATE OR REPLACE FUNCTION public.river_probe() RETURNS integer
LANGUAGE sql AS $$ SELECT 1 $$;

-- Ownership is explicit and bounded to this fixture's objects.  public stays
-- pg_database_owner; no REASSIGN OWNED is ever used.
ALTER SCHEMA core OWNER TO xm_migrator;
ALTER SCHEMA action OWNER TO xm_migrator;
ALTER SCHEMA audit OWNER TO xm_migrator;
ALTER SCHEMA ops OWNER TO xm_migrator;
ALTER SCHEMA alerts OWNER TO xm_migrator;
ALTER SCHEMA finance OWNER TO xm_migrator;
ALTER TABLE core.environment, core.service, core.connector, core.connection OWNER TO xm_migrator;
ALTER TABLE action.action_run, audit.audit_event, audit.chain_root OWNER TO xm_migrator;
ALTER TABLE ops.metric_observation, ops.metric_observation_sample OWNER TO xm_migrator;
ALTER TABLE alerts.alert, alerts.alert_silence OWNER TO xm_migrator;
ALTER TABLE finance.upstream_account, finance.profit_daily OWNER TO xm_migrator;
ALTER TABLE public.river_job OWNER TO xm_migrator;
ALTER SEQUENCE ops.metric_observation_sample_id_seq OWNER TO xm_migrator;
ALTER SEQUENCE finance.upstream_account_id_seq OWNER TO xm_migrator;
ALTER SEQUENCE public.river_probe_seq OWNER TO xm_migrator;
ALTER FUNCTION audit.chain_root_guard() OWNER TO xm_migrator;
ALTER FUNCTION public.river_probe() OWNER TO xm_migrator;
ALTER TYPE public.river_job_state OWNER TO xm_migrator;

-- Existing object grants: every capability is explicit; no PUBLIC defaults.
GRANT SELECT ON core.environment, core.service, core.connector, core.connection TO xm_api_runtime;
GRANT INSERT, UPDATE ON core.service, core.connection TO xm_api_runtime;
GRANT INSERT ON core.connector TO xm_api_runtime;
GRANT SELECT, INSERT ON core.environment TO xm_lifecycle_runtime;
GRANT SELECT ON core.environment, core.service, core.connector, core.connection TO xm_lifecycle_runtime, xm_ops_read, xm_backup_read;

GRANT INSERT ON action.action_run TO xm_api_runtime;
GRANT SELECT, INSERT ON audit.audit_event TO xm_api_runtime;
GRANT SELECT, INSERT ON audit.chain_root TO xm_worker_runtime;
GRANT UPDATE (exported_at, export_target) ON audit.chain_root TO xm_worker_runtime;
GRANT SELECT ON audit.audit_event, audit.chain_root TO xm_lifecycle_runtime, xm_ops_read, xm_backup_read;

GRANT SELECT, INSERT, UPDATE ON ops.metric_observation TO xm_worker_runtime;
GRANT SELECT, INSERT, DELETE ON ops.metric_observation_sample TO xm_worker_runtime;
GRANT USAGE ON SEQUENCE ops.metric_observation_sample_id_seq TO xm_worker_runtime;
GRANT SELECT ON ops.metric_observation, ops.metric_observation_sample TO xm_api_runtime, xm_lifecycle_runtime, xm_ops_read, xm_backup_read;
GRANT SELECT ON SEQUENCE ops.metric_observation_sample_id_seq TO xm_ops_read, xm_backup_read;

GRANT SELECT, UPDATE ON alerts.alert TO xm_api_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON alerts.alert TO xm_worker_runtime;
GRANT SELECT, INSERT ON alerts.alert_silence TO xm_api_runtime;
GRANT SELECT ON alerts.alert, alerts.alert_silence TO xm_lifecycle_runtime, xm_ops_read, xm_backup_read;

GRANT SELECT, INSERT, UPDATE ON finance.upstream_account, finance.profit_daily TO xm_api_runtime;
GRANT SELECT ON finance.upstream_account, finance.profit_daily TO xm_worker_runtime, xm_lifecycle_runtime, xm_ops_read, xm_backup_read;
GRANT SELECT ON SEQUENCE finance.upstream_account_id_seq TO xm_ops_read, xm_backup_read;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.river_job TO xm_worker_runtime;
GRANT USAGE ON SEQUENCE public.river_job_id_seq TO xm_worker_runtime;
GRANT USAGE ON TYPE public.river_job_state TO xm_worker_runtime;
GRANT EXECUTE ON FUNCTION public.river_probe() TO xm_worker_runtime;
REVOKE ALL ON TABLE public.river_job FROM PUBLIC;
REVOKE ALL ON TYPE public.river_job_state FROM PUBLIC;
REVOKE ALL ON FUNCTION public.river_probe() FROM PUBLIC;

-- Future objects created by the migrator default to deny for custom runtime
-- roles; River/public has the narrowly-scoped worker exception.
ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator IN SCHEMA core, action, audit, ops, alerts, finance
    REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator IN SCHEMA core, action, audit, ops, alerts, finance
    REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator IN SCHEMA core, action, audit, ops, alerts, finance
    REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator IN SCHEMA core, action, audit, ops, alerts, finance
    REVOKE USAGE ON TYPES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator IN SCHEMA public
    REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator IN SCHEMA public
    REVOKE USAGE ON TYPES FROM PUBLIC;

-- Seed one row used by positive/negative probes.
INSERT INTO core.environment (id, description) VALUES ('development', 'DBR1 probe') ON CONFLICT DO NOTHING;
INSERT INTO audit.chain_root (id, from_sequence, to_sequence, root_hash, signature, key_id)
VALUES ('00000000-0000-0000-0000-000000000001', 1, 1, repeat('a', 64), 'fixture', 'dbr1');
INSERT INTO ops.metric_observation (id, metric_key, environment, status)
VALUES ('00000000-0000-0000-0000-000000000002', 'dbr1.fixture', 'staging', 'ok');
