package dbroles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateTransitionSteadyAToRotatingThenSteadyB(t *testing.T) {
	base := DefaultPolicyV1()
	baseBytes, baseDigest, err := base.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	genesis := genesisEvent(baseDigest, testNow().Add(-2*time.Hour))
	trusted := TrustedPolicyState{PolicyBytes: baseBytes, PolicySHA256: baseDigest, TerminalEvent: genesis, Events: []RolePolicyStateEvent{genesis}}

	rotating := base
	r := rotating.Rotations["xm_api_runtime"]
	r.State = RotationRotatingAB
	r.SteadyIdentity = ""
	r.OldIdentity = "xm_api_a"
	r.NewIdentity = "xm_api_b"
	r.ApprovedChangeRequest = "cr-api-1"
	r.StartedAt = "2026-08-30T09:00:00Z"
	r.Deadline = "2026-08-30T12:00:00Z"
	rotating.Rotations["xm_api_runtime"] = r
	if err := rotating.SetRotationTopology("xm_api_runtime", RotationRotatingAB); err != nil {
		t.Fatal(err)
	}
	rotatingBytes, rotatingDigest, err := rotating.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	cr := validChangeRequest("cr-api-1", "xm_api_runtime", "xm_api_a", "xm_api_b", baseDigest, rotatingDigest, at("2026-08-30T09:00:00Z"), at("2026-08-30T12:00:00Z"), "approved")
	crBytes := mustJSON(t, cr)
	crDigest := digestBytes(crBytes)
	start := rotationEvent(genesis, "rotation-start", "xm_api_runtime", "steady-a", "rotating-a-b", baseDigest, rotatingDigest, "cr-api-1", crDigest, "approved", testNow().Add(-time.Hour), "")
	proposed := ProposedPolicyState{PolicyBytes: rotatingBytes, PolicySHA256: rotatingDigest, CandidateEvent: start, ChangeRequestState: cr, ChangeRequestStateBytes: crBytes, ChangeRequestStateSHA256: crDigest}
	if violations := ValidateTransition(trusted, proposed, testNow()); len(violations) != 0 {
		t.Fatalf("steady-a -> rotating-a-b rejected: %+v", violations)
	}

	trusted = TrustedPolicyState{PolicyBytes: rotatingBytes, PolicySHA256: rotatingDigest, TerminalEvent: start, Events: append(trusted.Events, start)}
	steadyB := rotating
	r = steadyB.Rotations["xm_api_runtime"]
	r.State = RotationSteadyB
	r.SteadyIdentity = "xm_api_b"
	r.ClosureEvidence = "sha256:" + strings.Repeat("b", 64)
	r.ClosedAt = "2026-08-30T10:00:00Z"
	steadyB.Rotations["xm_api_runtime"] = r
	if err := steadyB.SetRotationTopology("xm_api_runtime", RotationSteadyB); err != nil {
		t.Fatal(err)
	}
	steadyBBytes, steadyBDigest, err := steadyB.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	closedCR := cr
	closedCR.PreviousPolicySHA256 = rotatingDigest
	closedCR.Status = "closed"
	closedCR.CurrentPolicySHA256 = steadyBDigest
	closedCR.ClosureEvidenceSHA256 = strings.TrimPrefix(r.ClosureEvidence, "sha256:")
	closedCR.ClosedAt = r.ClosedAt
	closedCRBytes := mustJSON(t, closedCR)
	closedCRDigest := digestBytes(closedCRBytes)
	close := rotationEvent(start, "rotation-close", "xm_api_runtime", "rotating-a-b", "steady-b", rotatingDigest, steadyBDigest, "cr-api-1", closedCRDigest, "closed", testNow(), closedCR.ClosureEvidenceSHA256)
	proposed = ProposedPolicyState{PolicyBytes: steadyBBytes, PolicySHA256: steadyBDigest, CandidateEvent: close, ChangeRequestState: closedCR, ChangeRequestStateBytes: closedCRBytes, ChangeRequestStateSHA256: closedCRDigest}
	if violations := ValidateTransition(trusted, proposed, testNow()); len(violations) != 0 {
		t.Fatalf("rotating-a-b -> steady-b rejected: %+v", violations)
	}
}

func TestValidateTransitionRejectsSteadyAToSteadyB(t *testing.T) {
	base := DefaultPolicyV1()
	baseBytes, baseDigest := canonicalPolicy(t, base)
	b := base
	r := b.Rotations["xm_api_runtime"]
	r.State, r.SteadyIdentity, r.ClosureEvidence = RotationSteadyB, "xm_api_b", "sha256:"+strings.Repeat("a", 64)
	r.OldIdentity, r.NewIdentity, r.ApprovedChangeRequest = "xm_api_a", "xm_api_b", "cr-skip"
	r.StartedAt, r.Deadline = "2026-08-30T08:00:00Z", "2026-08-30T12:00:00Z"
	r.ClosedAt = "2026-08-30T09:30:00Z"
	b.Rotations["xm_api_runtime"] = r
	if err := b.SetRotationTopology("xm_api_runtime", RotationSteadyB); err != nil {
		t.Fatal(err)
	}
	bBytes, bDigest := canonicalPolicy(t, b)
	genesis := genesisEvent(baseDigest, testNow().Add(-time.Hour))
	cr := validChangeRequest("cr-skip", "xm_api_runtime", "xm_api_a", "xm_api_b", baseDigest, bDigest, at("2026-08-30T08:00:00Z"), at("2026-08-30T12:00:00Z"), "closed")
	crBytes := mustJSON(t, cr)
	crDigest := digestBytes(crBytes)
	event := rotationEvent(genesis, "rotation-close", "xm_api_runtime", "steady-a", "steady-b", baseDigest, bDigest, "cr-skip", crDigest, "closed", testNow(), strings.Repeat("a", 64))
	violations := ValidateTransition(TrustedPolicyState{PolicyBytes: baseBytes, PolicySHA256: baseDigest, TerminalEvent: genesis, Events: []RolePolicyStateEvent{genesis}}, ProposedPolicyState{PolicyBytes: bBytes, PolicySHA256: bDigest, CandidateEvent: event, ChangeRequestState: cr, ChangeRequestStateBytes: crBytes, ChangeRequestStateSHA256: crDigest}, testNow())
	assertViolationCode(t, violations, "ROTATION_TRANSITION_SKIPPED")
}

func TestValidateTransitionRejectsReplayOrDuplicateEvent(t *testing.T) {
	base := DefaultPolicyV1()
	bytes0, digest0 := canonicalPolicy(t, base)
	genesis := genesisEvent(digest0, testNow().Add(-time.Hour))
	cr := validChangeRequest("cr-replay", "xm_api_runtime", "xm_api_a", "xm_api_b", digest0, digest0, at("2026-08-30T09:00:00Z"), at("2026-08-30T12:00:00Z"), "approved")
	crBytes := mustJSON(t, cr)
	crDigest := digestBytes(crBytes)
	event := rotationEvent(genesis, "policy-update", "", "", "", digest0, digest0, "", "", "", testNow(), "")
	// Replay the terminal event as the candidate: the event id/tuple and sequence
	// are not a fresh append, so validation must fail closed.
	violations := ValidateTransition(TrustedPolicyState{PolicyBytes: bytes0, PolicySHA256: digest0, TerminalEvent: event, Events: []RolePolicyStateEvent{genesis, event}}, ProposedPolicyState{PolicyBytes: bytes0, PolicySHA256: digest0, CandidateEvent: event, ChangeRequestState: cr, ChangeRequestStateBytes: crBytes, ChangeRequestStateSHA256: crDigest}, testNow())
	assertViolationCode(t, violations, "ROTATION_EVENT_REPLAY")
}

func TestValidateTransitionRejectsExpiredRotatingState(t *testing.T) {
	base := DefaultPolicyV1()
	baseBytes, baseDigest := canonicalPolicy(t, base)
	b := base
	r := b.Rotations["xm_api_runtime"]
	r.State, r.SteadyIdentity = RotationRotatingAB, ""
	r.OldIdentity, r.NewIdentity, r.ApprovedChangeRequest = "xm_api_a", "xm_api_b", "cr-expired"
	r.StartedAt, r.Deadline = "2026-08-30T07:00:00Z", "2026-08-30T08:00:00Z"
	b.Rotations["xm_api_runtime"] = r
	if err := b.SetRotationTopology("xm_api_runtime", RotationRotatingAB); err != nil {
		t.Fatal(err)
	}
	bBytes, bDigest := canonicalPolicy(t, b)
	genesis := genesisEvent(baseDigest, testNow().Add(-4*time.Hour))
	cr := validChangeRequest("cr-expired", "xm_api_runtime", "xm_api_a", "xm_api_b", baseDigest, bDigest, at("2026-08-30T07:00:00Z"), at("2026-08-30T08:00:00Z"), "approved")
	crBytes := mustJSON(t, cr)
	crDigest := digestBytes(crBytes)
	event := rotationEvent(genesis, "rotation-start", "xm_api_runtime", "steady-a", "rotating-a-b", baseDigest, bDigest, "cr-expired", crDigest, "approved", testNow().Add(-3*time.Hour), "")
	violations := ValidateTransition(TrustedPolicyState{PolicyBytes: baseBytes, PolicySHA256: baseDigest, TerminalEvent: genesis, Events: []RolePolicyStateEvent{genesis}}, ProposedPolicyState{PolicyBytes: bBytes, PolicySHA256: bDigest, CandidateEvent: event, ChangeRequestState: cr, ChangeRequestStateBytes: crBytes, ChangeRequestStateSHA256: crDigest}, testNow())
	assertViolationCode(t, violations, "ROTATION_DEADLINE_EXPIRED")
}

func TestValidateTransitionRejectsPreviousPolicyDigestMismatch(t *testing.T) {
	base := DefaultPolicyV1()
	bytes0, digest0 := canonicalPolicy(t, base)
	genesis := genesisEvent(digest0, testNow().Add(-time.Hour))
	mutated := base
	r := mutated.Rotations["xm_api_runtime"]
	r.State, r.SteadyIdentity = RotationRotatingAB, ""
	r.OldIdentity, r.NewIdentity, r.ApprovedChangeRequest = "xm_api_a", "xm_api_b", "cr-prev"
	r.StartedAt, r.Deadline = "2026-08-30T09:00:00Z", "2026-08-30T12:00:00Z"
	mutated.Rotations["xm_api_runtime"] = r
	if err := mutated.SetRotationTopology("xm_api_runtime", RotationRotatingAB); err != nil {
		t.Fatal(err)
	}
	bytes1, digest1 := canonicalPolicy(t, mutated)
	cr := validChangeRequest("cr-prev", "xm_api_runtime", "xm_api_a", "xm_api_b", digest0, digest1, at("2026-08-30T09:00:00Z"), at("2026-08-30T12:00:00Z"), "approved")
	crBytes := mustJSON(t, cr)
	crDigest := digestBytes(crBytes)
	event := rotationEvent(genesis, "rotation-start", "xm_api_runtime", "steady-a", "rotating-a-b", strings.Repeat("f", 64), digest1, "cr-prev", crDigest, "approved", testNow(), "")
	violations := ValidateTransition(TrustedPolicyState{PolicyBytes: bytes0, PolicySHA256: digest0, TerminalEvent: genesis, Events: []RolePolicyStateEvent{genesis}}, ProposedPolicyState{PolicyBytes: bytes1, PolicySHA256: digest1, CandidateEvent: event, ChangeRequestState: cr, ChangeRequestStateBytes: crBytes, ChangeRequestStateSHA256: crDigest}, testNow())
	assertViolationCode(t, violations, "ROTATION_PREVIOUS_POLICY_DIGEST_MISMATCH")
}

func TestValidateTransitionRejectsCRStateDigestMismatch(t *testing.T) {
	base := DefaultPolicyV1()
	bytes0, digest0 := canonicalPolicy(t, base)
	mutated := base
	r := mutated.Rotations["xm_api_runtime"]
	r.State, r.SteadyIdentity = RotationRotatingAB, ""
	r.OldIdentity, r.NewIdentity, r.ApprovedChangeRequest = "xm_api_a", "xm_api_b", "cr-digest"
	r.StartedAt, r.Deadline = "2026-08-30T09:00:00Z", "2026-08-30T12:00:00Z"
	mutated.Rotations["xm_api_runtime"] = r
	if err := mutated.SetRotationTopology("xm_api_runtime", RotationRotatingAB); err != nil {
		t.Fatal(err)
	}
	bytes1, digest1 := canonicalPolicy(t, mutated)
	genesis := genesisEvent(digest0, testNow().Add(-time.Hour))
	cr := validChangeRequest("cr-digest", "xm_api_runtime", "xm_api_a", "xm_api_b", digest0, digest1, at("2026-08-30T09:00:00Z"), at("2026-08-30T12:00:00Z"), "approved")
	crBytes := mustJSON(t, cr)
	crDigest := digestBytes(crBytes)
	event := rotationEvent(genesis, "rotation-start", "xm_api_runtime", "steady-a", "rotating-a-b", digest0, digest1, "cr-digest", strings.Repeat("c", 64), "approved", testNow(), "")
	violations := ValidateTransition(TrustedPolicyState{PolicyBytes: bytes0, PolicySHA256: digest0, TerminalEvent: genesis, Events: []RolePolicyStateEvent{genesis}}, ProposedPolicyState{PolicyBytes: bytes1, PolicySHA256: digest1, CandidateEvent: event, ChangeRequestState: cr, ChangeRequestStateBytes: crBytes, ChangeRequestStateSHA256: crDigest}, testNow())
	assertViolationCode(t, violations, "ROTATION_CR_STATE_DIGEST_MISMATCH")
}

func TestValidateTransitionRejectsClosureDigestMismatch(t *testing.T) {
	// Keep the fixture deadline in the past relative to the machine clock used
	// by CI. The transition must nevertheless use the explicit validation time
	// supplied by the caller, not LoadPolicy's wall clock.
	base := DefaultPolicyV1()
	_, digest0 := canonicalPolicy(t, base)
	genesis := genesisEvent(digest0, testNow().Add(-2*time.Hour))
	rotating := base
	r := rotating.Rotations["xm_api_runtime"]
	r.State, r.SteadyIdentity = RotationRotatingAB, ""
	r.OldIdentity, r.NewIdentity, r.ApprovedChangeRequest = "xm_api_a", "xm_api_b", "cr-close"
	r.StartedAt, r.Deadline = "2026-08-30T08:00:00Z", "2026-08-30T12:00:00Z"
	rotating.Rotations["xm_api_runtime"] = r
	if err := rotating.SetRotationTopology("xm_api_runtime", RotationRotatingAB); err != nil {
		t.Fatal(err)
	}
	bytes1, digest1 := canonicalPolicy(t, rotating)
	cr := validChangeRequest("cr-close", "xm_api_runtime", "xm_api_a", "xm_api_b", digest0, digest1, at("2026-08-30T08:00:00Z"), at("2026-08-30T12:00:00Z"), "approved")
	crBytes := mustJSON(t, cr)
	crDigest := digestBytes(crBytes)
	start := rotationEvent(genesis, "rotation-start", "xm_api_runtime", "steady-a", "rotating-a-b", digest0, digest1, "cr-close", crDigest, "approved", testNow().Add(-time.Hour), "")
	steadyB := rotating
	r = steadyB.Rotations["xm_api_runtime"]
	r.State, r.SteadyIdentity = RotationSteadyB, "xm_api_b"
	r.ClosureEvidence, r.ClosedAt = "sha256:"+strings.Repeat("a", 64), "2026-08-30T10:00:00Z"
	steadyB.Rotations["xm_api_runtime"] = r
	if err := steadyB.SetRotationTopology("xm_api_runtime", RotationSteadyB); err != nil {
		t.Fatal(err)
	}
	bytes2, digest2 := canonicalPolicy(t, steadyB)
	closedCR := cr
	closedCR.Status, closedCR.CurrentPolicySHA256 = "closed", digest2
	closedCR.ClosedAt = r.ClosedAt
	closedCR.ClosureEvidenceSHA256 = strings.Repeat("a", 64)
	closedCRBytes := mustJSON(t, closedCR)
	closedDigest := digestBytes(closedCRBytes)
	close := rotationEvent(start, "rotation-close", "xm_api_runtime", "rotating-a-b", "steady-b", digest1, digest2, "cr-close", closedDigest, "closed", testNow(), strings.Repeat("f", 64))
	trusted := TrustedPolicyState{PolicyBytes: bytes1, PolicySHA256: digest1, TerminalEvent: start, Events: []RolePolicyStateEvent{genesis, start}}
	proposed := ProposedPolicyState{PolicyBytes: bytes2, PolicySHA256: digest2, CandidateEvent: close, ChangeRequestState: closedCR, ChangeRequestStateBytes: closedCRBytes, ChangeRequestStateSHA256: closedDigest}
	violations := ValidateTransition(trusted, proposed, testNow())
	assertViolationCode(t, violations, "ROTATION_CLOSURE_DIGEST_MISMATCH")
	// A later explicit time is still before the fixture deadline; this second
	// assertion guards against accidentally consulting time.Now() internally.
	assertViolationCode(t, ValidateTransition(trusted, proposed, testNow().Add(15*time.Minute)), "ROTATION_CLOSURE_DIGEST_MISMATCH")
}

func TestLoadStateEventsRejectsNonCanonicalDuplicateAndBrokenChain(t *testing.T) {
	policy := DefaultPolicyV1()
	_, digest := canonicalPolicy(t, policy)
	genesis := genesisEvent(digest, testNow())
	b, _, err := genesis.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	valid := append(b, '\n')
	if _, err := LoadStateEvents(valid); err != nil {
		t.Fatalf("valid genesis rejected: %v", err)
	}
	pretty := append([]byte(" "), append(b, []byte("\n")...)...)
	if _, err := LoadStateEvents(pretty); err == nil {
		t.Fatal("non-canonical event whitespace accepted")
	}
	duplicate := append(append([]byte(nil), b...), []byte(`,"unknown":true}`)...)
	if _, err := LoadStateEvents(append(duplicate, '\n')); err == nil {
		t.Fatal("unknown event field accepted")
	}
	broken := genesis
	broken.Kind = EventKindPolicyUpdate
	broken.ToState = nil
	line, _, err := broken.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStateEvents(append(line, '\n')); err == nil {
		t.Fatal("broken genesis accepted")
	}
}

func TestLoadChangeRequestStateRequiresCanonicalBytes(t *testing.T) {
	state := validChangeRequest("cr-canonical", "xm_api_runtime", "xm_api_a", "xm_api_b", strings.Repeat("a", 64), strings.Repeat("b", 64), at("2026-08-30T09:00:00Z"), at("2026-08-30T12:00:00Z"), "approved")
	b, _, err := state.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadChangeRequestState(b); err != nil {
		t.Fatalf("canonical CR rejected: %v", err)
	}
	if _, _, err := LoadChangeRequestState(append([]byte("\n"), b...)); err == nil {
		t.Fatal("CR leading whitespace accepted")
	}
}

func genesisEvent(policyDigest string, recorded time.Time) RolePolicyStateEvent {
	to := RotationSteadyA
	return RolePolicyStateEvent{Version: PolicyVersionV1, Sequence: 1, EventID: "evt-genesis", Kind: EventKindGenesis, CurrentPolicySHA256: policyDigest, ToState: &to, RecordedAt: recorded.UTC()}
}

func rotationEvent(previous RolePolicyStateEvent, kind, capability, from, to, previousPolicy, currentPolicy, requestID, requestDigest, crStatus string, recorded time.Time, closure string) RolePolicyStateEvent {
	e := RolePolicyStateEvent{Version: PolicyVersionV1, Sequence: previous.Sequence + 1, EventID: previous.EventID + "-" + kind, Kind: kind, CurrentPolicySHA256: currentPolicy, RecordedAt: recorded.UTC()}
	e.PreviousEventSHA256 = stringPtr(eventDigest(previous))
	e.PreviousPolicySHA256 = stringPtr(previousPolicy)
	if capability != "" {
		e.Capability, e.FromState, e.ToState = stringPtr(capability), stringPtr(from), stringPtr(to)
	}
	if requestID != "" {
		e.ApprovedChangeRequest, e.ChangeRequestStateSHA256, e.CRStatus = stringPtr(requestID), stringPtr(requestDigest), stringPtr(crStatus)
	}
	if closure != "" {
		e.ClosureEvidenceSHA256 = stringPtr(closure)
	}
	e.EventSHA256 = eventDigest(e)
	return e
}

func validChangeRequest(id, capability, oldIdentity, newIdentity, previousDigest, currentDigest string, validFrom, deadline time.Time, status string) ChangeRequestState {
	return ChangeRequestState{Version: PolicyVersionV1, ChangeRequestID: id, Capability: capability, OldIdentity: oldIdentity, NewIdentity: newIdentity, PreviousPolicySHA256: previousDigest, CurrentPolicySHA256: currentDigest, Status: status, ValidFrom: validFrom.UTC().Format(time.RFC3339Nano), ValidUntil: deadline.UTC().Format(time.RFC3339Nano), Deadline: deadline.UTC().Format(time.RFC3339Nano)}
}

func canonicalPolicy(t *testing.T, p Policy) ([]byte, string) {
	t.Helper()
	b, d, err := p.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return b, d
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func eventDigest(event RolePolicyStateEvent) string {
	copy := event
	copy.EventSHA256 = ""
	b, _ := canonicalJSON(copy)
	return digestBytes(b)
}

func stringPtr(value string) *string { return &value }

func at(value string) time.Time { parsed, _ := time.Parse(time.RFC3339Nano, value); return parsed }

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
