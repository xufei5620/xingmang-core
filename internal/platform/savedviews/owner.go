package savedviews

import (
	"errors"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const devHeaderIssuer = "dev://header-resolver"

var ErrInvalidOwner = errors.New("saved_view_owner_invalid")

func OwnerFromPrincipal(p principal.Principal) (Owner, error) {
	if p.Type != principal.TypeHuman {
		return Owner{}, ErrInvalidOwner
	}
	issuer := strings.TrimSpace(p.Issuer)
	identityZone := strings.TrimSpace(p.IdentityZone)
	environment := strings.TrimSpace(p.Environment)
	if issuer == "" || identityZone == "" || environment == "" {
		return Owner{}, ErrInvalidOwner
	}

	subject := strings.TrimSpace(p.Subject)
	if subject == "" && issuer == devHeaderIssuer && environment != "production" {
		subject = strings.TrimSpace(p.ID)
	}
	if subject == "" {
		return Owner{}, ErrInvalidOwner
	}
	return Owner{
		Issuer:       issuer,
		Subject:      subject,
		IdentityZone: identityZone,
		Environment:  environment,
	}, nil
}
