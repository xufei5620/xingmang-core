package sourceagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const IdentityUserStatusUnknown = "unknown"

// Sub2APIIdentityDBConnector reads only canonical OIDC identities for one
// configured central IdP. It never reads users, email identities, metadata, or
// channel/session tables.
type Sub2APIIdentityDBConnector struct {
	DB                 *sql.DB
	Dialect            SQLDialect
	TrustedProviderKey string
	TrustedIssuer      string
	Now                func() time.Time
}

func (c *Sub2APIIdentityDBConnector) SourceType() string { return SourceSub2API }

func (c *Sub2APIIdentityDBConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.DB == nil {
		return ScanPage{}, errors.New("sub2api identity connector: database is required")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	providerKey := strings.TrimSpace(c.TrustedProviderKey)
	issuer, err := canonicalIssuer(c.TrustedIssuer)
	if err != nil || providerKey == "" {
		return ScanPage{}, errors.New("sub2api identity connector: trusted OIDC provider is required")
	}
	effectiveCursor := req.Cursor
	if (req.Mode == ScanFull || req.Mode == ScanReconcile) && effectiveCursor.Completed {
		effectiveCursor = ScanCursor{Revision: effectiveCursor.Revision}
	}
	cursorTime, err := parseCursorTime(effectiveCursor.UpdatedAt)
	if err != nil {
		return ScanPage{}, err
	}
	limit := boundedScanLimit(req.Limit)
	query, args, err := sub2APIIdentityQuery(c.Dialect, providerKey, issuer, cursorTime, effectiveCursor.ID, limit)
	if err != nil {
		return ScanPage{}, err
	}
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return ScanPage{}, fmt.Errorf("sub2api identity connector: read projection: %w", err)
	}
	defer rows.Close()
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	page := ScanPage{Projections: make([]Projection, 0, limit)}
	next := effectiveCursor
	next.Version = 1
	next.Completed = false
	read := 0
	for rows.Next() {
		var id, userID int64
		var providerType, actualProviderKey, subject string
		var verifiedAt nullableTime
		var actualIssuer sql.NullString
		var createdAt, updatedAt nullableTime
		if err := rows.Scan(&id, &userID, &providerType, &actualProviderKey, &subject, &verifiedAt, &actualIssuer, &createdAt, &updatedAt); err != nil {
			return ScanPage{}, fmt.Errorf("sub2api identity connector: scan projection: %w", err)
		}
		if providerType != "oidc" || actualProviderKey != providerKey || strings.TrimRight(actualIssuer.String, "/") != issuer || id <= 0 || userID <= 0 || subject == "" || !createdAt.Valid || !updatedAt.Valid {
			return ScanPage{}, errors.New("sub2api identity connector: trusted identity invariant failed")
		}
		issuerValue := issuer
		page.Projections = append(page.Projections, Projection{
			EntityType: EntityIdentityBinding,
			ExternalID: strconv.FormatInt(id, 10),
			ObservedAt: now().UTC().Format(time.RFC3339Nano),
			Operation:  "upsert",
			Payload: IdentityBindingPayload{
				ExternalUserID: strconv.FormatInt(userID, 10), ProviderType: "oidc",
				ProviderKey: providerKey, ProviderSubject: subject, Issuer: issuerValue,
				VerifiedAt: nullableTimeString(verifiedAt), UserStatus: IdentityUserStatusUnknown,
				UpdatedAt: updatedAt.Time.UTC().Format(time.RFC3339Nano),
			},
		})
		next.UpdatedAt = updatedAt.Time.UTC().Format(time.RFC3339Nano)
		next.ID = id
		read++
	}
	if err := rows.Err(); err != nil {
		return ScanPage{}, err
	}
	page.HasMore = read == limit
	next.Completed = !page.HasMore
	page.NextCursor = next
	return page, nil
}

// NewAPIIdentityDBConnector reads only two security-barrier views. The provider
// view exposes four non-secret OIDC endpoints plus contract_ok; the binding
// view exposes identities only when that contract is structurally valid. It
// never receives raw-table access, users, client credentials, scopes, mappings,
// or access policy.
type NewAPIIdentityDBConnector struct {
	DB                  *sql.DB
	Dialect             SQLDialect
	TrustedProviderSlug string
	TrustedIssuer       string
	Now                 func() time.Time
}

func (c *NewAPIIdentityDBConnector) SourceType() string { return SourceNewAPI }

func (c *NewAPIIdentityDBConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.DB == nil {
		return ScanPage{}, errors.New("newapi identity connector: database is required")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	slug := strings.TrimSpace(c.TrustedProviderSlug)
	issuer, err := canonicalIssuer(c.TrustedIssuer)
	if err != nil || slug == "" {
		return ScanPage{}, errors.New("newapi identity connector: trusted OIDC provider is required")
	}
	effectiveCursor := req.Cursor
	if (req.Mode == ScanFull || req.Mode == ScanReconcile) && effectiveCursor.Completed {
		effectiveCursor = ScanCursor{Revision: effectiveCursor.Revision}
	}
	limit := boundedScanLimit(req.Limit)
	if c.Dialect == DialectPostgres {
		if err := c.verifyNewAPIProviderContract(ctx, slug, issuer); err != nil {
			return ScanPage{}, err
		}
	}
	query, args, err := newAPIIdentityQuery(c.Dialect, slug, effectiveCursor.ID, limit)
	if err != nil {
		return ScanPage{}, err
	}
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return ScanPage{}, fmt.Errorf("newapi identity connector: read projection: %w", err)
	}
	defer rows.Close()
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	page := ScanPage{
		Projections: make([]Projection, 0, limit),
		Warnings:    []string{"New API OAuth bindings have no updated_at/deleted_at; periodic full reconciliation with repeated-miss tombstone confirmation is required"},
	}
	next := effectiveCursor
	next.Version = 1
	next.Completed = false
	read := 0
	for rows.Next() {
		var id, userID, providerID int64
		var subject, actualSlug string
		var createdAt nullableTime
		if err := rows.Scan(&id, &userID, &providerID, &subject, &createdAt, &actualSlug); err != nil {
			return ScanPage{}, fmt.Errorf("newapi identity connector: scan projection: %w", err)
		}
		if id <= 0 || userID <= 0 || providerID <= 0 || subject == "" || !createdAt.Valid || actualSlug != slug {
			return ScanPage{}, errors.New("newapi identity connector: trusted identity invariant failed")
		}
		verifiedAt := createdAt.Time.UTC().Format(time.RFC3339Nano)
		page.Projections = append(page.Projections, Projection{
			EntityType: EntityIdentityBinding,
			ExternalID: strconv.FormatInt(id, 10),
			ObservedAt: now().UTC().Format(time.RFC3339Nano),
			Operation:  "upsert",
			Payload: IdentityBindingPayload{
				ExternalUserID: strconv.FormatInt(userID, 10), ProviderType: "oidc",
				ProviderKey: slug, ProviderSubject: subject, Issuer: issuer,
				VerifiedAt: &verifiedAt, UserStatus: IdentityUserStatusUnknown,
				UpdatedAt: verifiedAt,
			},
		})
		next.ID = id
		read++
	}
	if err := rows.Err(); err != nil {
		return ScanPage{}, err
	}
	page.HasMore = read == limit
	next.Completed = !page.HasMore
	page.NextCursor = next
	return page, nil
}

func (c *NewAPIIdentityDBConnector) verifyNewAPIProviderContract(ctx context.Context, slug, issuer string) error {
	var actualSlug, wellKnown, authorizationEndpoint, tokenEndpoint, userInfoEndpoint string
	var enabled, contractOK bool
	err := c.DB.QueryRowContext(ctx, `
		SELECT slug,enabled,well_known,authorization_endpoint,token_endpoint,
			user_info_endpoint,contract_ok
		FROM public.invoice_newapi_oidc_provider_contract_v1
		WHERE slug=$1`, slug).Scan(&actualSlug, &enabled, &wellKnown,
		&authorizationEndpoint, &tokenEndpoint, &userInfoEndpoint, &contractOK)
	if err != nil {
		return fmt.Errorf("newapi identity connector: read provider trust contract: %w", err)
	}
	if actualSlug != slug || !enabled || !contractOK ||
		!providerEndpointsMatchIssuer(wellKnown, authorizationEndpoint, tokenEndpoint, userInfoEndpoint, issuer) {
		return errors.New("newapi identity connector: trusted provider contract failed")
	}
	return nil
}

func sub2APIIdentityQuery(dialect SQLDialect, providerKey, issuer string, updatedAt time.Time, id int64, limit int) (string, []any, error) {
	switch dialect {
	case DialectPostgres:
		return `SELECT id, user_id, provider_type, provider_key, provider_subject,
verified_at, issuer, created_at, updated_at FROM public.invoice_sub2api_oidc_binding_projection_v1
WHERE provider_type = $1 AND provider_key = $2 AND issuer = $3
AND (updated_at, id) > ($4, $5)
ORDER BY updated_at ASC, id ASC LIMIT $6`, []any{"oidc", providerKey, issuer, updatedAt, id, limit}, nil
	case DialectMySQL, DialectSQLite:
		return `SELECT id, user_id, provider_type, provider_key, provider_subject,
verified_at, issuer, created_at, updated_at FROM auth_identities
WHERE provider_type = ? AND provider_key = ? AND issuer = ?
AND (updated_at > ? OR (updated_at = ? AND id > ?))
ORDER BY updated_at ASC, id ASC LIMIT ?`, []any{"oidc", providerKey, issuer, updatedAt, updatedAt, id, limit}, nil
	default:
		return "", nil, fmt.Errorf("unsupported SQL dialect %q", dialect)
	}
}

func newAPIIdentityQuery(dialect SQLDialect, slug string, id int64, limit int) (string, []any, error) {
	if dialect != DialectPostgres && dialect != DialectMySQL && dialect != DialectSQLite {
		return "", nil, fmt.Errorf("unsupported SQL dialect %q", dialect)
	}
	if dialect == DialectPostgres {
		return `SELECT id,user_id,provider_id,provider_user_id,created_at,provider_slug
FROM public.invoice_newapi_oidc_binding_projection_v1
WHERE provider_slug=$1 AND id>$2 ORDER BY id ASC LIMIT $3`, []any{slug, id, limit}, nil
	}
	return `SELECT b.id,b.user_id,b.provider_id,b.provider_user_id,b.created_at,p.slug
FROM user_oauth_bindings b JOIN custom_oauth_providers p ON p.id=b.provider_id
WHERE p.slug=` + placeholder(dialect, 1) + ` AND p.enabled=TRUE AND b.id>` + placeholder(dialect, 2) + `
ORDER BY b.id ASC LIMIT ` + placeholder(dialect, 3), []any{slug, id, limit}, nil
}

func canonicalIssuer(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid trusted issuer")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func providerEndpointsMatchIssuer(wellKnown, authorizationEndpoint, tokenEndpoint, userInfoEndpoint, issuer string) bool {
	trusted, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	wellKnownURL, wellKnownErr := url.Parse(strings.TrimSpace(wellKnown))
	authorizationURL, authorizationErr := url.Parse(strings.TrimSpace(authorizationEndpoint))
	tokenURL, tokenErr := url.Parse(strings.TrimSpace(tokenEndpoint))
	userInfoURL, userInfoErr := url.Parse(strings.TrimSpace(userInfoEndpoint))
	expectedWellKnownPath := strings.TrimRight(trusted.Path, "/") + "/.well-known/openid-configuration"
	wellKnownMatches := wellKnownErr == nil && endpointURLSafe(wellKnownURL, trusted) && strings.TrimRight(wellKnownURL.Path, "/") == expectedWellKnownPath
	endpointUnderIssuer := func(candidate *url.URL, err error) bool {
		return err == nil && endpointURLSafe(candidate, trusted) &&
			strings.HasPrefix(strings.TrimRight(candidate.Path, "/"), strings.TrimRight(trusted.Path, "/")+"/")
	}
	return wellKnownMatches && endpointUnderIssuer(authorizationURL, authorizationErr) &&
		endpointUnderIssuer(tokenURL, tokenErr) && endpointUnderIssuer(userInfoURL, userInfoErr)
}

func endpointURLSafe(candidate, trusted *url.URL) bool {
	return candidate != nil && candidate.User == nil && candidate.Fragment == "" && candidate.RawQuery == "" &&
		candidate.Scheme == "https" && candidate.Scheme == trusted.Scheme &&
		strings.EqualFold(candidate.Host, trusted.Host)
}
