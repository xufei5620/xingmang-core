-- Immutable launch policy selected by the operator:
--   2026-09-01 00:00:00 Asia/Shanghai = 2026-08-31 16:00:00 UTC.
--
-- This migration is intentionally pre-launch only. A populated financial
-- ledger requires a dedicated recalculation/red-invoice migration rather than
-- silently changing eligibility underneath pending or issued requests.

-- The migration runner owns one transaction for this entire file. Block old
-- API/ingest writers before checking the empty-ledger precondition so a row
-- cannot appear between the check and installation of the policy guards.
LOCK TABLE funding_lots,source_usage_events,source_credit_events,
    consumption_allocations,invoice_requests,source_cutover_manifests IN SHARE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM funding_lots)
       OR EXISTS (SELECT 1 FROM source_usage_events)
       OR EXISTS (SELECT 1 FROM source_credit_events)
       OR EXISTS (SELECT 1 FROM consumption_allocations)
       OR EXISTS (SELECT 1 FROM invoice_requests)
       OR EXISTS (SELECT 1 FROM source_cutover_manifests) THEN
        RAISE EXCEPTION 'eligibility policy migration requires an empty pre-launch financial ledger';
    END IF;
END
$$;

CREATE TABLE invoice_eligibility_policy (
    singleton_id             SMALLINT PRIMARY KEY DEFAULT 1 CHECK (singleton_id = 1),
    eligibility_start_at     TIMESTAMPTZ NOT NULL,
    display_timezone         TEXT NOT NULL CHECK (display_timezone = 'Asia/Shanghai'),
    require_payment_at_or_after BOOLEAN NOT NULL CHECK (require_payment_at_or_after),
    require_usage_at_or_after   BOOLEAN NOT NULL CHECK (require_usage_at_or_after),
    policy_version           BIGINT NOT NULL DEFAULT 1 CHECK (policy_version > 0),
    updated_by               TEXT NOT NULL CHECK (btrim(updated_by) <> ''),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (eligibility_start_at = date_trunc('second', eligibility_start_at))
);

INSERT INTO invoice_eligibility_policy(
    singleton_id,eligibility_start_at,display_timezone,
    require_payment_at_or_after,require_usage_at_or_after,updated_by)
VALUES(
    1,'2026-09-01T00:00:00+08:00'::timestamptz,'Asia/Shanghai',TRUE,TRUE,
    'migration:0011_invoice_eligibility_policy'
);

ALTER TABLE invoice_requests
    ADD COLUMN eligibility_policy_start_at TIMESTAMPTZ NOT NULL
        DEFAULT '2026-09-01T00:00:00+08:00'::timestamptz,
    ADD COLUMN eligibility_policy_version BIGINT NOT NULL DEFAULT 1
        CHECK (eligibility_policy_version > 0);
ALTER TABLE invoice_requests
    ALTER COLUMN eligibility_policy_start_at DROP DEFAULT,
    ALTER COLUMN eligibility_policy_version DROP DEFAULT;

CREATE OR REPLACE FUNCTION enforce_invoice_request_immutability() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.invoice_user_id IS DISTINCT FROM OLD.invoice_user_id
       OR NEW.source_instance_id IS DISTINCT FROM OLD.source_instance_id
       OR NEW.profile_id IS DISTINCT FROM OLD.profile_id
       OR NEW.profile_snapshot_ciphertext IS DISTINCT FROM OLD.profile_snapshot_ciphertext
       OR NEW.currency IS DISTINCT FROM OLD.currency
       OR NEW.issuer_code IS DISTINCT FROM OLD.issuer_code
       OR NEW.service_item IS DISTINCT FROM OLD.service_item
       OR NEW.amount_minor IS DISTINCT FROM OLD.amount_minor
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.eligibility_policy_start_at IS DISTINCT FROM OLD.eligibility_policy_start_at
       OR NEW.eligibility_policy_version IS DISTINCT FROM OLD.eligibility_policy_version THEN
        RAISE EXCEPTION 'immutable invoice request fields cannot be changed';
    END IF;
    IF OLD.issue_snapshot_ciphertext IS NOT NULL
       AND (NEW.issue_snapshot_ciphertext IS DISTINCT FROM OLD.issue_snapshot_ciphertext
            OR NEW.issuer_setting_revision IS DISTINCT FROM OLD.issuer_setting_revision) THEN
        RAISE EXCEPTION 'issued invoice snapshot cannot be changed';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION enforce_invoice_request_policy_snapshot() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    policy_start TIMESTAMPTZ;
    policy_version BIGINT;
BEGIN
    SELECT p.eligibility_start_at,p.policy_version
    INTO STRICT policy_start,policy_version
    FROM invoice_eligibility_policy p WHERE p.singleton_id=1;
    IF NEW.eligibility_policy_start_at IS DISTINCT FROM policy_start
       OR NEW.eligibility_policy_version IS DISTINCT FROM policy_version THEN
        RAISE EXCEPTION 'invoice request policy snapshot does not match immutable policy';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER invoice_requests_policy_snapshot_guard
BEFORE INSERT ON invoice_requests
FOR EACH ROW EXECUTE FUNCTION enforce_invoice_request_policy_snapshot();

CREATE FUNCTION guard_invoice_eligibility_policy() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'invoice eligibility policy is immutable; change requires a new audited migration';
END;
$$;

CREATE TRIGGER invoice_eligibility_policy_guard
BEFORE UPDATE OR DELETE ON invoice_eligibility_policy
FOR EACH ROW EXECUTE FUNCTION guard_invoice_eligibility_policy();
CREATE TRIGGER invoice_eligibility_policy_truncate_guard
BEFORE TRUNCATE ON invoice_eligibility_policy
FOR EACH STATEMENT EXECUTE FUNCTION guard_invoice_eligibility_policy();

CREATE FUNCTION enforce_source_manifest_projection_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    source_kind TEXT;
    owner_name NAME;
    policy_start TIMESTAMPTZ;
BEGIN
    SELECT source_type INTO STRICT source_kind FROM source_instances WHERE id=NEW.source_instance_id;
    SELECT r.rolname INTO STRICT owner_name
    FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba
    WHERE d.datname=current_database();
	SELECT eligibility_start_at INTO STRICT policy_start
	FROM invoice_eligibility_policy WHERE singleton_id=1;
	IF (NEW.cutover_at>=policy_start OR NEW.database_clock>=policy_start)
	   AND NOT (current_user=owner_name AND NEW.projection_contract='fixture-v3'
	       AND NEW.signing_key_id='fixture' AND NEW.source_runtime_version='fixture-runtime') THEN
		RAISE EXCEPTION 'source cutover must be strictly before invoice eligibility start';
	END IF;
    IF (source_kind='sub2api' AND NEW.projection_contract='sub2api-economic-v3')
       OR (source_kind='newapi' AND NEW.projection_contract='newapi-economic-rc25-v3') THEN
        RETURN NEW;
    END IF;
    IF current_user=owner_name AND NEW.projection_contract='fixture-v3'
       AND NEW.signing_key_id='fixture' AND NEW.source_runtime_version='fixture-runtime' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'source projection contract does not match source type';
END;
$$;

CREATE TRIGGER source_cutover_manifests_projection_contract_guard
BEFORE INSERT OR UPDATE OF source_instance_id,projection_contract,signing_key_id,
    source_runtime_version,cutover_at,database_clock
ON source_cutover_manifests
FOR EACH ROW EXECUTE FUNCTION enforce_source_manifest_projection_contract();

ALTER TABLE source_usage_events
    ADD COLUMN invoice_eligible BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE source_usage_events ALTER COLUMN invoice_eligible DROP DEFAULT;

-- The v3 source usage contract currently proves wallet usage only
-- (billing_scope='wallet'). A subscription payment must therefore remain
-- unconsumed until a future version introduces authoritative subscription
-- usage evidence; purchase approval alone is not proof of actual use.
ALTER TABLE funding_lots
    ADD CONSTRAINT funding_lots_subscription_requires_usage_evidence CHECK (
        eligibility_kind<>'SUBSCRIPTION_CASH' OR consumed_cash_minor=0
    );

ALTER TABLE source_account_eligibility_state
    DROP CONSTRAINT source_account_eligibility_state_bootstrap_kind_check,
    ADD CONSTRAINT source_account_eligibility_state_bootstrap_kind_check CHECK (
        bootstrap_kind IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY')
    );

CREATE OR REPLACE FUNCTION enforce_account_eligibility_cutover_contract() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    manifest_cutover TIMESTAMPTZ;
    manifest_unit TEXT;
    account_source UUID;
BEGIN
    IF TG_OP='UPDATE' AND (
        NEW.external_account_id IS DISTINCT FROM OLD.external_account_id
        OR NEW.source_instance_id IS DISTINCT FROM OLD.source_instance_id
        OR NEW.cutover_at IS DISTINCT FROM OLD.cutover_at
        OR NEW.unit_code IS DISTINCT FROM OLD.unit_code
        OR NEW.cutover_balance_units IS DISTINCT FROM OLD.cutover_balance_units
        OR NEW.cutover_manifest_hash IS DISTINCT FROM OLD.cutover_manifest_hash
        OR NEW.bootstrap_kind IS DISTINCT FROM OLD.bootstrap_kind
        OR NEW.finalization_delay_seconds IS DISTINCT FROM OLD.finalization_delay_seconds
    ) THEN
        RAISE EXCEPTION 'account eligibility trust boundary is immutable';
    END IF;
    SELECT cutover_at,unit_code INTO manifest_cutover,manifest_unit
    FROM source_cutover_manifests
    WHERE source_instance_id=NEW.source_instance_id AND manifest_hash=NEW.cutover_manifest_hash;
    SELECT source_instance_id INTO account_source FROM external_accounts WHERE id=NEW.external_account_id;
    IF manifest_cutover IS NULL OR account_source<>NEW.source_instance_id OR manifest_unit<>NEW.unit_code THEN
        RAISE EXCEPTION 'account eligibility state does not match source cutover manifest';
    END IF;
    IF (NEW.bootstrap_kind='SIGNED_CUTOVER' AND NEW.cutover_at<>manifest_cutover)
       OR (NEW.bootstrap_kind='POST_CUTOVER_REPLAY'
           AND (NEW.cutover_at<>manifest_cutover OR NEW.cutover_balance_units<>0)) THEN
        RAISE EXCEPTION 'account eligibility cutover boundary is invalid';
    END IF;
    RETURN NEW;
END;
$$;

ALTER TABLE source_credit_events
    DROP CONSTRAINT source_credit_events_credit_kind_check,
    ADD COLUMN funding_lot_id UUID REFERENCES funding_lots(id) ON DELETE RESTRICT,
    ADD CONSTRAINT source_credit_events_credit_kind_check CHECK (credit_kind IN (
        'LEGACY_NON_INVOICEABLE','PRE_POLICY_NON_INVOICEABLE',
        'BONUS','REBATE','ADMIN','UNKNOWN_POSITIVE'
    )),
    ADD CONSTRAINT source_credit_events_pre_policy_lot_check CHECK (
        (credit_kind='PRE_POLICY_NON_INVOICEABLE' AND funding_lot_id IS NOT NULL)
        OR (credit_kind<>'PRE_POLICY_NON_INVOICEABLE' AND funding_lot_id IS NULL)
    );

CREATE UNIQUE INDEX source_credit_events_pre_policy_lot_unique
    ON source_credit_events(funding_lot_id)
    WHERE credit_kind='PRE_POLICY_NON_INVOICEABLE';

CREATE FUNCTION enforce_usage_invoice_policy() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE policy_start TIMESTAMPTZ;
BEGIN
    SELECT eligibility_start_at INTO STRICT policy_start
    FROM invoice_eligibility_policy WHERE singleton_id=1;
    IF NEW.invoice_eligible IS DISTINCT FROM (NEW.event_time>=policy_start) THEN
        RAISE EXCEPTION 'usage invoice eligibility does not match immutable policy start';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER source_usage_events_invoice_policy_guard
BEFORE INSERT OR UPDATE OF event_time,invoice_eligible ON source_usage_events
FOR EACH ROW EXECUTE FUNCTION enforce_usage_invoice_policy();

CREATE FUNCTION enforce_funding_lot_invoice_policy() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE policy_start TIMESTAMPTZ;
BEGIN
	IF TG_OP='UPDATE' AND OLD.completed_at IS NOT NULL
	   AND NEW.completed_at IS DISTINCT FROM OLD.completed_at THEN
		RAISE EXCEPTION 'funding lot completion time is immutable after first observation';
	END IF;
	IF TG_OP='UPDATE' AND OLD.eligibility_kind<>'LEGACY_NON_INVOICEABLE'
	   AND NEW.eligibility_kind IS DISTINCT FROM OLD.eligibility_kind THEN
		RAISE EXCEPTION 'classified funding lot eligibility kind is immutable';
	END IF;
    IF NEW.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH') THEN
        SELECT eligibility_start_at INTO STRICT policy_start
        FROM invoice_eligibility_policy WHERE singleton_id=1;
        IF NEW.completed_at IS NULL OR NEW.completed_at<policy_start
           OR NEW.eligibility_cutover_at IS NULL
           OR NEW.eligibility_cutover_at<policy_start THEN
            RAISE EXCEPTION 'cash funding lot predates immutable invoice eligibility policy';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER funding_lots_invoice_policy_guard
BEFORE INSERT OR UPDATE OF eligibility_kind,completed_at,eligibility_cutover_at ON funding_lots
FOR EACH ROW EXECUTE FUNCTION enforce_funding_lot_invoice_policy();

CREATE FUNCTION enforce_cash_consumption_invoice_policy() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    usage_allowed BOOLEAN;
    contract_matches BOOLEAN;
BEGIN
    IF NEW.funding_lot_id IS NOT NULL THEN
        SELECT u.invoice_eligible,
            u.external_account_id=fl.external_account_id
            AND u.source_instance_id=fl.source_instance_id
            AND fl.eligibility_kind='WALLET_CASH'
            AND u.unit_code=eas.unit_code
            AND u.cutover_manifest_hash=eas.cutover_manifest_hash
            AND u.configuration_hash=scm.configuration_hash
        INTO STRICT usage_allowed,contract_matches
        FROM source_usage_events u
        JOIN funding_lots fl ON fl.id=NEW.funding_lot_id
        JOIN source_account_eligibility_state eas ON eas.external_account_id=fl.external_account_id
        JOIN source_cutover_manifests scm ON scm.source_instance_id=fl.source_instance_id
        WHERE u.id=NEW.usage_event_id;
        IF NOT usage_allowed OR NOT contract_matches THEN
            RAISE EXCEPTION 'cash allocation lacks matching post-policy wallet consumption evidence';
        END IF;
    ELSE
        SELECT u.external_account_id=ce.external_account_id
            AND u.source_instance_id=ce.source_instance_id
            AND u.unit_code=ce.unit_code
            AND u.cutover_manifest_hash=ce.cutover_manifest_hash
            AND u.configuration_hash=ce.configuration_hash
        INTO STRICT contract_matches
        FROM source_usage_events u
        JOIN source_credit_events ce ON ce.id=NEW.credit_event_id
        WHERE u.id=NEW.usage_event_id;
        IF NOT contract_matches THEN
            RAISE EXCEPTION 'non-cash allocation crosses source account or projection contract';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER consumption_allocations_invoice_policy_guard
BEFORE INSERT OR UPDATE OF usage_event_id,funding_lot_id,credit_event_id ON consumption_allocations
FOR EACH ROW EXECUTE FUNCTION enforce_cash_consumption_invoice_policy();

CREATE FUNCTION reject_consumption_allocation_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'consumption allocations are rebuilt by delete and insert, never updated';
END;
$$;

CREATE TRIGGER consumption_allocations_update_guard
BEFORE UPDATE ON consumption_allocations
FOR EACH ROW EXECUTE FUNCTION reject_consumption_allocation_update();

-- Extend the existing deferred mirror check so an active production wallet
-- lot cannot claim more consumed cash than the authoritative post-policy usage
-- allocations actually prove. Frozen/refund-attention lots intentionally keep
-- their historical exposure floor. fixture-v3 is emitted only by the
-- test/bootstrap-only UpsertFundingLot helper; signed production manifest
-- registration rejects that projection contract.
CREATE OR REPLACE FUNCTION enforce_lot_consumption_mirror() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target_lot UUID;
    lot_minor BIGINT;
    paid_minor BIGINT;
    current_cash_units NUMERIC(78,0);
    current_consumed_units NUMERIC(78,0);
    current_numerator NUMERIC(156,0);
    current_rounded BIGINT;
    current_remainder NUMERIC(156,0);
    allocated_units NUMERIC(78,0);
    allocated_minor BIGINT;
    refund_frozen BOOLEAN;
    account_status TEXT;
    fixture_contract BOOLEAN;
BEGIN
    IF TG_OP='DELETE' THEN
        target_lot := OLD.funding_lot_id;
    ELSE
        target_lot := NEW.funding_lot_id;
    END IF;
    IF target_lot IS NULL THEN
        IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
    END IF;
    SELECT fl.consumed_cash_minor,fl.verified_cash_minor,flcs.cash_service_units,
        flcs.consumed_service_units,flcs.cumulative_cash_numerator,
        flcs.rounded_consumed_cash_minor,flcs.rounding_remainder_numerator,
        fl.refund_frozen,eas.eligibility_status,
        (scm.projection_contract='fixture-v3' AND scm.signing_key_id='fixture'
         AND scm.source_runtime_version='fixture-runtime')
    INTO lot_minor,paid_minor,current_cash_units,current_consumed_units,current_numerator,
        current_rounded,current_remainder,refund_frozen,account_status,fixture_contract
    FROM funding_lots fl
    JOIN funding_lot_consumption_state flcs ON flcs.funding_lot_id=fl.id
    JOIN source_account_eligibility_state eas ON eas.external_account_id=fl.external_account_id
    JOIN source_cutover_manifests scm ON scm.source_instance_id=fl.source_instance_id
    WHERE fl.id=target_lot FOR UPDATE OF fl,flcs;
    IF current_rounded <> lot_minor OR current_rounded > paid_minor THEN
        RAISE EXCEPTION 'lot consumption state does not match funding lot';
    END IF;
    IF current_numerator <> paid_minor::numeric * current_consumed_units THEN
        RAISE EXCEPTION 'cumulative cash numerator is inconsistent';
    END IF;
    IF current_cash_units > 0 AND
       current_remainder <> current_numerator - current_rounded::numeric * current_cash_units THEN
        RAISE EXCEPTION 'cash rounding remainder is inconsistent';
    END IF;
    IF current_cash_units > 0 AND
       current_rounded::numeric <> floor((2*current_numerator + current_cash_units) /
                                         (2*current_cash_units)) THEN
        RAISE EXCEPTION 'cash rounding is not exact half-up';
    END IF;
    IF current_cash_units > 0 AND current_consumed_units = current_cash_units
       AND current_rounded <> paid_minor THEN
        RAISE EXCEPTION 'fully consumed lot must equal verified cash';
    END IF;
    IF NOT refund_frozen AND account_status='active' AND NOT fixture_contract THEN
        SELECT COALESCE(sum(service_units),0),COALESCE(sum(cash_minor_delta),0)
        INTO allocated_units,allocated_minor
        FROM consumption_allocations WHERE funding_lot_id=target_lot;
        IF allocated_units<>current_consumed_units OR allocated_minor<>current_rounded THEN
            RAISE EXCEPTION 'wallet consumption state lacks matching usage allocations';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$$;

CREATE CONSTRAINT TRIGGER consumption_allocations_mirror_guard
AFTER INSERT OR DELETE ON consumption_allocations
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_lot_consumption_mirror();
