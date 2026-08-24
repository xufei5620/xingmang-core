package adminsettings

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"sort"
	"strings"
	"unicode/utf8"
)

type Service struct {
	repo Repository
	box  SecretBox
}

func NewService(repo Repository, box SecretBox) *Service { return &Service{repo: repo, box: box} }

func (s *Service) Get(ctx context.Context) (Settings, error) { return s.repo.Get(ctx) }

func (s *Service) Update(ctx context.Context, in UpdateInput, expectedRevision int64, actor Actor) (Settings, error) {
	normalized, err := normalize(in)
	if err != nil {
		return Settings{}, err
	}
	if expectedRevision < 0 || strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return Settings{}, fmt.Errorf("%w: revision, actor and request ID are required", ErrInvalidSettings)
	}
	return s.repo.Update(ctx, normalized, expectedRevision, actor)
}

// UpdateSMTP atomically updates SMTP metadata and its optional credential. Any
// credential encryption is completed before the repository opens a transaction.
func (s *Service) UpdateSMTP(ctx context.Context, in UpdateInput, change SMTPSecretChange, authorizationCode string, expectedRevision int64, actor Actor) (Settings, error) {
	normalized, err := normalize(in)
	if err != nil {
		return Settings{}, err
	}
	if expectedRevision < 0 || strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return Settings{}, fmt.Errorf("%w: revision, actor and request ID are required", ErrInvalidSettings)
	}
	var envelope SecretEnvelope
	switch change {
	case SMTPSecretUnchanged:
		if authorizationCode != "" {
			return Settings{}, fmt.Errorf("%w: unchanged secret cannot include an authorization code", ErrInvalidSettings)
		}
	case SMTPSecretSet:
		if s.box == nil || strings.TrimSpace(authorizationCode) == "" {
			return Settings{}, fmt.Errorf("%w: SMTP authorization code is required", ErrInvalidSettings)
		}
		envelope, err = s.box.Seal(ctx, []byte(authorizationCode))
		if err != nil {
			return Settings{}, fmt.Errorf("encrypt SMTP secret: %w", err)
		}
		if len(envelope.Ciphertext) == 0 || strings.TrimSpace(envelope.KeyVersion) == "" {
			return Settings{}, errorsInvalidEnvelope()
		}
	case SMTPSecretClear:
		if authorizationCode != "" {
			return Settings{}, fmt.Errorf("%w: clear secret cannot include an authorization code", ErrInvalidSettings)
		}
	default:
		return Settings{}, fmt.Errorf("%w: unsupported SMTP secret change", ErrInvalidSettings)
	}
	return s.repo.UpdateSMTP(ctx, normalized, change, envelope, expectedRevision, actor)
}

func (s *Service) SetSMTPSecret(ctx context.Context, authorizationCode string, expectedRevision int64, actor Actor) (Settings, error) {
	if s.box == nil || strings.TrimSpace(authorizationCode) == "" {
		return Settings{}, fmt.Errorf("%w: SMTP authorization code is required", ErrInvalidSettings)
	}
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return Settings{}, fmt.Errorf("%w: actor and request ID are required", ErrInvalidSettings)
	}
	envelope, err := s.box.Seal(ctx, []byte(authorizationCode))
	if err != nil {
		return Settings{}, fmt.Errorf("encrypt SMTP secret: %w", err)
	}
	if len(envelope.Ciphertext) == 0 || strings.TrimSpace(envelope.KeyVersion) == "" {
		return Settings{}, errorsInvalidEnvelope()
	}
	return s.repo.StoreSMTPSecret(ctx, envelope, expectedRevision, actor)
}

func (s *Service) ClearSMTPSecret(ctx context.Context, expectedRevision int64, actor Actor) (Settings, error) {
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return Settings{}, fmt.Errorf("%w: actor and request ID are required", ErrInvalidSettings)
	}
	return s.repo.ClearSMTPSecret(ctx, expectedRevision, actor)
}

// SMTPSecretForDelivery is intentionally separate from Get. Only the mail
// delivery worker should call it; ordinary settings responses never contain it.
func (s *Service) SMTPSecretForDelivery(ctx context.Context) (string, error) {
	if s.box == nil {
		return "", ErrSecretMissing
	}
	envelope, err := s.repo.LoadSMTPSecret(ctx)
	if err != nil {
		return "", err
	}
	plain, err := s.box.Open(ctx, envelope)
	if err != nil {
		return "", fmt.Errorf("decrypt SMTP secret: %w", err)
	}
	return string(plain), nil
}

func errorsInvalidEnvelope() error {
	return fmt.Errorf("%w: secret box returned an empty envelope", ErrInvalidSettings)
}

func normalize(in UpdateInput) (UpdateInput, error) {
	in.IssuerName = strings.TrimSpace(in.IssuerName)
	in.SMTPHost = strings.ToLower(strings.TrimSpace(in.SMTPHost))
	in.SMTPFrom = strings.TrimSpace(in.SMTPFrom)
	in.SMTPFromName = strings.TrimSpace(in.SMTPFromName)
	if in.IssuerName == "" || utf8.RuneCountInString(in.IssuerName) > 200 || in.MinimumRequestMinor < MinimumMinor ||
		in.EligibilityStartAt.IsZero() || !in.EligibilityStartAt.UTC().Equal(RequiredEligibilityStartAt) ||
		in.SMTPPort != 587 || in.SMTPFromName == "" || utf8.RuneCountInString(in.SMTPFromName) > 128 || !in.SMTPStartTLS {
		return UpdateInput{}, ErrInvalidSettings
	}
	if in.SMTPHost != "smtp.qq.com" && in.SMTPHost != "smtp.exmail.qq.com" && in.SMTPHost != "smtp.gmail.com" {
		return UpdateInput{}, fmt.Errorf("%w: unsupported SMTP host", ErrInvalidSettings)
	}
	address, err := mail.ParseAddress(in.SMTPFrom)
	if err != nil || !strings.EqualFold(address.Address, in.SMTPFrom) {
		return UpdateInput{}, fmt.Errorf("%w: invalid SMTP from address", ErrInvalidSettings)
	}
	if len(in.AdminCIDRs) == 0 {
		return UpdateInput{}, fmt.Errorf("%w: at least one admin CIDR is required", ErrInvalidSettings)
	}
	if len(in.AdminCIDRs) > 16 {
		return UpdateInput{}, fmt.Errorf("%w: at most 16 admin CIDRs are allowed", ErrInvalidSettings)
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(in.AdminCIDRs))
	for _, raw := range in.AdminCIDRs {
		raw = strings.TrimSpace(raw)
		if !strings.Contains(raw, "/") {
			if ip := net.ParseIP(raw); ip != nil {
				if ip.To4() != nil {
					raw += "/32"
				} else {
					raw += "/128"
				}
			}
		}
		_, network, parseErr := net.ParseCIDR(raw)
		if parseErr != nil {
			return UpdateInput{}, fmt.Errorf("%w: invalid admin CIDR %q", ErrInvalidSettings, raw)
		}
		ones, bits := network.Mask.Size()
		if network.IP.IsUnspecified() || network.IP.IsMulticast() || network.IP.IsLinkLocalUnicast() || network.IP.IsLinkLocalMulticast() ||
			(bits == 32 && ones < 24) || (bits == 128 && ones < 64) {
			return UpdateInput{}, fmt.Errorf("%w: unsafe or overly broad admin CIDR %q", ErrInvalidSettings, raw)
		}
		canonical := network.String()
		if _, ok := seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			normalized = append(normalized, canonical)
		}
	}
	sort.Strings(normalized)
	in.AdminCIDRs = normalized
	return in, nil
}
