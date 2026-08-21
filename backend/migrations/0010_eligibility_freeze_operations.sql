-- Administrator operations for consumption-eligibility freezes. Resolution
-- evidence remains encrypted at rest and no manual entitlement override is
-- introduced: the application may resolve only after trusted source facts and
-- a finalized balance evaluation prove the account safe.

ALTER TABLE eligibility_freezes
    ADD COLUMN resolution_evidence_ciphertext BYTEA,
    ADD COLUMN resolution_note_ciphertext BYTEA,
    ADD COLUMN resolution_note_hash CHAR(64),
    ADD COLUMN resolution_version BIGINT NOT NULL DEFAULT 1
        CHECK (resolution_version > 0),
    ADD CONSTRAINT eligibility_freezes_resolution_note_hash_check
        CHECK (resolution_note_hash IS NULL OR resolution_note_hash ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT eligibility_freezes_encrypted_resolution_check CHECK (
        (status='open'
         AND resolution_evidence_ciphertext IS NULL
         AND resolution_note_ciphertext IS NULL
         AND resolution_note_hash IS NULL
         AND resolution_version=1)
        OR
        (status='resolved'
         AND octet_length(resolution_evidence_ciphertext) BETWEEN 16 AND 8192
         AND octet_length(resolution_note_ciphertext) BETWEEN 16 AND 8192
         AND resolution_note_hash ~ '^[0-9a-f]{64}$'
         AND resolution_version>1)
    );

CREATE INDEX eligibility_freezes_admin_page_idx
    ON eligibility_freezes(status,opened_at DESC,id DESC);

CREATE INDEX eligibility_freezes_account_status_idx
    ON eligibility_freezes(external_account_id,status,opened_at DESC,id DESC);
