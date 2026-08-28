-- XM-B003：每位 HUMAN Principal 的个人表格视图。
--
-- 这是控制平台自己的 UI 偏好，不写任何上游系统。owner 与 environment 全部由
-- 认证 Principal 派生；客户端没有可以改写这些边界的列或参数。
CREATE SCHEMA IF NOT EXISTS ui;

CREATE TABLE ui.saved_view (
    id              uuid PRIMARY KEY,
    owner_issuer    text NOT NULL,
    owner_subject   text NOT NULL,
    identity_zone   text NOT NULL,
    environment     text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    table_key       text NOT NULL,
    name            text NOT NULL,
    state_version   smallint NOT NULL,
    query           text NOT NULL,
    filters         jsonb NOT NULL,
    sort_column     text,
    sort_direction  text,
    known_columns   text[] NOT NULL,
    visible_columns text[] NOT NULL,
    density         text NOT NULL,
    state_hash      text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT saved_view_owner_issuer_nonempty CHECK (btrim(owner_issuer) <> ''),
    CONSTRAINT saved_view_owner_subject_nonempty CHECK (btrim(owner_subject) <> ''),
    CONSTRAINT saved_view_identity_zone_nonempty CHECK (btrim(identity_zone) <> ''),
    CONSTRAINT saved_view_table_key_format CHECK (
        char_length(table_key) BETWEEN 1 AND 128
        AND table_key ~ '^[a-z0-9][a-z0-9._-]{0,127}$'
    ),
    CONSTRAINT saved_view_name_length CHECK (
        btrim(name) <> '' AND char_length(name) BETWEEN 1 AND 24
    ),
    CONSTRAINT saved_view_state_version_v1 CHECK (state_version = 1),
    CONSTRAINT saved_view_query_length CHECK (char_length(query) <= 256),
    CONSTRAINT saved_view_filters_object CHECK (jsonb_typeof(filters) = 'object'),
    CONSTRAINT saved_view_sort_pair CHECK (
        (sort_column IS NULL AND sort_direction IS NULL)
        OR (
            sort_column IS NOT NULL
            AND btrim(sort_column) <> ''
            AND sort_direction IN ('asc', 'desc')
        )
    ),
    CONSTRAINT saved_view_known_columns_nonempty CHECK (
        cardinality(known_columns) BETWEEN 1 AND 64
        AND array_position(known_columns, NULL) IS NULL
    ),
    CONSTRAINT saved_view_visible_columns_nonempty CHECK (
        cardinality(visible_columns) BETWEEN 1 AND 64
        AND array_position(visible_columns, NULL) IS NULL
    ),
    CONSTRAINT saved_view_visible_columns_known CHECK (visible_columns <@ known_columns),
    CONSTRAINT saved_view_density CHECK (density IN ('compact', 'standard', 'comfortable')),
    CONSTRAINT saved_view_state_hash_sha256 CHECK (state_hash ~ '^[0-9a-f]{64}$')
);

CREATE UNIQUE INDEX saved_view_owner_table_name_key
    ON ui.saved_view (
        owner_issuer, owner_subject, identity_zone, environment, table_key, name
    );

CREATE INDEX saved_view_owner_table_updated_idx
    ON ui.saved_view (
        owner_issuer, owner_subject, identity_zone, environment, table_key, updated_at DESC
    );

