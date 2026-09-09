CREATE INDEX oidc_backchannel_logout_retention_idx
    ON oidc_backchannel_logout_events(received_at, token_expires_at, id);
