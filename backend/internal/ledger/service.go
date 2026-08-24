package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"invoice-system/backend/internal/domain"
)

type AllocationInput struct {
	FundingLotID string `json:"funding_lot_id"`
	AmountMinor  int64  `json:"amount_minor"`
}

type SubmitInput struct {
	PrincipalID      string            `json:"principal_id"`
	ProfileID        string            `json:"profile_id"`
	SourceInstanceID string            `json:"source_instance_id"`
	IdempotencyKey   string            `json:"idempotency_key"`
	Allocations      []AllocationInput `json:"allocations"`
}

type Service struct {
	mu                  sync.Mutex
	now                 func() time.Time
	lots                map[string]domain.FundingLot
	profiles            map[string]domain.InvoiceProfile
	requests            map[string]domain.InvoiceRequest
	documents           map[string]domain.InvoiceDocument
	outbox              map[string]domain.EmailOutbox
	idempotency         map[string]string
	minimumRequestMinor int64
}

func NewService() *Service {
	return &Service{
		now: time.Now, lots: map[string]domain.FundingLot{}, profiles: map[string]domain.InvoiceProfile{},
		requests: map[string]domain.InvoiceRequest{}, documents: map[string]domain.InvoiceDocument{},
		outbox: map[string]domain.EmailOutbox{}, idempotency: map[string]string{},
		minimumRequestMinor: domain.MinimumRequestMinor,
	}
}

func (s *Service) SetMinimumRequestMinor(value int64) error {
	if value < domain.MinimumRequestMinor {
		return domain.ErrMinimumAmount
	}
	s.mu.Lock()
	s.minimumRequestMinor = value
	s.mu.Unlock()
	return nil
}
func (s *Service) MinimumRequestMinor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.minimumRequestMinor
}

func randomID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func (s *Service) AddFundingLot(_ context.Context, lot domain.FundingLot) error {
	if err := lot.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.lots[lot.ID]; exists {
		return domain.ErrConflict
	}
	now := s.now()
	if lot.ObservedAt.IsZero() {
		lot.ObservedAt = now
	}
	if lot.UpdatedAt.IsZero() {
		lot.UpdatedAt = now
	}
	s.lots[lot.ID] = lot
	return nil
}

func (s *Service) SaveProfile(_ context.Context, profile domain.InvoiceProfile) (domain.InvoiceProfile, error) {
	if err := profile.Validate(); err != nil {
		return domain.InvoiceProfile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if profile.ID == "" {
		profile.ID = randomID("prof")
		profile.Revision = 1
		profile.CreatedAt = now
	} else if existing, ok := s.profiles[profile.ID]; ok {
		if existing.PrincipalID != profile.PrincipalID {
			return domain.InvoiceProfile{}, domain.ErrForbidden
		}
		profile.Revision = existing.Revision + 1
		profile.CreatedAt = existing.CreatedAt
	} else {
		return domain.InvoiceProfile{}, domain.ErrNotFound
	}
	profile.UpdatedAt = now
	s.profiles[profile.ID] = profile
	return profile, nil
}

// VerifyNewAPIPayment turns a source-only New API top-up candidate into an
// invoiceable funding lot after an administrator has checked independent
// payment evidence. It deliberately cannot verify Sub2API lots or change an
// already verified/frozen lot.
func (s *Service) VerifyNewAPIPayment(_ context.Context, lotID string, paidMinor int64, currency string) (domain.FundingLot, error) {
	if paidMinor <= 0 || currency != domain.CurrencyCNY {
		return domain.FundingLot{}, domain.ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lot, ok := s.lots[lotID]
	if !ok {
		return domain.FundingLot{}, domain.ErrNotFound
	}
	if lot.SourceType != domain.SourceNewAPI || lot.Verification != domain.VerificationPending {
		return domain.FundingLot{}, domain.ErrInvalidState
	}
	lot.Currency = currency
	lot.OriginalMinor = paidMinor
	lot.CurrentCapMinor = paidMinor
	lot.Verification = domain.VerificationVerified
	lot.UpdatedAt = s.now()
	if err := lot.Validate(); err != nil {
		return domain.FundingLot{}, err
	}
	s.lots[lot.ID] = lot
	return lot, nil
}

func (s *Service) VerifyNewAPIPaymentWithEvidence(ctx context.Context, _ string, lotID string, paidMinor int64, currency, evidence string) (domain.FundingLot, error) {
	if strings.TrimSpace(evidence) == "" || len(evidence) > 512 {
		return domain.FundingLot{}, domain.ErrConflict
	}
	return s.VerifyNewAPIPayment(ctx, lotID, paidMinor, currency)
}

func (s *Service) Submit(_ context.Context, input SubmitInput) (domain.InvoiceRequest, error) {
	if strings.TrimSpace(input.PrincipalID) == "" || strings.TrimSpace(input.IdempotencyKey) == "" || len(input.Allocations) == 0 {
		return domain.InvoiceRequest{}, fmt.Errorf("invalid submission")
	}
	if len(input.IdempotencyKey) > 128 || strings.ContainsAny(input.IdempotencyKey, "\r\n\x00") {
		return domain.InvoiceRequest{}, fmt.Errorf("invalid idempotency key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idem := input.PrincipalID + "\n" + input.IdempotencyKey
	if requestID, ok := s.idempotency[idem]; ok {
		return s.requests[requestID], nil
	}
	profile, ok := s.profiles[input.ProfileID]
	if !ok {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	}
	if profile.PrincipalID != input.PrincipalID {
		return domain.InvoiceRequest{}, domain.ErrForbidden
	}

	inputs := append([]AllocationInput(nil), input.Allocations...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].FundingLotID < inputs[j].FundingLotID })
	seen := map[string]struct{}{}
	allocations := make([]domain.Allocation, 0, len(inputs))
	var total int64
	var sourceType domain.SourceType
	for _, requested := range inputs {
		if requested.AmountMinor <= 0 {
			return domain.InvoiceRequest{}, domain.ErrInsufficientAmount
		}
		if _, duplicate := seen[requested.FundingLotID]; duplicate {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		seen[requested.FundingLotID] = struct{}{}
		lot, exists := s.lots[requested.FundingLotID]
		if !exists {
			return domain.InvoiceRequest{}, domain.ErrNotFound
		}
		if lot.PrincipalID != input.PrincipalID {
			return domain.InvoiceRequest{}, domain.ErrForbidden
		}
		if lot.SourceInstanceID != input.SourceInstanceID {
			return domain.InvoiceRequest{}, domain.ErrSourceMixing
		}
		if sourceType == "" {
			sourceType = lot.SourceType
		} else if sourceType != lot.SourceType {
			return domain.InvoiceRequest{}, domain.ErrSourceMixing
		}
		if lot.Verification != domain.VerificationVerified {
			return domain.InvoiceRequest{}, domain.ErrUnverifiedPayment
		}
		if lot.Currency != domain.CurrencyCNY {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		if requested.AmountMinor > lot.AvailableMinor() {
			return domain.InvoiceRequest{}, domain.ErrInsufficientAmount
		}
		if requested.AmountMinor > int64(^uint64(0)>>1)-total {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		total += requested.AmountMinor
		allocations = append(allocations, domain.Allocation{FundingLotID: lot.ID, ExternalOrder: lot.ExternalOrderID, AmountMinor: requested.AmountMinor})
	}
	if total < s.minimumRequestMinor {
		return domain.InvoiceRequest{}, fmt.Errorf("minimum invoice amount is %d minor units: %w", s.minimumRequestMinor, domain.ErrMinimumAmount)
	}

	for _, allocation := range allocations {
		lot := s.lots[allocation.FundingLotID]
		lot.ReservedMinor += allocation.AmountMinor
		lot.UpdatedAt = s.now()
		s.lots[lot.ID] = lot
	}
	now := s.now()
	id := randomID("invreq")
	request := domain.InvoiceRequest{
		ID: id, RequestNo: "INV-" + strings.ToUpper(id[len(id)-10:]), PrincipalID: input.PrincipalID,
		SourceInstanceID: input.SourceInstanceID, SourceType: sourceType, Currency: domain.CurrencyCNY,
		IssuerCode: "default", ServiceItem: domain.FixedServiceItem, AmountMinor: total, Status: domain.StatusPendingReview,
		Profile: domain.SnapshotProfile(profile), Allocations: allocations, Version: 1, SubmittedAt: now, UpdatedAt: now,
	}
	s.requests[id] = request
	s.idempotency[idem] = id
	return request, nil
}

func (s *Service) Cancel(_ context.Context, principalID, requestID string, expectedVersion int64) (domain.InvoiceRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	}
	if request.PrincipalID != principalID {
		return domain.InvoiceRequest{}, domain.ErrForbidden
	}
	if request.Version != expectedVersion {
		return domain.InvoiceRequest{}, domain.ErrVersionConflict
	}
	if request.Status != domain.StatusPendingReview && request.Status != domain.StatusNeedsChanges {
		return domain.InvoiceRequest{}, domain.ErrInvalidState
	}
	s.releaseLocked(request)
	request.Status = domain.StatusUserCancelled
	request.Version++
	request.UpdatedAt = s.now()
	s.requests[request.ID] = request
	return request, nil
}

func (s *Service) Review(_ context.Context, adminID, requestID, action, note string, expectedVersion int64) (domain.InvoiceRequest, error) {
	if len(note) > 2000 || strings.ContainsRune(note, '\x00') {
		return domain.InvoiceRequest{}, fmt.Errorf("invalid review note")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	}
	if request.Version != expectedVersion {
		return domain.InvoiceRequest{}, domain.ErrVersionConflict
	}
	if request.Status != domain.StatusPendingReview && request.Status != domain.StatusNeedsChanges {
		return domain.InvoiceRequest{}, domain.ErrInvalidState
	}
	switch action {
	case "approve":
		request.Status = domain.StatusApproved
	case "return":
		request.Status = domain.StatusNeedsChanges
	case "reject":
		request.Status = domain.StatusRejected
		s.releaseLocked(request)
	default:
		return domain.InvoiceRequest{}, domain.ErrInvalidState
	}
	request.ReviewedBy = adminID
	request.ReviewNote = strings.TrimSpace(note)
	request.Version++
	request.UpdatedAt = s.now()
	s.requests[request.ID] = request
	return request, nil
}

func (s *Service) ConfirmManualIssue(_ context.Context, adminID, requestID string, expectedVersion int64) (domain.InvoiceRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	}
	if request.Version != expectedVersion {
		return domain.InvoiceRequest{}, domain.ErrVersionConflict
	}
	if request.Status != domain.StatusApproved && request.Status != domain.StatusManualIssuing {
		return domain.InvoiceRequest{}, domain.ErrInvalidState
	}
	for _, allocation := range request.Allocations {
		lot := s.lots[allocation.FundingLotID]
		if lot.ReservedMinor < allocation.AmountMinor {
			return domain.InvoiceRequest{}, domain.ErrConflict
		}
		lot.ReservedMinor -= allocation.AmountMinor
		lot.IssuedMinor += allocation.AmountMinor
		lot.UpdatedAt = s.now()
		s.lots[lot.ID] = lot
	}
	request.Status = domain.StatusIssuedAwaitingDocument
	request.IssuedBy = adminID
	request.Version++
	request.UpdatedAt = s.now()
	s.requests[request.ID] = request
	return request, nil
}

func (s *Service) BeginManualIssue(_ context.Context, adminID, requestID string, expectedVersion int64) (domain.InvoiceRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	}
	if request.Version != expectedVersion {
		return domain.InvoiceRequest{}, domain.ErrVersionConflict
	}
	if request.Status != domain.StatusApproved {
		return domain.InvoiceRequest{}, domain.ErrInvalidState
	}
	request.Status = domain.StatusManualIssuing
	request.IssuedBy = adminID
	request.Version++
	request.UpdatedAt = s.now()
	s.requests[request.ID] = request
	return request, nil
}

func (s *Service) AttachDocument(_ context.Context, adminID string, document domain.InvoiceDocument, expectedVersion int64) (domain.InvoiceRequest, domain.InvoiceDocument, domain.EmailOutbox, error) {
	if err := document.Validate(); err != nil {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[document.RequestID]
	if !ok {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrNotFound
	}
	if request.Version != expectedVersion {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrVersionConflict
	}
	if request.Status != domain.StatusIssuedAwaitingDocument {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrInvalidState
	}
	if document.ID == "" {
		document.ID = randomID("doc")
	}
	document.UploadedBy = adminID
	if document.CreatedAt.IsZero() {
		document.CreatedAt = s.now()
	}
	for _, existing := range s.documents {
		if existing.InvoiceNumber == document.InvoiceNumber {
			return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, domain.ErrConflict
		}
	}
	s.documents[document.ID] = document
	request.Status = domain.StatusIssued
	request.Version++
	request.UpdatedAt = s.now()
	s.requests[request.ID] = request
	outbox := domain.EmailOutbox{ID: randomID("mail"), RequestID: request.ID, DocumentID: document.ID, RecipientHash: "pending-hmac", TemplateVersion: "invoice-issued-v1", Status: "queued", NextAttemptAt: s.now(), CreatedAt: s.now()}
	s.outbox[outbox.ID] = outbox
	return request, document, outbox, nil
}

func (s *Service) releaseLocked(request domain.InvoiceRequest) {
	for _, allocation := range request.Allocations {
		lot := s.lots[allocation.FundingLotID]
		if lot.ReservedMinor >= allocation.AmountMinor {
			lot.ReservedMinor -= allocation.AmountMinor
		}
		lot.UpdatedAt = s.now()
		s.lots[lot.ID] = lot
	}
}

func (s *Service) ListFundingLots(_ context.Context, principalID string) ([]domain.FundingLot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.FundingLot{}
	for _, lot := range s.lots {
		if lot.PrincipalID == principalID {
			out = append(out, lot)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompletedAt.After(out[j].CompletedAt) })
	return out, nil
}

func (s *Service) ListProfiles(_ context.Context, principalID string) ([]domain.InvoiceProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.InvoiceProfile{}
	for _, profile := range s.profiles {
		if profile.PrincipalID == principalID {
			out = append(out, profile)
		}
	}
	return out, nil
}

func (s *Service) ListRequests(_ context.Context, principalID string, admin bool) ([]domain.InvoiceRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.InvoiceRequest{}
	for _, request := range s.requests {
		if admin || request.PrincipalID == principalID {
			out = append(out, request)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.After(out[j].SubmittedAt) })
	return out, nil
}

func (s *Service) GetRequest(_ context.Context, principalID, requestID string, admin bool) (domain.InvoiceRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.InvoiceRequest{}, domain.ErrNotFound
	}
	if !admin && request.PrincipalID != principalID {
		return domain.InvoiceRequest{}, domain.ErrForbidden
	}
	return request, nil
}

func (s *Service) GetDocumentForRequest(_ context.Context, principalID, requestID string) (domain.InvoiceDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.InvoiceDocument{}, domain.ErrNotFound
	}
	if request.PrincipalID != principalID {
		return domain.InvoiceDocument{}, domain.ErrForbidden
	}
	if request.Status != domain.StatusIssued {
		return domain.InvoiceDocument{}, domain.ErrInvalidState
	}
	for _, document := range s.documents {
		if document.RequestID == requestID {
			return document, nil
		}
	}
	return domain.InvoiceDocument{}, domain.ErrNotFound
}

func (s *Service) GetDocumentForRequestAsAdmin(_ context.Context, requestID string) (domain.InvoiceDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.InvoiceDocument{}, domain.ErrNotFound
	}
	if request.Status != domain.StatusIssued && request.Status != domain.StatusRefundAttention {
		return domain.InvoiceDocument{}, domain.ErrInvalidState
	}
	for _, document := range s.documents {
		if document.RequestID == requestID {
			return document, nil
		}
	}
	return domain.InvoiceDocument{}, domain.ErrNotFound
}
