package dbroles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

var eventIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
var changeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
var capabilityIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// RolePolicyStateEvent is one append-only policy state event. EventSHA256 is
// derived metadata and is intentionally not serialized; the wire hash is the
// SHA-256 of the canonical event object without a self-referential field.
type RolePolicyStateEvent struct {
	Version                  int       `json:"version"`
	Sequence                 int64     `json:"sequence"`
	EventID                  string    `json:"event_id"`
	Kind                     string    `json:"kind"`
	PreviousEventSHA256      *string   `json:"previous_event_sha256"`
	PreviousPolicySHA256     *string   `json:"previous_policy_sha256"`
	CurrentPolicySHA256      string    `json:"current_policy_sha256"`
	Capability               *string   `json:"capability"`
	FromState                *string   `json:"from_state"`
	ToState                  *string   `json:"to_state"`
	ApprovedChangeRequest    *string   `json:"approved_change_request"`
	ChangeRequestStateSHA256 *string   `json:"change_request_state_sha256"`
	CRStatus                 *string   `json:"cr_status"`
	RecordedAt               time.Time `json:"recorded_at"`
	ClosureEvidenceSHA256    *string   `json:"closure_evidence_sha256"`
	EventSHA256              string    `json:"-"`
}

// ChangeRequestState is the digest-pinned, machine-readable change request
// state consumed by rotation transitions. Times are strings to preserve the
// exact RFC3339Nano artifact representation.
type ChangeRequestState struct {
	Version               int    `json:"version"`
	ChangeRequestID       string `json:"change_request_id"`
	Capability            string `json:"capability"`
	Status                string `json:"status"`
	OldIdentity           string `json:"old_identity"`
	NewIdentity           string `json:"new_identity"`
	PreviousPolicySHA256  string `json:"previous_policy_sha256"`
	CurrentPolicySHA256   string `json:"current_policy_sha256"`
	ValidFrom             string `json:"valid_from"`
	ValidUntil            string `json:"valid_until"`
	Deadline              string `json:"deadline"`
	ClosureEvidenceSHA256 string `json:"closure_evidence_sha256"`
	ClosedAt              string `json:"closed_at"`
}

func (s ChangeRequestState) MarshalJSON() ([]byte, error) {
	type wire struct {
		Version               int    `json:"version"`
		ChangeRequestID       string `json:"change_request_id"`
		Capability            string `json:"capability"`
		Status                string `json:"status"`
		OldIdentity           string `json:"old_identity"`
		NewIdentity           string `json:"new_identity"`
		PreviousPolicySHA256  string `json:"previous_policy_sha256"`
		CurrentPolicySHA256   string `json:"current_policy_sha256"`
		ValidFrom             string `json:"valid_from"`
		ValidUntil            string `json:"valid_until"`
		Deadline              string `json:"deadline"`
		ClosureEvidenceSHA256 any    `json:"closure_evidence_sha256"`
		ClosedAt              any    `json:"closed_at"`
	}
	var closure any = s.ClosureEvidenceSHA256
	if s.ClosureEvidenceSHA256 == "" {
		closure = nil
	}
	var closed any = s.ClosedAt
	if s.ClosedAt == "" {
		closed = nil
	}
	return json.Marshal(wire{Version: s.Version, ChangeRequestID: s.ChangeRequestID, Capability: s.Capability, Status: s.Status, OldIdentity: s.OldIdentity, NewIdentity: s.NewIdentity, PreviousPolicySHA256: s.PreviousPolicySHA256, CurrentPolicySHA256: s.CurrentPolicySHA256, ValidFrom: s.ValidFrom, ValidUntil: s.ValidUntil, Deadline: s.Deadline, ClosureEvidenceSHA256: closure, ClosedAt: closed})
}

// TrustedPolicyState is the current-base state after replaying a complete
// genesis-to-terminal event chain. Events must be supplied in order; the
// validator never trusts TerminalEvent alone.
type TrustedPolicyState struct {
	PolicyBytes   []byte
	PolicySHA256  string
	TerminalEvent RolePolicyStateEvent
	Events        []RolePolicyStateEvent
}

// ProposedPolicyState is one candidate policy plus its unique event and
// digest-pinned change-request artifact.
type ProposedPolicyState struct {
	PolicyBytes              []byte
	PolicySHA256             string
	CandidateEvent           RolePolicyStateEvent
	ChangeRequestState       ChangeRequestState
	ChangeRequestStateBytes  []byte
	ChangeRequestStateSHA256 string
}

// Canonical returns event bytes and digest. EventSHA256 is excluded by the
// json:"-" tag, so callers can safely cache the returned digest.
func (e RolePolicyStateEvent) Canonical() ([]byte, string, error) {
	if err := validateEventShape(e); err != nil {
		return nil, "", err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, "", err
	}
	return b, RawDigest(b), nil
}

// Digest is a convenience alias for Canonical's SHA-256.
func (e RolePolicyStateEvent) Digest() (string, error) {
	_, digest, err := e.Canonical()
	return digest, err
}
func (e RolePolicyStateEvent) CanonicalBytes() ([]byte, string, error) { return e.Canonical() }

// LoadStateEvents strictly parses a JSONL event log, verifies canonical line
// bytes and the complete hash/sequence chain, and returns derived hashes.
func LoadStateEvents(data []byte) ([]RolePolicyStateEvent, error) {
	if len(data) == 0 {
		return nil, errors.New("dbroles events: empty artifact")
	}
	if data[len(data)-1] != '\n' {
		return nil, errors.New("dbroles events: artifact must end with LF")
	}
	if bytes.Contains(data, []byte("\r")) {
		return nil, errors.New("dbroles events: CRLF is not canonical")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) == 0 {
		return nil, errors.New("dbroles events: no events")
	}
	events := make([]RolePolicyStateEvent, 0, len(lines))
	seenIDs := map[string]bool{}
	seenTuples := map[string]bool{}
	for index, line := range lines {
		if len(line) == 0 || bytes.TrimSpace(line) == nil || !bytes.Equal(line, bytes.TrimSpace(line)) {
			return nil, fmt.Errorf("dbroles events line %d: non-canonical whitespace", index+1)
		}
		if err := rejectDuplicateJSONKeys(line); err != nil {
			return nil, fmt.Errorf("dbroles events line %d: %w", index+1, err)
		}
		var event RolePolicyStateEvent
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&event); err != nil {
			return nil, fmt.Errorf("dbroles events line %d: %w", index+1, err)
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("dbroles events line %d: trailing JSON", index+1)
		}
		canonical, digest, err := event.Canonical()
		if err != nil {
			return nil, fmt.Errorf("dbroles events line %d: %w", index+1, err)
		}
		if !bytes.Equal(canonical, line) {
			return nil, fmt.Errorf("dbroles events line %d: non-canonical JSON", index+1)
		}
		event.EventSHA256 = digest
		if seenIDs[event.EventID] {
			return nil, fmt.Errorf("dbroles events line %d: duplicate event id", index+1)
		}
		seenIDs[event.EventID] = true
		tuple := eventTuple(event)
		if seenTuples[tuple] {
			return nil, fmt.Errorf("dbroles events line %d: duplicate event tuple", index+1)
		}
		seenTuples[tuple] = true
		events = append(events, event)
	}
	if violations := validateEventChain(events); len(violations) > 0 {
		return nil, errors.New(violations[0].Message)
	}
	return events, nil
}

// LoadRolePolicyStateEvents is a descriptive alias.
func LoadRolePolicyStateEvents(data []byte) ([]RolePolicyStateEvent, error) {
	return LoadStateEvents(data)
}
func LoadStateEventLog(data []byte) ([]RolePolicyStateEvent, error)    { return LoadStateEvents(data) }
func LoadStateEventsJSONL(data []byte) ([]RolePolicyStateEvent, error) { return LoadStateEvents(data) }

// LoadChangeRequestState strictly parses one canonical CR state artifact and
// returns its exact-byte SHA-256.
func LoadChangeRequestState(data []byte) (ChangeRequestState, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return ChangeRequestState{}, "", errors.New("dbroles change request: empty artifact")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return ChangeRequestState{}, "", err
	}
	for _, field := range []string{"version", "change_request_id", "capability", "status", "old_identity", "new_identity", "previous_policy_sha256", "current_policy_sha256", "valid_from", "valid_until", "deadline", "closure_evidence_sha256", "closed_at"} {
		if !topLevelFieldPresent(data, field) {
			return ChangeRequestState{}, "", fmt.Errorf("dbroles change request: missing %s", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var state ChangeRequestState
	if err := dec.Decode(&state); err != nil {
		return ChangeRequestState{}, "", fmt.Errorf("dbroles change request decode: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return ChangeRequestState{}, "", errors.New("dbroles change request: trailing JSON")
	}
	if err := validateChangeRequestShape(state); err != nil {
		return ChangeRequestState{}, "", err
	}
	canonical, err := json.Marshal(state)
	if err != nil {
		return ChangeRequestState{}, "", err
	}
	if !bytes.Equal(canonical, data) {
		return ChangeRequestState{}, "", errors.New("dbroles change request: non-canonical JSON")
	}
	return state, RawDigest(data), nil
}
func LoadChangeRequestStateBytes(data []byte) (ChangeRequestState, string, error) {
	return LoadChangeRequestState(data)
}
func LoadChangeRequest(data []byte) (ChangeRequestState, string, error) {
	return LoadChangeRequestState(data)
}

func (s ChangeRequestState) Canonical() ([]byte, string, error) {
	if err := validateChangeRequestShape(s); err != nil {
		return nil, "", err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, "", err
	}
	return b, RawDigest(b), nil
}

// ValidateEventChain validates a supplied event sequence independently of a
// policy. It is useful to verifier callers and tests.
func ValidateEventChain(events []RolePolicyStateEvent) []Violation { return validateEventChain(events) }

func validateEventChain(events []RolePolicyStateEvent) []Violation {
	var out []Violation
	add := func(code, msg string) { out = append(out, Violation{Code: code, Message: msg}) }
	if len(events) == 0 {
		add("ROTATION_EVENT_CHAIN_EMPTY", "event chain is empty")
		return out
	}
	seenID := map[string]bool{}
	seenTuple := map[string]bool{}
	for i, event := range events {
		digest, err := event.Digest()
		if err != nil {
			add("ROTATION_EVENT_INVALID", err.Error())
			continue
		}
		if event.EventSHA256 != "" && event.EventSHA256 != digest {
			add("ROTATION_EVENT_HASH_MISMATCH", "event hash does not match canonical bytes")
		}
		if seenID[event.EventID] {
			add("ROTATION_EVENT_REPLAY", "event id was replayed")
		}
		seenID[event.EventID] = true
		tuple := eventTuple(event)
		if seenTuple[tuple] {
			add("ROTATION_EVENT_REPLAY", "event tuple was duplicated")
		}
		seenTuple[tuple] = true
		if event.Sequence != int64(i+1) {
			add("ROTATION_EVENT_SEQUENCE_INVALID", "event sequence must increase by one")
		}
		if i == 0 {
			if event.Kind != EventKindGenesis {
				add("ROTATION_GENESIS_REQUIRED", "event chain must start with genesis")
			}
			if event.PreviousEventSHA256 != nil || event.PreviousPolicySHA256 != nil {
				add("ROTATION_GENESIS_PREVIOUS_NOT_NULL", "genesis previous fields must be null")
			}
			if event.Capability != nil || event.FromState != nil || event.ApprovedChangeRequest != nil || event.ChangeRequestStateSHA256 != nil || event.CRStatus != nil || event.ClosureEvidenceSHA256 != nil {
				add("ROTATION_GENESIS_FIELDS_INVALID", "genesis rotation/CR fields must be null")
			}
			if event.ToState == nil || *event.ToState != RotationSteadyA {
				add("ROTATION_GENESIS_STATE_INVALID", "genesis must establish steady-a policy state")
			}
		} else {
			prev := events[i-1]
			prevDigest, _ := prev.Digest()
			if event.PreviousEventSHA256 == nil || *event.PreviousEventSHA256 != prevDigest {
				add("ROTATION_EVENT_CHAIN_BROKEN", "previous event digest does not match terminal predecessor")
			}
			if event.PreviousPolicySHA256 == nil || *event.PreviousPolicySHA256 == "" {
				add("ROTATION_EVENT_PREVIOUS_POLICY_REQUIRED", "non-genesis event must bind previous policy")
			}
			if event.PreviousPolicySHA256 != nil && *event.PreviousPolicySHA256 != prev.CurrentPolicySHA256 {
				add("ROTATION_EVENT_PREVIOUS_POLICY_MISMATCH", "previous policy digest does not match predecessor")
			}
			if event.CurrentPolicySHA256 == prev.CurrentPolicySHA256 {
				add("ROTATION_POLICY_UPDATE_NOOP", "non-genesis event must advance policy bytes")
			}
		}
	}
	return sortViolations(out)
}

// ValidateTransition validates exactly one candidate event against a trusted
// current-base chain. It is pure and performs no filesystem/network/DB work.
func ValidateTransition(previous TrustedPolicyState, current ProposedPolicyState, now time.Time) []Violation {
	var violations []Violation
	add := func(code, capability, identity, object, message string) {
		violations = append(violations, Violation{Code: code, Capability: capability, Identity: identity, Object: object, Message: message})
	}
	if now.IsZero() || now.Location() != time.UTC {
		add("ROTATION_NOW_INVALID", "", "", "", "validation time must be explicit UTC")
	}
	if len(previous.PolicyBytes) == 0 || previous.PolicySHA256 == "" || RawDigest(previous.PolicyBytes) != previous.PolicySHA256 {
		add("ROTATION_PREVIOUS_POLICY_DIGEST_MISMATCH", "", "", "", "trusted previous policy bytes/digest mismatch")
	}
	if len(previous.PolicyBytes) > 0 {
		if previousPolicy, previousErr := loadPolicyUnchecked(previous.PolicyBytes); previousErr == nil {
			for _, v := range previousPolicy.ValidateCurrentAt(now) {
				violations = append(violations, v)
			}
		}
	}
	if len(previous.Events) == 0 {
		add("ROTATION_EVENT_CHAIN_EMPTY", "", "", "", "trusted previous event chain is empty")
	} else {
		chain := validateEventChain(previous.Events)
		violations = append(violations, chain...)
		terminal := previous.Events[len(previous.Events)-1]
		terminalDigest, _ := terminal.Digest()
		if previous.TerminalEvent.EventID != "" && previous.TerminalEvent.EventID != terminal.EventID {
			add("ROTATION_EVENT_TERMINAL_MISMATCH", "", "", "", "caller terminal event is not chain terminal")
		}
		if previous.TerminalEvent.EventSHA256 != "" && previous.TerminalEvent.EventSHA256 != terminalDigest {
			add("ROTATION_EVENT_TERMINAL_MISMATCH", "", "", "", "caller terminal event hash mismatch")
		}
		if previous.TerminalEvent.EventID != "" {
			callerDigest, callerErr := previous.TerminalEvent.Digest()
			if callerErr != nil || callerDigest != terminalDigest {
				add("ROTATION_EVENT_TERMINAL_MISMATCH", "", "", "", "caller terminal event bytes differ from chain terminal")
			}
		}
		if terminal.CurrentPolicySHA256 != previous.PolicySHA256 {
			add("ROTATION_PREVIOUS_POLICY_DIGEST_MISMATCH", "", "", "", "terminal event policy digest differs from trusted policy")
		}
		if previous.TerminalEvent.EventID == "" {
			add("ROTATION_EVENT_TERMINAL_MISMATCH", "", "", "", "trusted terminal event is required")
		}
	}
	if len(current.PolicyBytes) == 0 || current.PolicySHA256 == "" || RawDigest(current.PolicyBytes) != current.PolicySHA256 {
		add("ROTATION_CURRENT_POLICY_DIGEST_MISMATCH", "", "", "", "candidate policy bytes/digest mismatch")
	}
	policy, err := loadPolicyUnchecked(current.PolicyBytes)
	if err != nil {
		add("ROTATION_CURRENT_POLICY_INVALID", "", "", "", "candidate policy is invalid: "+err.Error())
	} else {
		for _, v := range policy.ValidateCurrentAt(now) {
			violations = append(violations, v)
		}
	}
	if len(current.ChangeRequestStateBytes) == 0 {
		add("ROTATION_CR_STATE_MISSING", "", "", "", "change request state artifact is required")
	}
	var cr ChangeRequestState
	var crDigest string
	var crErr error
	if len(current.ChangeRequestStateBytes) > 0 {
		cr, crDigest, crErr = LoadChangeRequestState(current.ChangeRequestStateBytes)
	} else {
		crErr = errors.New("missing artifact")
	}
	if crErr != nil {
		add("ROTATION_CR_STATE_INVALID", "", "", "", "change request state invalid: "+crErr.Error())
	} else {
		if current.ChangeRequestStateSHA256 == "" || current.ChangeRequestStateSHA256 != crDigest {
			add("ROTATION_CR_STATE_DIGEST_MISMATCH", "", "", "", "change request state digest mismatch")
		}
		if current.ChangeRequestState != cr {
			add("ROTATION_CR_STATE_DIGEST_MISMATCH", "", "", "", "provided change request state differs from bytes")
		}
		current.ChangeRequestState = cr
	}
	event := current.CandidateEvent
	eventDigest, eventErr := event.Digest()
	if eventErr != nil {
		add("ROTATION_EVENT_INVALID", "", "", "", "candidate event invalid: "+eventErr.Error())
	}
	if event.EventSHA256 != "" && event.EventSHA256 != eventDigest {
		add("ROTATION_EVENT_HASH_MISMATCH", "", "", "", "candidate event hash mismatch")
	}
	if len(previous.Events) > 0 {
		terminal := previous.Events[len(previous.Events)-1]
		terminalDigest, _ := terminal.Digest()
		if event.Sequence != terminal.Sequence+1 {
			add("ROTATION_EVENT_SEQUENCE_INVALID", "", "", "", "candidate sequence must be terminal+1")
		}
		if event.PreviousEventSHA256 == nil || *event.PreviousEventSHA256 != terminalDigest {
			add("ROTATION_EVENT_CHAIN_BROKEN", "", "", "", "candidate previous event digest mismatch")
		}
		for _, old := range previous.Events {
			oldDigest, _ := old.Digest()
			if event.EventID == old.EventID || eventDigest == oldDigest {
				add("ROTATION_EVENT_REPLAY", "", "", "", "candidate event was replayed")
			}
		}
		if event.PreviousPolicySHA256 == nil || *event.PreviousPolicySHA256 != previous.PolicySHA256 {
			add("ROTATION_PREVIOUS_POLICY_DIGEST_MISMATCH", "", "", "", "candidate previous policy digest mismatch")
		}
	}
	if event.CurrentPolicySHA256 != current.PolicySHA256 {
		add("ROTATION_CURRENT_POLICY_DIGEST_MISMATCH", "", "", "", "candidate event current policy digest mismatch")
	}
	if len(previous.PolicyBytes) > 0 && bytes.Equal(previous.PolicyBytes, current.PolicyBytes) && event.Kind != EventKindPolicyUpdate {
		add("ROTATION_POLICY_NO_CHANGE", "", "", "", "rotation event must change exact policy bytes")
	}
	if event.Kind == EventKindGenesis {
		add("ROTATION_EVENT_KIND_INVALID", "", "", "", "genesis cannot be appended")
	}
	if event.Kind != EventKindPolicyUpdate && event.Kind != EventKindRotationStart && event.Kind != EventKindRotationClose {
		add("ROTATION_EVENT_KIND_INVALID", "", "", "", "unknown candidate event kind")
	}
	if event.Kind == EventKindRotationStart || event.Kind == EventKindRotationClose {
		if event.Capability == nil || event.FromState == nil || event.ToState == nil {
			add("ROTATION_EVENT_FIELDS_INVALID", "", "", "", "rotation event requires capability/from_state/to_state")
		} else if policy.Version == PolicyVersionV1 {
			validateRotationTransition(previous, policy, current.PolicySHA256, event, cr, now, add)
		}
		if event.ApprovedChangeRequest == nil || event.ChangeRequestStateSHA256 == nil || event.CRStatus == nil {
			add("ROTATION_CR_BINDING_REQUIRED", "", "", "", "rotation event requires CR binding fields")
		} else {
			if *event.ApprovedChangeRequest != cr.ChangeRequestID {
				capability := ""
				if event.Capability != nil {
					capability = *event.Capability
				}
				add("ROTATION_CR_BINDING_MISMATCH", capability, "", "", "event CR id does not match artifact")
			}
			if *event.ChangeRequestStateSHA256 != crDigest {
				add("ROTATION_CR_STATE_DIGEST_MISMATCH", "", "", "", "event CR digest does not match artifact")
			}
			if event.Kind == EventKindRotationStart && *event.CRStatus != "approved" {
				add("ROTATION_CR_NOT_APPROVED", "", "", "", "rotation start requires approved CR")
			}
			if event.Kind == EventKindRotationClose && *event.CRStatus != "closed" {
				add("ROTATION_CR_NOT_CLOSED", "", "", "", "rotation close requires closed CR")
			}
		}
	}
	if event.Kind == EventKindPolicyUpdate {
		if event.Capability != nil || event.FromState != nil || event.ToState != nil || event.ClosureEvidenceSHA256 != nil {
			add("ROTATION_POLICY_UPDATE_FIELDS_INVALID", "", "", "", "policy-update cannot carry rotation fields")
		}
		if len(previous.PolicyBytes) > 0 && len(current.PolicyBytes) > 0 {
			oldPolicy, oldErr := loadPolicyUnchecked(previous.PolicyBytes)
			if oldErr == nil && err == nil && !rotationMapsEqual(oldPolicy, policy) {
				add("ROTATION_POLICY_UPDATE_ROTATION_CHANGED", "", "", "", "policy-update may not change rotation state")
			}
			if bytes.Equal(previous.PolicyBytes, current.PolicyBytes) {
				add("ROTATION_POLICY_UPDATE_NOOP", "", "", "", "policy-update must change exact policy bytes")
			}
		}
	}
	return sortViolations(violations)
}

func validateRotationTransition(previous TrustedPolicyState, current Policy, currentRawDigest string, event RolePolicyStateEvent, cr ChangeRequestState, now time.Time, add func(string, string, string, string, string)) {
	capability := *event.Capability
	previousPolicy, _, err := LoadPolicy(previous.PolicyBytes)
	if err != nil {
		return
	}
	old, ok := previousPolicy.Rotations[capability]
	if !ok {
		add("ROTATION_CAPABILITY_UNKNOWN", capability, "", "", "unknown rotation capability")
		return
	}
	next, ok := current.Rotations[capability]
	if !ok {
		add("ROTATION_CAPABILITY_UNKNOWN", capability, "", "", "candidate rotation capability missing")
		return
	}
	if event.FromState == nil || event.ToState == nil {
		return
	}
	if old.State != *event.FromState {
		add("ROTATION_FROM_STATE_MISMATCH", capability, "", "", "event from_state does not match previous policy")
	}
	if next.State != *event.ToState {
		add("ROTATION_TO_STATE_MISMATCH", capability, "", "", "event to_state does not match candidate policy")
	}
	if event.Kind == EventKindRotationStart && (old.State != RotationSteadyA || next.State != RotationRotatingAB) {
		if old.State == RotationSteadyA && next.State == RotationSteadyB {
			add("ROTATION_TRANSITION_SKIPPED", capability, "", "", "steady-a to steady-b is not allowed")
		} else {
			add("ROTATION_TRANSITION_INVALID", capability, "", "", "rotation-start must be steady-a to rotating-a-b")
		}
	}
	if old.State == RotationSteadyA && next.State == RotationSteadyB {
		add("ROTATION_TRANSITION_SKIPPED", capability, "", "", "steady-a to steady-b is not allowed")
	}
	if event.Kind == EventKindRotationClose && (old.State != RotationRotatingAB || next.State != RotationSteadyB) {
		add("ROTATION_TRANSITION_INVALID", capability, "", "", "rotation-close must be rotating-a-b to steady-b")
	}
	if cr.Capability != capability || cr.OldIdentity != next.OldIdentity || cr.NewIdentity != next.NewIdentity {
		add("ROTATION_CR_BINDING_MISMATCH", capability, "", "", "CR capability/identity binding mismatch")
	}
	if cr.PreviousPolicySHA256 != previous.PolicySHA256 || cr.CurrentPolicySHA256 != currentRawDigest {
		add("ROTATION_CR_POLICY_DIGEST_MISMATCH", capability, "", "", "CR policy digest binding mismatch")
	}
	validFrom, validFromOK := parseUTC(cr.ValidFrom)
	validUntil, validUntilOK := parseUTC(cr.ValidUntil)
	deadline, deadlineOK := parseUTC(cr.Deadline)
	if !validFromOK || !validUntilOK || !deadlineOK {
		add("ROTATION_CR_TIME_INVALID", capability, "", "", "CR validity/deadline must be UTC RFC3339Nano")
	} else {
		if event.Kind == EventKindRotationStart {
			if next.StartedAt != cr.ValidFrom || next.Deadline != cr.Deadline {
				add("ROTATION_CR_TIME_BINDING_MISMATCH", capability, "", "", "rotation start times must match CR")
			}
		}
		if event.Kind == EventKindRotationClose {
			if next.StartedAt != cr.ValidFrom || next.Deadline != cr.Deadline || next.ClosedAt != cr.ClosedAt {
				add("ROTATION_CR_TIME_BINDING_MISMATCH", capability, "", "", "rotation closure times must match CR")
			}
		}
		if !validFrom.Before(validUntil) || !validFrom.Before(deadline) {
			add("ROTATION_CR_TIME_INVALID", capability, "", "", "CR validity window is not ordered")
		}
		if event.Kind == EventKindRotationStart && (now.Before(validFrom) || !now.Before(validUntil) || !now.Before(deadline)) {
			add("ROTATION_DEADLINE_EXPIRED", capability, "", "", "rotation start is outside approved window")
		}
		if event.Kind == EventKindRotationClose {
			closed, ok := parseUTC(cr.ClosedAt)
			if !ok || closed.After(deadline) || !closed.After(validFrom) {
				add("ROTATION_CLOSURE_TIME_INVALID", capability, "", "", "rotation closure is outside deadline")
			}
			if cr.Status != "closed" {
				add("ROTATION_CR_NOT_CLOSED", capability, "", "", "CR must be closed")
			}
			if next.ClosureEvidence == "" || !shaPrefixPattern.MatchString(next.ClosureEvidence) {
				add("ROTATION_CLOSURE_DIGEST_MISMATCH", capability, "", "", "closure evidence is missing or malformed")
			} else if strings.TrimPrefix(next.ClosureEvidence, "sha256:") != cr.ClosureEvidenceSHA256 {
				add("ROTATION_CLOSURE_DIGEST_MISMATCH", capability, "", "", "closure evidence digest mismatch")
			}
			if event.ClosureEvidenceSHA256 == nil || *event.ClosureEvidenceSHA256 != cr.ClosureEvidenceSHA256 {
				add("ROTATION_CLOSURE_DIGEST_MISMATCH", capability, "", "", "event closure digest mismatch")
			}
		}
	}
}

func rotationMapsEqual(a, b Policy) bool { return mapsEqual(a.Rotations, b.Rotations) }
func mapsEqual(a, b map[string]RotationSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func eventTuple(e RolePolicyStateEvent) string {
	b, err := json.Marshal(e)
	if err == nil {
		return RawDigest(b)
	}
	return fmt.Sprintf("%d\x00%s\x00%s", e.Sequence, e.EventID, e.Kind)
}
func nullableString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func validateEventShape(e RolePolicyStateEvent) error {
	if e.Version != PolicyVersionV1 {
		return fmt.Errorf("event version must be %d", PolicyVersionV1)
	}
	if e.Sequence < 1 {
		return errors.New("event sequence must be positive")
	}
	if e.EventID == "" || !eventIDPattern.MatchString(e.EventID) {
		return errors.New("event_id is required")
	}
	if e.CurrentPolicySHA256 == "" || !digestPattern.MatchString(e.CurrentPolicySHA256) {
		return errors.New("current_policy_sha256 must be lowercase SHA-256")
	}
	if e.RecordedAt.IsZero() || e.RecordedAt.Location() != time.UTC {
		return errors.New("recorded_at must be UTC")
	}
	for label, value := range map[string]*string{"previous_event_sha256": e.PreviousEventSHA256, "previous_policy_sha256": e.PreviousPolicySHA256, "change_request_state_sha256": e.ChangeRequestStateSHA256, "closure_evidence_sha256": e.ClosureEvidenceSHA256} {
		if value != nil && !digestPattern.MatchString(*value) {
			return fmt.Errorf("%s must be lowercase SHA-256 or null", label)
		}
	}
	if e.Capability != nil && !capabilityIDPattern.MatchString(*e.Capability) {
		return errors.New("capability is not canonical")
	}
	for label, value := range map[string]*string{"from_state": e.FromState, "to_state": e.ToState} {
		if value != nil && *value != RotationSteadyA && *value != RotationRotatingAB && *value != RotationSteadyB {
			return fmt.Errorf("%s is not a legal rotation state", label)
		}
	}
	if e.ApprovedChangeRequest != nil && !changeIDPattern.MatchString(*e.ApprovedChangeRequest) {
		return errors.New("approved_change_request is not canonical")
	}
	if e.CRStatus != nil && *e.CRStatus != "approved" && *e.CRStatus != "closed" {
		return errors.New("cr_status is not legal")
	}
	switch e.Kind {
	case EventKindGenesis:
		if e.PreviousEventSHA256 != nil || e.PreviousPolicySHA256 != nil || e.Capability != nil || e.FromState != nil || e.ApprovedChangeRequest != nil || e.ChangeRequestStateSHA256 != nil || e.CRStatus != nil || e.ClosureEvidenceSHA256 != nil {
			return errors.New("genesis nullable fields must be null")
		}
	case EventKindPolicyUpdate:
		if e.Capability != nil || e.FromState != nil || e.ToState != nil || e.ApprovedChangeRequest != nil || e.ChangeRequestStateSHA256 != nil || e.CRStatus != nil || e.ClosureEvidenceSHA256 != nil {
			return errors.New("policy-update cannot carry rotation state")
		}
	case EventKindRotationStart, EventKindRotationClose:
		if e.Capability == nil || e.FromState == nil || e.ToState == nil || *e.Capability == "" || *e.FromState == "" || *e.ToState == "" {
			return errors.New("rotation event requires capability/from_state/to_state")
		}
		if e.ApprovedChangeRequest == nil || e.ChangeRequestStateSHA256 == nil || e.CRStatus == nil {
			return errors.New("rotation event requires CR binding")
		}
		if e.Kind == EventKindRotationStart && *e.CRStatus != "approved" {
			return errors.New("rotation-start CR status must be approved")
		}
		if e.Kind == EventKindRotationClose && (*e.CRStatus != "closed" || e.ClosureEvidenceSHA256 == nil) {
			return errors.New("rotation-close CR status/closure binding invalid")
		}
	default:
		return errors.New("unknown event kind")
	}
	return nil
}
func validateChangeRequestShape(s ChangeRequestState) error {
	if s.Version != PolicyVersionV1 {
		return fmt.Errorf("dbroles change request: unsupported version %d", s.Version)
	}
	if !changeIDPattern.MatchString(s.ChangeRequestID) || !capabilityIDPattern.MatchString(s.Capability) || !rolePattern.MatchString(s.OldIdentity) || !rolePattern.MatchString(s.NewIdentity) {
		return errors.New("dbroles change request: id/capability/identities required")
	}
	if s.Status != "approved" && s.Status != "closed" {
		return errors.New("dbroles change request: status must be approved or closed")
	}
	for field, value := range map[string]string{"previous_policy_sha256": s.PreviousPolicySHA256, "current_policy_sha256": s.CurrentPolicySHA256} {
		if !digestPattern.MatchString(value) {
			return fmt.Errorf("dbroles change request: %s invalid", field)
		}
	}
	parsed := map[string]time.Time{}
	for field, value := range map[string]string{"valid_from": s.ValidFrom, "valid_until": s.ValidUntil, "deadline": s.Deadline} {
		if parsed[field], _ = parseUTC(value); parsed[field].IsZero() {
			return fmt.Errorf("dbroles change request: %s invalid", field)
		}
	}
	if !parsed["valid_from"].Before(parsed["valid_until"]) || !parsed["valid_from"].Before(parsed["deadline"]) || parsed["deadline"].After(parsed["valid_until"]) {
		return errors.New("dbroles change request: validity/deadline window is not ordered")
	}
	if s.ClosureEvidenceSHA256 != "" && !digestPattern.MatchString(s.ClosureEvidenceSHA256) {
		return errors.New("dbroles change request: closure evidence digest invalid")
	}
	if s.ClosedAt != "" {
		if _, ok := parseUTC(s.ClosedAt); !ok {
			return errors.New("dbroles change request: closed_at invalid")
		}
	}
	if s.Status == "approved" && (s.ClosureEvidenceSHA256 != "" || s.ClosedAt != "") {
		return errors.New("dbroles change request: approved state cannot contain closure fields")
	}
	if s.Status == "closed" && (s.ClosureEvidenceSHA256 == "" || s.ClosedAt == "") {
		return errors.New("dbroles change request: closed state requires closure fields")
	}
	return nil
}
func parseUTC(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || !strings.HasSuffix(value, "Z") || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, false
	}
	return parsed, true
}
func sortViolations(v []Violation) []Violation {
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Code != v[j].Code {
			return v[i].Code < v[j].Code
		}
		if v[i].Capability != v[j].Capability {
			return v[i].Capability < v[j].Capability
		}
		if v[i].Identity != v[j].Identity {
			return v[i].Identity < v[j].Identity
		}
		if v[i].Object != v[j].Object {
			return v[i].Object < v[j].Object
		}
		return v[i].Message < v[j].Message
	})
	return v
}
