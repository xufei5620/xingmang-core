package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Every test in this file talks only to an httptest loopback server - no
// real network access, matching connectors/platformusers/realclient_test.go's
// own approach to testing a real-mode client without a real upstream.

func TestFetchOneSuccessCarriesBodyAndAuthorizesRequest(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Api-Key")
		if r.Method != http.MethodGet {
			t.Errorf("server saw method %s, want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL)
	client := newReadOnlyHTTPClient(nil, []string{endpoint.Hostname()})
	step := ProbeStep{Purpose: "version", Method: "GET", Path: sub2apiRouteVersion}

	result := fetchOne(context.Background(), client, endpoint, step, sub2apiAuthorize("test-token-value"))
	if result.Err != nil {
		t.Fatalf("fetchOne failed: %v", result.Err)
	}
	if result.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", result.Status)
	}
	if string(result.Body) != `{"code":0,"data":{}}` {
		t.Errorf("body = %s", result.Body)
	}
	if gotAuth != "test-token-value" {
		t.Errorf("server saw X-Api-Key=%q, want test-token-value", gotAuth)
	}
}

func TestFetchOneClassifies423AsComplianceBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusLocked)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL)
	client := newReadOnlyHTTPClient(nil, []string{endpoint.Hostname()})
	step := ProbeStep{Purpose: "users_page", Method: "GET", Path: sub2apiRouteUsers}

	result := fetchOne(context.Background(), client, endpoint, step, nil)
	if !result.ComplianceBlocked {
		t.Fatal("423 was not classified as ComplianceBlocked")
	}
	if result.Err == nil {
		t.Fatal("423 must still be reported as an error (nothing to redact)")
	}
}

func TestFetchOneRefusesKnownMockRouteWithoutAnyNetworkCall(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL)
	client := newReadOnlyHTTPClient(nil, []string{endpoint.Hostname()})
	step := ProbeStep{Purpose: "sneaky", Method: "GET", Path: "/api/v1/admin/users/u_1/usage"}

	result := fetchOne(context.Background(), client, endpoint, step, nil)
	if result.Err == nil {
		t.Fatal("fetchOne allowed a known mock route through")
	}
	if called {
		t.Fatal("fetchOne reached the network for a route mustNotBeMockRoute should have refused")
	}
}

func TestFetchOneRefusesNonGET(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := newReadOnlyHTTPClient(nil, []string{endpoint.Hostname()})
	step := ProbeStep{Purpose: "bad", Method: "POST", Path: "/api/v1/admin/system/version"}

	result := fetchOne(context.Background(), client, endpoint, step, nil)
	if result.Err == nil || called {
		t.Fatal("fetchOne allowed a non-GET step through")
	}
}

func TestFetchOneReadOnlyTransportRejectsHostOutsideAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL)
	// Allowlist a *different* host than the one we are actually pointed at -
	// the shared internal/platform/connector transport must fail closed.
	client := newReadOnlyHTTPClient(nil, []string{"not-the-test-server.invalid"})
	step := ProbeStep{Purpose: "version", Method: "GET", Path: sub2apiRouteVersion}

	result := fetchOne(context.Background(), client, endpoint, step, nil)
	if result.Err == nil {
		t.Fatal("fetchOne succeeded despite the host not being in the allowlist")
	}
}

func TestFetchOneNonSuccessStatusIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := newReadOnlyHTTPClient(nil, []string{endpoint.Hostname()})
	step := ProbeStep{Purpose: "version", Method: "GET", Path: sub2apiRouteVersion}

	result := fetchOne(context.Background(), client, endpoint, step, nil)
	if result.Err == nil {
		t.Fatal("fetchOne treated HTTP 500 as success")
	}
	if result.ComplianceBlocked {
		t.Fatal("HTTP 500 must not be classified as ComplianceBlocked")
	}
}

func TestRunProbesContinuesAfterAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sub2apiRouteVersion {
			w.WriteHeader(http.StatusLocked)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":0}}`))
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL)
	client := newReadOnlyHTTPClient(nil, []string{endpoint.Hostname()})
	steps, err := planFor(PlatformSub2API, defaultSampleSize)
	if err != nil {
		t.Fatal(err)
	}
	results := runProbes(context.Background(), client, endpoint, steps, nil)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (one per step, even though the first failed)", len(results))
	}
	if !results[0].ComplianceBlocked {
		t.Error("first result should be ComplianceBlocked")
	}
	if results[1].Err != nil {
		t.Errorf("second probe should have still run and succeeded: %v", results[1].Err)
	}
}
