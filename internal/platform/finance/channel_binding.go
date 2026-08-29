package finance

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrBindingConflict     = errors.New("platform channel binding conflict")
	ErrBindingPrecondition = errors.New("platform channel binding precondition failed")
)

type ChannelRef struct {
	ServiceID         uuid.UUID
	ExternalChannelID string
}

func NewChannelRef(serviceID uuid.UUID, externalID string) (ChannelRef, error) {
	trimmed := strings.TrimSpace(externalID)
	if serviceID == uuid.Nil || trimmed == "" || len(trimmed) > 256 {
		return ChannelRef{}, ErrInvalidFormat
	}
	return ChannelRef{ServiceID: serviceID, ExternalChannelID: trimmed}, nil
}

type PlatformChannelBinding struct {
	ID                uuid.UUID
	Environment       string
	Channel           ChannelRef
	UpstreamAccountID uuid.UUID
	ValidFrom         time.Time
	ValidTo           *time.Time
	Provenance        string
	Reason            string
	CreatedBy         string
	CreatedAt         time.Time
}

type SetBindingResult struct {
	Binding  PlatformChannelBinding
	Previous *PlatformChannelBinding
	Changed  bool
}

type CandidateState string

const (
	CandidateUnmapped CandidateState = "unmapped"
	CandidateProposed CandidateState = "candidate"
	CandidateConflict CandidateState = "conflict"
	CandidateOrphan   CandidateState = "orphan"
)

type CandidateEvidenceStatus string

const (
	EvidenceSufficient   CandidateEvidenceStatus = "sufficient"
	EvidenceInsufficient CandidateEvidenceStatus = "insufficient"
	EvidenceConflicting  CandidateEvidenceStatus = "conflicting"
)

type InventoryChannel struct {
	ExternalChannelID string
	Name              string
}

type InventorySnapshot struct {
	ServiceID uuid.UUID
	// ServiceType identifies the managed connector whose directory is being
	// evaluated. It is kept alongside ServiceID so token-map evidence cannot be
	// proposed merely because an external channel id happens to match.
	ServiceType string
	Known       bool
	Complete    bool
	Channels    []InventoryChannel
}

type TokenMapEvidence struct {
	ExternalChannelID string
	UpstreamAccountID uuid.UUID
	// SystemType is the type recorded on the upstream account. An empty value is
	// accepted for legacy callers, while a non-empty mismatch is explicit
	// conflict evidence.
	SystemType                string
	ActiveServiceCount        int
	SystemTypeMatches         bool
	PlatformAssignmentMissing bool
}

type ChannelCandidate struct {
	Channel                   ChannelRef
	Name                      string
	State                     CandidateState
	EvidenceStatus            CandidateEvidenceStatus
	UpstreamAccountIDs        []uuid.UUID
	ReasonCodes               []string
	PlatformAssignmentMissing bool
	InventoryUnknown          bool
}

func EvaluateBindingCandidates(
	inventory InventorySnapshot,
	confirmed []PlatformChannelBinding,
	evidence []TokenMapEvidence,
) []ChannelCandidate {
	type accumulator struct {
		name            string
		inInventory     bool
		targets         map[uuid.UUID]struct{}
		reasons         map[string]struct{}
		platformMissing bool
		hasConfirmed    bool
	}
	byID := map[string]*accumulator{}
	ensure := func(raw string) (string, *accumulator) {
		id := strings.TrimSpace(raw)
		acc := byID[id]
		if acc == nil {
			acc = &accumulator{targets: map[uuid.UUID]struct{}{}, reasons: map[string]struct{}{}}
			byID[id] = acc
		}
		return id, acc
	}
	for _, channel := range inventory.Channels {
		id, acc := ensure(channel.ExternalChannelID)
		if id == "" {
			continue
		}
		acc.inInventory = true
		acc.name = channel.Name
	}
	for _, binding := range confirmed {
		if binding.Channel.ServiceID != inventory.ServiceID {
			continue
		}
		_, acc := ensure(binding.Channel.ExternalChannelID)
		acc.hasConfirmed = true
	}
	for _, item := range evidence {
		id, acc := ensure(item.ExternalChannelID)
		if id == "" {
			continue
		}
		acc.platformMissing = acc.platformMissing || item.PlatformAssignmentMissing
		switch {
		case item.ActiveServiceCount == 0:
			acc.reasons["no_active_service"] = struct{}{}
		case item.ActiveServiceCount > 1:
			acc.reasons["ambiguous_service"] = struct{}{}
		case !item.SystemTypeMatches || (item.SystemType != "" && inventory.ServiceType != "" && item.SystemType != inventory.ServiceType):
			acc.reasons["system_type_mismatch"] = struct{}{}
		case item.UpstreamAccountID != uuid.Nil:
			acc.targets[item.UpstreamAccountID] = struct{}{}
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]ChannelCandidate, 0, len(ids))
	for _, id := range ids {
		acc := byID[id]
		targets := make([]uuid.UUID, 0, len(acc.targets))
		for target := range acc.targets {
			targets = append(targets, target)
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].String() < targets[j].String() })
		reasons := make([]string, 0, len(acc.reasons))
		for reason := range acc.reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)

		candidate := ChannelCandidate{
			Channel: ChannelRef{ServiceID: inventory.ServiceID, ExternalChannelID: id},
			Name:    acc.name, UpstreamAccountIDs: targets, ReasonCodes: reasons,
			PlatformAssignmentMissing: acc.platformMissing,
		}
		hardConflict := len(targets) > 1
		for _, reason := range reasons {
			if reason == "ambiguous_service" || reason == "system_type_mismatch" {
				hardConflict = true
				break
			}
		}
		hasNoService := false
		for _, reason := range reasons {
			if reason == "no_active_service" {
				hasNoService = true
				break
			}
		}
		switch {
		case hardConflict:
			candidate.State = CandidateConflict
			candidate.EvidenceStatus = EvidenceConflicting
		case hasNoService:
			// A token-map row without a matching active service is retained as
			// an orphan for investigation, but its evidence is insufficient.
			// Incomplete inventory still wins: absence cannot prove orphanage.
			if !acc.inInventory && (!inventory.Known || !inventory.Complete) {
				candidate.State = CandidateUnmapped
				candidate.EvidenceStatus = EvidenceInsufficient
				candidate.InventoryUnknown = true
			} else {
				candidate.State = CandidateOrphan
				candidate.EvidenceStatus = EvidenceInsufficient
			}
		case !acc.inInventory && (!inventory.Known || !inventory.Complete):
			candidate.State = CandidateUnmapped
			candidate.EvidenceStatus = EvidenceInsufficient
			candidate.InventoryUnknown = true
		case !acc.inInventory && (len(targets) > 0 || acc.hasConfirmed):
			candidate.State = CandidateOrphan
			candidate.EvidenceStatus = EvidenceSufficient
		case len(targets) == 1:
			candidate.State = CandidateProposed
			candidate.EvidenceStatus = EvidenceSufficient
		default:
			candidate.State = CandidateUnmapped
			candidate.EvidenceStatus = EvidenceInsufficient
		}
		out = append(out, candidate)
	}
	return out
}
