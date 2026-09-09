package main

import "testing"

func TestPlanForSub2API(t *testing.T) {
	steps, err := planFor(PlatformSub2API, 5)
	if err != nil {
		t.Fatalf("planFor: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Path != sub2apiRouteVersion || steps[0].Method != "GET" {
		t.Errorf("step 0 = %+v, want GET %s", steps[0], sub2apiRouteVersion)
	}
	if steps[1].Path != sub2apiRouteUsers || steps[1].Method != "GET" {
		t.Errorf("step 1 = %+v, want GET %s", steps[1], sub2apiRouteUsers)
	}
	if got := steps[1].Query.Get("page_size"); got != "5" {
		t.Errorf("page_size = %q, want 5", got)
	}
	if got := steps[1].Query.Get("page"); got != "1" {
		t.Errorf("page = %q, want 1", got)
	}
}

func TestPlanForNewAPI(t *testing.T) {
	steps, err := planFor(PlatformNewAPI, 5)
	if err != nil {
		t.Fatalf("planFor: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Path != newapiRouteStatus {
		t.Errorf("step 0 path = %q, want %q", steps[0].Path, newapiRouteStatus)
	}
	if steps[1].Path != newapiRouteUsers {
		t.Errorf("step 1 path = %q, want %q (trailing slash required)", steps[1].Path, newapiRouteUsers)
	}
	// NewAPI's page parameter is "p", not "page" - a silent-301-then-refused-redirect bug if wrong.
	if got := steps[1].Query.Get("p"); got != "1" {
		t.Errorf("p = %q, want 1 (NOT the page= parameter name)", got)
	}
	if steps[1].Query.Get("page") != "" {
		t.Error("query must not contain a page= key for NewAPI - the real param is p=")
	}
}

func TestPlanForUnknownPlatform(t *testing.T) {
	if _, err := planFor("not-a-platform", 5); err == nil {
		t.Fatal("planFor accepted an unknown platform")
	}
}

func TestPlanForSampleSizeDefaultsAndBounds(t *testing.T) {
	steps, err := planFor(PlatformSub2API, 0)
	if err != nil {
		t.Fatalf("planFor with 0: %v", err)
	}
	if got := steps[1].Query.Get("page_size"); got != "5" {
		t.Errorf("sample size 0 should default to %d, got page_size=%s", defaultSampleSize, got)
	}

	if _, err := planFor(PlatformSub2API, maxSampleSize+1); err == nil {
		t.Fatal("planFor accepted a sample size above maxSampleSize - this tool must never become a bulk exporter")
	}

	if _, err := planFor(PlatformSub2API, maxSampleSize); err != nil {
		t.Fatalf("planFor rejected the exact max sample size: %v", err)
	}
}

func TestMustNotBeMockRouteBlocksKnownBadRoutes(t *testing.T) {
	bad := []string{
		"/api/v1/admin/users/u_10241/usage",
		"/api/v1/admin/dashboard/realtime",
		"/api/user/token",
		"/api/user/aff",
		"/api/user/epay/notify",
		"/api/channel/test",
		"/api/channel/test/123",
		"/api/oauth/callback",
	}
	for _, path := range bad {
		if err := mustNotBeMockRoute(path); err == nil {
			t.Errorf("mustNotBeMockRoute(%q) = nil, want an error", path)
		}
	}
}

func TestMustNotBeMockRouteAllowsThisToolsOwnRoutes(t *testing.T) {
	ok := []string{sub2apiRouteVersion, sub2apiRouteUsers, newapiRouteStatus, newapiRouteUsers}
	for _, path := range ok {
		if err := mustNotBeMockRoute(path); err != nil {
			t.Errorf("mustNotBeMockRoute(%q) = %v, want nil - this is one of this tool's own two probes per platform", path, err)
		}
	}
}

func TestPlanForNeverProducesAMockRoute(t *testing.T) {
	// Defense in depth pinned down: whatever planFor ever returns for either
	// platform must independently pass mustNotBeMockRoute.
	for _, platform := range []string{PlatformSub2API, PlatformNewAPI} {
		steps, err := planFor(platform, defaultSampleSize)
		if err != nil {
			t.Fatalf("planFor(%s): %v", platform, err)
		}
		for _, s := range steps {
			if err := mustNotBeMockRoute(s.Path); err != nil {
				t.Errorf("planFor(%s) produced a route that fails mustNotBeMockRoute: %s: %v", platform, s.Path, err)
			}
		}
	}
}
