DROP TABLE IF EXISTS assurance.probe_result;
DROP TABLE IF EXISTS assurance.probe_run;
DROP TABLE IF EXISTS assurance.probe_declaration;
DROP SCHEMA IF EXISTS assurance;

ALTER TABLE core.connector_config
    DROP COLUMN IF EXISTS probe_enabled,
    DROP COLUMN IF EXISTS probe_credential_ref;
