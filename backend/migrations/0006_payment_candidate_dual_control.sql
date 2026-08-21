-- A manual New API refund/freeze permanently lowers the maximum amount that a
-- later two-person review may restore. Source rescans never write this column.
ALTER TABLE funding_lots
  ADD COLUMN newapi_manual_ceiling_minor BIGINT,
  ADD CONSTRAINT funding_lots_newapi_manual_ceiling_check CHECK (
    newapi_manual_ceiling_minor IS NULL
    OR (newapi_manual_ceiling_minor >= 0 AND newapi_manual_ceiling_minor <= original_minor)
  );

-- Verification is a two-person decision. The public API action remains
-- "verify"; the store records whether the call proposed or approved it.
ALTER TABLE payment_candidate_reviews
  DROP CONSTRAINT IF EXISTS payment_candidate_reviews_review_action_check;
ALTER TABLE payment_candidate_reviews
  ADD CONSTRAINT payment_candidate_reviews_review_action_check
  CHECK (review_action IN ('verify_propose','verify_approve','reject','freeze'));

-- A rejected/frozen proposal may later be restarted with the same external
-- evidence after new review. Verify-row idempotency is serialized by the
-- decision row and funding-lot lock, so the old all-actions uniqueness would
-- incorrectly resurrect/approve stale evidence.
ALTER TABLE payment_candidate_reviews
  DROP CONSTRAINT IF EXISTS payment_candidate_reviews_funding_lot_id_review_action_evidence_hash_key;
ALTER TABLE payment_candidate_reviews
  DROP CONSTRAINT IF EXISTS payment_candidate_reviews_funding_lot_id_review_action_evid_key;
CREATE UNIQUE INDEX payment_candidate_reviews_negative_idempotency
  ON payment_candidate_reviews(funding_lot_id,review_action,evidence_hash)
  WHERE review_action IN ('reject','freeze');

CREATE TABLE payment_candidate_decisions (
  funding_lot_id     UUID PRIMARY KEY REFERENCES funding_lots(id) ON DELETE RESTRICT,
  state              TEXT NOT NULL CHECK (state IN ('proposed','approved')),
  paid_minor         BIGINT NOT NULL CHECK (paid_minor > 0),
  currency           CHAR(3) NOT NULL CHECK (currency = 'CNY'),
  evidence_hash      CHAR(64) NOT NULL CHECK (evidence_hash ~ '^[0-9a-f]{64}$'),
  proposed_by        UUID NOT NULL,
  proposed_review_id UUID NOT NULL UNIQUE REFERENCES payment_candidate_reviews(id) ON DELETE RESTRICT,
  proposed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  approved_by        UUID,
  approved_review_id UUID UNIQUE REFERENCES payment_candidate_reviews(id) ON DELETE RESTRICT,
  approved_at        TIMESTAMPTZ,
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (
    (state='proposed' AND approved_by IS NULL AND approved_review_id IS NULL AND approved_at IS NULL)
    OR
    (state='approved' AND approved_by IS NOT NULL AND approved_review_id IS NOT NULL
                      AND approved_at IS NOT NULL AND approved_by <> proposed_by)
  )
);

CREATE FUNCTION enforce_payment_candidate_decision_amount() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  source_kind TEXT;
  trusted_upper BIGINT;
BEGIN
	SELECT si.source_type, LEAST(fl.original_minor,
		COALESCE(fl.newapi_manual_ceiling_minor,fl.original_minor))
    INTO source_kind, trusted_upper
  FROM funding_lots fl
  JOIN source_instances si ON si.id=fl.source_instance_id
  WHERE fl.id=NEW.funding_lot_id
  FOR UPDATE OF fl;
  IF source_kind IS DISTINCT FROM 'newapi' THEN
    RAISE EXCEPTION 'payment candidate decision requires a New API funding lot';
  END IF;
  IF NEW.paid_minor > trusted_upper THEN
    RAISE EXCEPTION 'verified payment amount exceeds trusted candidate upper bound';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER payment_candidate_decision_amount_guard
BEFORE INSERT OR UPDATE ON payment_candidate_decisions
FOR EACH ROW EXECUTE FUNCTION enforce_payment_candidate_decision_amount();

CREATE FUNCTION enforce_newapi_verified_dual_control() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  source_kind TEXT;
  decision_ok BOOLEAN;
BEGIN
  IF NEW.verification_state <> 'verified' THEN
    RETURN NEW;
  END IF;
  SELECT source_type INTO source_kind FROM source_instances WHERE id=NEW.source_instance_id;
  IF source_kind <> 'newapi' THEN
    RETURN NEW;
  END IF;
  SELECT EXISTS (
    SELECT 1 FROM payment_candidate_decisions d
    WHERE d.funding_lot_id=NEW.id AND d.state='approved'
      AND d.paid_minor=NEW.current_cap_minor AND d.currency=NEW.currency
      AND d.proposed_by <> d.approved_by
  ) INTO decision_ok;
  IF NOT decision_ok THEN
    RAISE EXCEPTION 'New API funding lot requires approved two-person payment decision';
  END IF;
  RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER newapi_verified_dual_control_guard
AFTER INSERT OR UPDATE OF verification_state,current_cap_minor,currency ON funding_lots
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_newapi_verified_dual_control();

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM funding_lots fl
    JOIN source_instances si ON si.id=fl.source_instance_id
    WHERE si.source_type='newapi' AND fl.verification_state='verified'
  ) THEN
    RAISE EXCEPTION 'pre-existing singly verified New API lots require manual freeze and re-review before migration';
  END IF;
END
$$;
