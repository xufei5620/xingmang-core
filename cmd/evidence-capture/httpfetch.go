package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

const (
	// requestTimeout matches the platform's existing real connectors
	// (connectors/platformusers/client.go defaultRequestTimeout) - there is
	// no reason an evidence probe needs a different budget.
	requestTimeout = 15 * time.Second
	// maxResponseBytes caps a single response body. 4 MiB is generous for a
	// version probe or a page of at most maxSampleSize rows; anything
	// bigger is treated as bad_response rather than read into memory.
	maxResponseBytes = 4 << 20
	userAgent        = "xingmang-platform/evidence-capture"
)

// newReadOnlyHTTPClient builds the same GET/HEAD-only, host-allowlisted,
// no-redirect transport the platform's real connectors use
// (internal/platform/connector.NewReadOnlyClientWithBase - ADR-018's four
// read-only gates). base is nil in production; tests inject an httptest
// transport the same way connectors/platformusers/realclient_test.go does.
func newReadOnlyHTTPClient(base http.RoundTripper, allowlist []string) *http.Client {
	return connector.NewReadOnlyClientWithBase(base, allowlist, requestTimeout)
}

// fetchResult is the outcome of one ProbeStep, success or failure. This
// tool always reports every step it attempted (see runProbes) rather than
// aborting at the first error, because a partial capture ("version worked,
// users page got 423") is itself useful diagnostic evidence for the
// operator running it, not a reason to hide what did work.
type fetchResult struct {
	Step              ProbeStep
	Status            int
	Body              []byte
	ObservedAt        time.Time
	LatencyMS         int64
	Err               error
	ComplianceBlocked bool // true iff Status == 423 (Sub2API AdminComplianceGuard)
}

// fetchOne issues exactly one read-only GET. It re-asserts
// mustNotBeMockRoute and the GET-only method even though planFor already
// guarantees both - this function has no way to know whether its caller is
// the real planFor or a test/future refactor, and the cost of checking
// twice is a few nanoseconds against the cost of an evidence tool ever
// hitting a write-disguised-as-GET route.
func fetchOne(ctx context.Context, httpClient *http.Client, endpoint *url.URL, step ProbeStep, authorize func(*http.Request)) fetchResult {
	result := fetchResult{Step: step}
	if err := mustNotBeMockRoute(step.Path); err != nil {
		result.Err = err
		return result
	}
	if step.Method != http.MethodGet {
		result.Err = fmt.Errorf("evidence-capture: refusing non-GET probe %s %s", step.Method, step.Path)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}

	target := *endpoint
	target.Path = strings.TrimSuffix(endpoint.Path, "/") + step.Path
	if len(step.Query) > 0 {
		target.RawQuery = step.Query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		result.Err = fmt.Errorf("evidence-capture: failed to build request for %s: %w", step.Purpose, err)
		return result
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if authorize != nil {
		authorize(req)
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	result.ObservedAt = start.UTC()
	result.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		// The transport's own error text is safe to surface here: it comes
		// from Go's net/http or from connector.ReadOnlyTransport's own
		// KindForbiddenTarget/KindWriteAttempt errors, never from an
		// upstream response body.
		result.Err = fmt.Errorf("evidence-capture: request %s failed: %w", step.Purpose, err)
		return result
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	result.Status = resp.StatusCode
	if resp.StatusCode == http.StatusLocked {
		result.ComplianceBlocked = true
		result.Err = fmt.Errorf(
			"evidence-capture: %s got HTTP 423 - AdminComplianceGuard has not been accepted for this credential's account; "+
				"a human must complete compliance acceptance in the Sub2API admin console before this tool can capture evidence "+
				"(see docs/runbooks/USERS-REAL-APPROVAL.md); this tool will never send that POST itself", step.Purpose)
		return result
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The upstream response body never enters this error - only the
		// status code does (same discipline as
		// connectors/platformusers/client.go's classifyStatus).
		result.Err = fmt.Errorf("evidence-capture: %s got HTTP %d", step.Purpose, resp.StatusCode)
		return result
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		result.Err = fmt.Errorf("evidence-capture: %s: failed to read response body: %w", step.Purpose, err)
		return result
	}
	if len(body) > maxResponseBytes {
		result.Err = fmt.Errorf("evidence-capture: %s response exceeded %d byte cap", step.Purpose, maxResponseBytes)
		return result
	}
	result.Body = body
	return result
}

// runProbes issues every step in order and returns every result, success or
// failure - see fetchResult's doc comment for why this never aborts early.
func runProbes(ctx context.Context, httpClient *http.Client, endpoint *url.URL, steps []ProbeStep, authorize func(*http.Request)) []fetchResult {
	out := make([]fetchResult, 0, len(steps))
	for _, step := range steps {
		out = append(out, fetchOne(ctx, httpClient, endpoint, step, authorize))
	}
	return out
}

// sub2apiAuthorize / newapiAuthorize set the header shape each platform's
// admin API expects (connectors/platformusers/client.go authorize: Sub2API
// takes the raw token in X-Api-Key, NewAPI takes it as a Bearer token).
func sub2apiAuthorize(token string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set("X-Api-Key", token) }
}

func newapiAuthorize(token string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+token) }
}
