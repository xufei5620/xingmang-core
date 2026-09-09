-- XM-INV-BALANCE-BLIP: a single transient positive balance-evidence
-- difference must not, by itself, synthesize a permanent UNKNOWN_POSITIVE
-- non-cash credit -- see docs/handoffs/XM-INV-BALANCE-BLIP.md for the
-- production incident (account 40bd883d..., 2026-09-02) and
-- docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md section
-- 2.9 for the rule chosen (evaluatePendingBalanceEvidenceTx,
-- postgresstore/consumption.go).
--
-- Part 1: a checkpoint/proof whose positive difference is not confirmed by
-- the next real piece of balance evidence is now recorded as
-- 'positive_blip_ignored' instead of synthesizing a credit for it. This is a
-- terminal, historical status -- like 'source_gap_frozen', it is
-- deliberately absent from evaluatePendingBalanceEvidenceTx's
-- trusted-interval query (unchanged by this migration), so it can never
-- itself become a trust anchor for a later evaluation.
ALTER TABLE balance_checkpoint_evaluations
    DROP CONSTRAINT IF EXISTS balance_checkpoint_evaluations_evaluation_status_check,
    ADD CONSTRAINT balance_checkpoint_evaluations_evaluation_status_check CHECK (evaluation_status IN (
        'matched','positive_classified_non_cash','negative_frozen','source_gap_frozen',
        'positive_blip_ignored'
    ));
ALTER TABLE balance_carry_forward_evaluations
    DROP CONSTRAINT IF EXISTS balance_carry_forward_evaluations_evaluation_status_check,
    ADD CONSTRAINT balance_carry_forward_evaluations_evaluation_status_check CHECK (evaluation_status IN (
        'matched','positive_classified_non_cash','negative_frozen','source_gap_frozen',
        'positive_blip_ignored'
    ));

-- Part 2: eligibility-repair --kind=balance-blip must remove a synthesized
-- UNKNOWN_POSITIVE credit that turns out to have been exactly this blip (its
-- amount permanently offset by every later checkpoint's own negative_frozen
-- difference, until the account froze). source_credit_events is otherwise
-- unconditionally immutable (reject_source_eligibility_fact_mutation,
-- migration 0009) -- correct for every externally-sourced fact, but an
-- UNKNOWN_POSITIVE row is never external: it is the one credit_kind this
-- system invents by inference rather than observes from a source stream. A
-- narrow, transaction-scoped, GUC-gated exception -- mirroring migration
-- 0016's guarded POLICY_ANCHOR re-anchor UPDATE -- lets a repair transaction
-- delete exactly such a row, and only such a row:
-- invoice.balance_blip_repair_delete='on' (set via set_config(...,true), a
-- transaction-local setting, never persisted) plus
-- OLD.credit_kind='UNKNOWN_POSITIVE'. Every other credit_kind, and every
-- other table's fact-immutability trigger, is untouched -- this replaces
-- only source_credit_events_immutable's trigger function, not the shared
-- reject_source_eligibility_fact_mutation() every other table's trigger
-- still points at.
CREATE FUNCTION enforce_source_credit_event_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' AND OLD.credit_kind='UNKNOWN_POSITIVE'
       AND COALESCE(current_setting('invoice.balance_blip_repair_delete', true), '') = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'source eligibility facts are immutable';
END;
$$;

DROP TRIGGER IF EXISTS source_credit_events_immutable ON source_credit_events;
CREATE TRIGGER source_credit_events_immutable
BEFORE UPDATE OR DELETE ON source_credit_events
FOR EACH ROW EXECUTE FUNCTION enforce_source_credit_event_mutation();
