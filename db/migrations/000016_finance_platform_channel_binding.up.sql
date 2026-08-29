-- XM-C-MAP2：managed service 渠道与上游账号的时间版本化人工确认绑定。
CREATE UNIQUE INDEX IF NOT EXISTS service_id_environment_key
    ON core.service (id, environment);

CREATE UNIQUE INDEX IF NOT EXISTS upstream_account_id_environment_key
    ON finance.upstream_account (id, environment);

CREATE TABLE finance.platform_channel_binding (
    id                  uuid PRIMARY KEY,
    environment         text NOT NULL,
    service_id          uuid NOT NULL,
    external_channel_id text NOT NULL,
    upstream_account_id uuid NOT NULL,
    valid_from          timestamptz NOT NULL,
    valid_to            timestamptz,
    provenance          text NOT NULL,
    reason              text NOT NULL,
    created_by          text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT platform_channel_binding_environment_fk
      FOREIGN KEY (environment) REFERENCES core.environment(id) ON DELETE RESTRICT,
    CONSTRAINT platform_channel_binding_service_environment_fk
      FOREIGN KEY (service_id, environment) REFERENCES core.service(id, environment) ON DELETE RESTRICT,
    CONSTRAINT platform_channel_binding_account_environment_fk
      FOREIGN KEY (upstream_account_id, environment)
      REFERENCES finance.upstream_account(id, environment) ON DELETE RESTRICT,
    CONSTRAINT platform_channel_binding_channel_non_blank CHECK (btrim(external_channel_id) <> ''),
    CONSTRAINT platform_channel_binding_channel_canonical CHECK (external_channel_id = btrim(external_channel_id)),
    CONSTRAINT platform_channel_binding_reason_non_blank CHECK (btrim(reason) <> ''),
    CONSTRAINT platform_channel_binding_creator_non_blank CHECK (btrim(created_by) <> ''),
    CONSTRAINT platform_channel_binding_provenance_allowed CHECK (provenance IN ('manual', 'token_map_backfill')),
    CONSTRAINT platform_channel_binding_interval_ordered CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE UNIQUE INDEX platform_channel_binding_active_key
  ON finance.platform_channel_binding(service_id, external_channel_id)
  WHERE valid_to IS NULL;

CREATE UNIQUE INDEX platform_channel_binding_start_key
  ON finance.platform_channel_binding(service_id, external_channel_id, valid_from);

CREATE INDEX platform_channel_binding_upstream_history_idx
  ON finance.platform_channel_binding(upstream_account_id, valid_from, valid_to);
