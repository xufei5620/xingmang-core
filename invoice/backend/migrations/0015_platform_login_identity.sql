-- Platform-password login (XM-INV-LOGIN): the user-facing login no longer
-- goes through the central OIDC provider. A verified platform account is
-- still keyed through the existing (oidc_issuer, oidc_subject) identity pair
-- -- issuer holds the platform's exact configured login origin and subject
-- holds the platform's user ID -- so ResolveOrCreate, auth_sessions and every
-- downstream query keep working unchanged. These two columns are an explicit,
-- queryable projection of that same pair for platform-scoped login sessions;
-- they stay NULL for OIDC-established rows (administrators).
ALTER TABLE invoice_users
    ADD COLUMN platform TEXT CHECK (platform IS NULL OR platform IN ('sub2api', 'newapi')),
    ADD COLUMN platform_user_id TEXT CHECK (
        platform_user_id IS NULL OR (
            btrim(platform_user_id) = platform_user_id
            AND platform_user_id <> ''
            AND char_length(platform_user_id) <= 512
        )
    ),
    ADD CONSTRAINT invoice_users_platform_pair_ck CHECK ((platform IS NULL) = (platform_user_id IS NULL));
