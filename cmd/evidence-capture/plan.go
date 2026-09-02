package main

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// PlatformSub2API / PlatformNewAPI are the only two platform values this
// tool accepts. There is deliberately no "generic connector" mode: the
// request plan below is not data, it is source - the only way to add a
// platform is to add a new named plan function and get it reviewed.
const (
	PlatformSub2API = "sub2api"
	PlatformNewAPI  = "newapi"
)

// defaultSampleSize / maxSampleSize bound how many rows a single "users
// page" probe asks for. This tool exists to capture *shape* evidence for a
// product+security approval, not to export a census: a reviewer approving
// SUB2_REAL_APPROVAL/NEWAPI_REAL_APPROVAL needs to see a handful of
// realistic redacted rows, not the whole user table. There is no flag that
// raises this above maxSampleSize.
const (
	defaultSampleSize = 5
	maxSampleSize     = 20
)

// ProbeStep is one fixed, named, read-only HTTP request this tool is
// willing to make. There is no CLI flag anywhere in this program that lets
// a caller supply an arbitrary path or method - every ProbeStep a run can
// ever issue comes from planFor below. That is the primary safety property
// of this tool (the read-only HTTP transport in internal/platform/connector
// is the second, independent one - see httpfetch.go): a request this tool
// has never heard of cannot be sent by mistyping a flag.
type ProbeStep struct {
	// Purpose names what this request is for; it becomes the evidence
	// filename stem and the request-log.json "purpose" field.
	Purpose string
	Method  string
	Path    string
	Query   url.Values
}

// requestLine renders "METHOD /path?query" for --dry-run output and the
// request log. Query values here are only ever small non-secret integers
// (page numbers, page sizes) - never anything that came from an upstream
// response or a credential.
func (s ProbeStep) requestLine() string {
	if len(s.Query) == 0 {
		return s.Method + " " + s.Path
	}
	return s.Method + " " + s.Path + "?" + s.Query.Encode()
}

// planFor returns the fixed, ordered list of requests an evidence capture
// run for platform will make. sampleSize is clamped to [1, maxSampleSize].
//
// Every path here is cited against source that has already verified it is a
// real, non-mock, GET-safe endpoint:
//   - Sub2API routes and the AdminComplianceGuard behavior: EV-2026-08-27-sub2api-read-survey.md,
//     connectors/platformusers/upstream.go (sub2apiRouteUsers, routeVersion
//     in connectors/sub2api/upstream.go).
//   - NewAPI routes: EV-2026-08-27-newapi-read-survey.md,
//     connectors/platformusers/upstream.go (newapiRouteUsers, newapiRouteStatus).
//
// Neither platform is known to expose a distinct "single user by id" GET
// endpoint (Sub2API's only per-id admin route, /api/v1/admin/users/:id/usage,
// is a proven mock - see mustNotBeMockRoute). Both future real GetUser
// parsers (Task 4/5 of the design plan) are specified to parse a *page*
// body and pick one user out of it
// (docs/superpowers/plans/2026-08-28-platform-user-read-v2.md
// parseSub2UsersPage/parseNewAPIUsersPage). This tool follows the same
// shape: "one user detail" evidence is the first record extracted from the
// same users-page response, not a second network call to a guessed route.
func planFor(platform string, sampleSize int) ([]ProbeStep, error) {
	if sampleSize <= 0 {
		sampleSize = defaultSampleSize
	}
	if sampleSize > maxSampleSize {
		return nil, fmt.Errorf("evidence-capture: sample size %d exceeds max %d (this tool captures shape evidence, not a census)", sampleSize, maxSampleSize)
	}

	switch platform {
	case PlatformSub2API:
		return []ProbeStep{
			{Purpose: "version", Method: "GET", Path: sub2apiRouteVersion},
			{
				Purpose: "users_page", Method: "GET", Path: sub2apiRouteUsers,
				Query: url.Values{"page": {"1"}, "page_size": {strconv.Itoa(sampleSize)}},
			},
		}, nil
	case PlatformNewAPI:
		return []ProbeStep{
			{Purpose: "version_and_quota", Method: "GET", Path: newapiRouteStatus},
			{
				Purpose: "users_page", Method: "GET", Path: newapiRouteUsers,
				Query: url.Values{"p": {"1"}, "page_size": {strconv.Itoa(sampleSize)}},
			},
		}, nil
	default:
		return nil, fmt.Errorf("evidence-capture: unknown platform %q (must be %q or %q)", platform, PlatformSub2API, PlatformNewAPI)
	}
}

// knownMockOrWriteRoutes are routes that must never be requested by this
// tool even if a future edit to planFor gets the path wrong. Each entry is
// cited against source proof it is unsafe, not a guess:
//   - Sub2API: proven to return a hardcoded mock 200 regardless of input
//     (EV-2026-08-27-sub2api-read-survey.md "红旗" section) or gated by
//     AdminComplianceGuard write semantics not relevant to a read-only tool.
//   - NewAPI: proven by source review to write to the database despite
//     being a GET (connectors/newapi/upstream.go writeDisguisedAsGetRoutes,
//     EV-2026-08-27-newapi-read-survey.md "红旗").
//
// An entry ending in "/" names a SUBTREE: it blocks anything strictly
// beneath it but not the bare path itself - "/api/v1/admin/users/" blocks
// ".../users/u_1/usage" (the proven mock) without blocking this tool's own
// ".../users" list route. An entry without a trailing slash blocks that
// exact route *and* anything beneath it (matching
// connectors/newapi/upstream.go's own routeMatchesPrefix semantics for the
// same list: "/api/channel/test" blocks both itself and "/api/channel/test/:id").
//
// mustNotBeMockRoute is asserted against every ProbeStep before it is sent
// (see httpfetch.go) - this is defense in depth on top of planFor only ever
// constructing two fixed paths per platform.
var knownMockOrWriteRoutes = []string{
	"/api/v1/admin/users/",             // subtree: covers .../:id/usage, the proven Sub2API mock - not the bare list route
	"/api/v1/admin/dashboard/realtime", // proven mock (EV-2026-08-27 红旗)
	"/api/v1/admin/redeem-codes/stats", // proven mock
	"/api/v1/admin/groups/",            // proven mock (.../:id/stats)
	"/api/v1/admin/proxies/",           // proven mock (.../:id/stats)
	"/api/user/token",                  // silently rotates the caller's own access token
	"/api/user/aff",                    // lazy-writes an invite code on GET
	"/api/user/epay/notify",            // marks a topup order paid
	"/api/subscription/epay/",          // creates/settles a subscription
	"/api/channel/test",                // real upstream call + writes stats
	"/api/channel/update_balance",      // writes channel balance
	"/api/channel/fetch_models",        // writes back refreshed OAuth token
	"/api/oauth/",                      // creates users/sessions
}

// mustNotBeMockRoute returns an error if path is a known mock/write route,
// or beneath one - see knownMockOrWriteRoutes for the exact-vs-subtree
// distinction. A false positive here just means this tool refuses to run,
// which is always the safe failure direction for a tool whose only job is
// staying read-only.
func mustNotBeMockRoute(path string) error {
	clean := "/" + strings.Trim(path, "/")
	for _, bad := range knownMockOrWriteRoutes {
		if strings.HasSuffix(bad, "/") {
			subtreeRoot := "/" + strings.Trim(bad, "/")
			if strings.HasPrefix(clean, subtreeRoot+"/") {
				return fmt.Errorf("evidence-capture: refusing to request %s - beneath known mock/write subtree (%s)", path, bad)
			}
			continue
		}
		badClean := "/" + strings.Trim(bad, "/")
		if clean == badClean || strings.HasPrefix(clean, badClean+"/") {
			return fmt.Errorf("evidence-capture: refusing to request %s - known mock or write-disguised-as-GET route (%s)", path, bad)
		}
	}
	return nil
}

// sortedKeys is a small shared helper for deterministic map iteration in
// evidence output (field-shape tables, dropped-field lists).
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
