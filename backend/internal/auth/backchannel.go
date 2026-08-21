package auth

import (
	"context"
	"errors"
	"strings"
	"time"
)

type BackchannelLogoutActor struct {
	RequestID    string
	SourceIPHash string
}

type BackchannelLogoutResult struct {
	EventID         string
	RevokedSessions int64
	Replay          bool
}

type BackchannelLogoutRepository interface {
	ApplyBackchannelLogout(context.Context, VerifiedBackchannelLogout, BackchannelLogoutActor) (BackchannelLogoutResult, error)
}

type BackchannelLogoutService struct{ repository BackchannelLogoutRepository }

func NewBackchannelLogoutService(repository BackchannelLogoutRepository) (*BackchannelLogoutService, error) {
	if repository == nil {
		return nil, errors.New("back-channel logout repository is required")
	}
	return &BackchannelLogoutService{repository: repository}, nil
}

func (s *BackchannelLogoutService) Process(ctx context.Context, event VerifiedBackchannelLogout, actor BackchannelLogoutActor) (BackchannelLogoutResult, error) {
	now := time.Now().UTC()
	if s == nil || s.repository == nil || event.Issuer == "" || event.TokenID == "" || event.IssuedAt.IsZero() || event.ExpiresAt.IsZero() ||
		(event.SessionID == "" && event.Subject == "") || !validRequestID(actor.RequestID) ||
		(actor.SourceIPHash != "" && len(actor.SourceIPHash) != 64) || strings.ContainsAny(actor.SourceIPHash, "\r\n\x00") ||
		event.IssuedAt.After(now.Add(5*time.Minute)) || !event.ExpiresAt.After(now) || event.ExpiresAt.After(event.IssuedAt.Add(35*time.Minute)) {
		return BackchannelLogoutResult{}, ErrInvalidLogoutToken
	}
	return s.repository.ApplyBackchannelLogout(ctx, event, actor)
}
