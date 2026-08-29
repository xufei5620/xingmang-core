package finance

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestChannelRefNormalizesOpaqueIDWithoutRewritingIt(t *testing.T) {
	serviceID := uuid.New()
	ref, err := NewChannelRef(serviceID, "  001-AbC  ")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ExternalChannelID != "001-AbC" {
		t.Fatalf("normalized id = %q", ref.ExternalChannelID)
	}
	if _, err := NewChannelRef(serviceID, "   "); err == nil {
		t.Fatal("accepted blank channel id")
	}
}

func TestEvaluateBindingCandidatesFourStates(t *testing.T) {
	serviceID := uuid.New()
	accountA, accountB := uuid.New(), uuid.New()
	inventory := InventorySnapshot{
		ServiceID: serviceID, Known: true, Complete: true,
		Channels: []InventoryChannel{{ExternalChannelID: "unmapped"}, {ExternalChannelID: "candidate"}, {ExternalChannelID: "conflict"}},
	}
	evidence := []TokenMapEvidence{
		{ExternalChannelID: "candidate", UpstreamAccountID: accountA, ActiveServiceCount: 1, SystemTypeMatches: true},
		{ExternalChannelID: "conflict", UpstreamAccountID: accountA, ActiveServiceCount: 1, SystemTypeMatches: true},
		{ExternalChannelID: "conflict", UpstreamAccountID: accountB, ActiveServiceCount: 1, SystemTypeMatches: true},
		{ExternalChannelID: "orphan", UpstreamAccountID: accountA, ActiveServiceCount: 1, SystemTypeMatches: true},
	}
	got := EvaluateBindingCandidates(inventory, nil, evidence)
	states := map[string]CandidateState{}
	for _, candidate := range got {
		states[candidate.Channel.ExternalChannelID] = candidate.State
	}
	if states["unmapped"] != CandidateUnmapped || states["candidate"] != CandidateProposed ||
		states["conflict"] != CandidateConflict || states["orphan"] != CandidateOrphan {
		t.Fatalf("states=%v", states)
	}
}

func TestIncompleteInventoryNeverCreatesOrphan(t *testing.T) {
	serviceID := uuid.New()
	got := EvaluateBindingCandidates(
		InventorySnapshot{ServiceID: serviceID, Known: true, Complete: false},
		nil,
		[]TokenMapEvidence{{ExternalChannelID: "missing", UpstreamAccountID: uuid.New(), ActiveServiceCount: 1, SystemTypeMatches: true}},
	)
	if len(got) != 1 || got[0].State == CandidateOrphan || !got[0].InventoryUnknown {
		t.Fatalf("candidates=%+v", got)
	}
}

func TestTokenEvidenceNeedsExactlyOneMatchingActiveService(t *testing.T) {
	serviceID := uuid.New()
	for _, evidence := range []TokenMapEvidence{
		{ExternalChannelID: "x", UpstreamAccountID: uuid.New(), ActiveServiceCount: 0, SystemTypeMatches: true},
		{ExternalChannelID: "x", UpstreamAccountID: uuid.New(), ActiveServiceCount: 2, SystemTypeMatches: true},
		{ExternalChannelID: "x", UpstreamAccountID: uuid.New(), ActiveServiceCount: 1, SystemTypeMatches: false},
	} {
		got := EvaluateBindingCandidates(InventorySnapshot{ServiceID: serviceID, Known: true, Complete: true}, nil, []TokenMapEvidence{evidence})
		if len(got) != 1 || got[0].EvidenceStatus == EvidenceSufficient {
			t.Fatalf("evidence=%+v candidate=%+v", evidence, got)
		}
	}
}

func TestTokenEvidenceWithoutActiveServiceIsOrphanWithInsufficientEvidence(t *testing.T) {
	serviceID := uuid.New()
	got := EvaluateBindingCandidates(
		InventorySnapshot{ServiceID: serviceID, Known: true, Complete: true},
		nil,
		[]TokenMapEvidence{{ExternalChannelID: "legacy-1", UpstreamAccountID: uuid.New(), ActiveServiceCount: 0, SystemTypeMatches: true}},
	)
	if len(got) != 1 {
		t.Fatalf("candidates=%+v", got)
	}
	if got[0].State != CandidateOrphan || got[0].EvidenceStatus != EvidenceInsufficient {
		t.Fatalf("zero active services must remain an insufficient orphan: %+v", got[0])
	}
	if !slices.Contains(got[0].ReasonCodes, "no_active_service") {
		t.Fatalf("missing no_active_service reason: %+v", got[0].ReasonCodes)
	}
}

func TestTokenEvidenceSystemTypeMismatchIsConflict(t *testing.T) {
	serviceID := uuid.New()
	got := EvaluateBindingCandidates(
		InventorySnapshot{ServiceID: serviceID, ServiceType: "newapi", Known: true, Complete: true},
		nil,
		[]TokenMapEvidence{{ExternalChannelID: "legacy-1", UpstreamAccountID: uuid.New(), ActiveServiceCount: 1, SystemType: "sub2api", SystemTypeMatches: true}},
	)
	if len(got) != 1 || got[0].State != CandidateConflict || got[0].EvidenceStatus != EvidenceConflicting {
		t.Fatalf("system type mismatch must conflict: %+v", got)
	}
	if !slices.Contains(got[0].ReasonCodes, "system_type_mismatch") {
		t.Fatalf("missing system_type_mismatch reason: %+v", got[0].ReasonCodes)
	}
}
