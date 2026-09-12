package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testOwner = "0123456789abcdef0123456789abcdef"
const testProject = "xm-rehearsal-provider-test"

func testFixtures() fixtures {
	return fixtures{SchemaVersion: 1, Clients: map[string]fixtureClient{
		"sub2api": {Identifier: "sub@example.invalid", Password: "sub-fixture-random-password-00001", Subject: "700001", Email: "sub@example.invalid"},
		"newapi":  {Identifier: "new-fixture", Password: "new-fixture-random-password-00002", Subject: "700002", Email: "new@example.invalid"},
	}}
}

func testGuard() fixtureGuard {
	return fixtureGuard{SchemaVersion: 1, Mode: "server-rehearsal", Owner: testOwner, Project: testProject, BindIP: "172.29.99.8", NetworkCIDR: "172.29.99.0/24", ClientSourceIDs: map[string]string{
		"sub2api": strings.Join([]string{"c0ac81fb", "6c5e", "47a2", "9a68", "1284da59f5d1"}, "-"),
		"newapi":  strings.Join([]string{"5b6e2c90", "3157", "4253", "ac99", "aaeb48e77f60"}, "-"),
	}}
}

func TestGuardRejectsUnsafeListenerOrIdentity(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*fixtureGuard)
		listen  string
		owner   string
		project string
	}{
		{"production", func(g *fixtureGuard) { g.Mode = "production" }, "172.29.99.8:18080", testOwner, testProject},
		{"public", func(g *fixtureGuard) { g.BindIP = "8.8.8.8" }, "8.8.8.8:18080", testOwner, testProject},
		{"wildcard", func(g *fixtureGuard) { g.BindIP = "0.0.0.0" }, "0.0.0.0:18080", testOwner, testProject},
		{"ipv6-wildcard", func(g *fixtureGuard) { g.BindIP = "::" }, "[::]:18080", testOwner, testProject},
		{"hostname", func(g *fixtureGuard) { g.BindIP = "localhost" }, "localhost:18080", testOwner, testProject},
		{"different-listen", func(g *fixtureGuard) {}, "172.29.99.9:18080", testOwner, testProject},
		{"missing-owner", func(g *fixtureGuard) { g.Owner = "" }, "172.29.99.8:18080", testOwner, testProject},
		{"wrong-owner", func(g *fixtureGuard) {}, "172.29.99.8:18080", strings.Repeat("f", 32), testProject},
		{"production-project", func(g *fixtureGuard) { g.Project = "production" }, "172.29.99.8:18080", testOwner, "production"},
		{"wrong-project", func(g *fixtureGuard) {}, "172.29.99.8:18080", testOwner, "xm-rehearsal-other"},
		{"same-sources", func(g *fixtureGuard) { g.ClientSourceIDs["newapi"] = g.ClientSourceIDs["sub2api"] }, "172.29.99.8:18080", testOwner, testProject},
		{"missing-source", func(g *fixtureGuard) { delete(g.ClientSourceIDs, "newapi") }, "172.29.99.8:18080", testOwner, testProject},
		{"extra-source", func(g *fixtureGuard) { g.ClientSourceIDs["other"] = g.ClientSourceIDs["newapi"] }, "172.29.99.8:18080", testOwner, testProject},
		{"port-zero", func(g *fixtureGuard) {}, "172.29.99.8:0", testOwner, testProject},
		{"loopback", func(g *fixtureGuard) { g.BindIP = "127.0.0.1"; g.NetworkCIDR = "127.0.0.0/8" }, "127.0.0.1:18080", testOwner, testProject},
		{"missing-network", func(g *fixtureGuard) { g.NetworkCIDR = "" }, "172.29.99.8:18080", testOwner, testProject},
		{"broad-network", func(g *fixtureGuard) { g.NetworkCIDR = "0.0.0.0/0" }, "172.29.99.8:18080", testOwner, testProject},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := testGuard()
			c.mutate(&g)
			if err := validateGuard(g, c.listen, c.owner, c.project); err == nil {
				t.Fatal("unsafe fixture provider accepted")
			}
		})
	}
	if err := validateGuard(testGuard(), "172.29.99.8:18080", testOwner, testProject); err != nil {
		t.Fatal(err)
	}
	g := testGuard()
	g.Mode = "local-synthetic"
	g.BindIP = "11.240.7.8"
	g.NetworkCIDR = "11.240.7.0/24"
	if err := validateGuard(g, "11.240.7.8:18080", testOwner, testProject); err != nil {
		t.Fatal(err)
	}
}

func TestFixturesRejectRealOrAmbiguousAccounts(t *testing.T) {
	for _, name := range []string{"real-email", "short-password", "bad-subject", "sub-identifier", "missing-platform", "same-password"} {
		t.Run(name, func(t *testing.T) {
			f := testFixtures()
			c := f.Clients["sub2api"]
			switch name {
			case "real-email":
				c.Email = "someone@example.com"
				c.Identifier = c.Email
			case "short-password":
				c.Password = "password"
			case "bad-subject":
				c.Subject = "0"
			case "sub-identifier":
				c.Identifier = "different@example.invalid"
			case "missing-platform":
				delete(f.Clients, "newapi")
			case "same-password":
				c.Password = f.Clients["newapi"].Password
			}
			f.Clients["sub2api"] = c
			if err := validateFixtures(f); err == nil {
				t.Fatal("unsafe fixture account accepted")
			}
		})
	}
}

func TestConfigurationOnlyReadsPrivateAbsoluteRegularFiles(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "fixtures.json")
	guard := filepath.Join(dir, "guard.json")
	f := testFixtures()
	fJSON, _ := json.Marshal(f)
	// The shared seed document can also contain opaque staff fields.
	var shared map[string]any
	_ = json.Unmarshal(fJSON, &shared)
	shared["staff"] = map[string]any{"ignored": "synthetic"}
	fJSON, _ = json.Marshal(shared)
	gJSON, _ := json.Marshal(testGuard())
	if err := os.WriteFile(private, fJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, gJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfiguration(private, guard, "172.29.99.8:18080", testOwner, testProject); err != nil {
		t.Fatal(err)
	}
	for _, files := range [][2]string{{"fixtures.json", guard}, {private, "guard.json"}, {dir, guard}} {
		if _, err := loadConfiguration(files[0], files[1], "172.29.99.8:18080", testOwner, testProject); err == nil {
			t.Fatal("unsafe fixture path accepted")
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(private, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfiguration(private, guard, "172.29.99.8:18080", testOwner, testProject); err == nil {
			t.Fatal("public-readable private fixture accepted")
		}
	}
}

func TestProviderLoginAndProfileContracts(t *testing.T) {
	f := testFixtures()
	h := newProvider(f)
	for _, c := range []struct{ platform, login, profile, field string }{
		{"sub2api", "/api/v1/auth/login", "/api/v1/auth/me", "email"},
		{"newapi", "/api/user/login", "/api/user/self", "username"},
	} {
		t.Run(c.platform, func(t *testing.T) {
			client := f.Clients[c.platform]
			body, _ := json.Marshal(map[string]string{c.field: client.Identifier, "password": client.Password})
			r := httptest.NewRequest(http.MethodPost, c.login, bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("login status %d", w.Code)
			}
			var envelope struct {
				Code    int  `json:"code"`
				Success bool `json:"success"`
				Data    struct {
					AccessToken string `json:"access_token"`
					User        struct {
						ID    int64  `json:"id"`
						Email string `json:"email"`
					} `json:"user"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if c.platform == "sub2api" && envelope.Code != 0 || c.platform == "newapi" && !envelope.Success {
				t.Fatal("wrong platform envelope")
			}
			if envelope.Data.User.ID <= 0 || envelope.Data.User.Email != client.Email {
				t.Fatal("wrong fixture identity")
			}
			if bytes.Contains(w.Body.Bytes(), []byte(client.Password)) {
				t.Fatal("password leaked in response")
			}
			profile := httptest.NewRequest(http.MethodGet, c.profile, nil)
			if c.platform == "sub2api" {
				profile.Header.Set("Authorization", "Bearer "+envelope.Data.AccessToken)
			} else {
				cookies := w.Result().Cookies()
				if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure {
					t.Fatal("missing secure profile session")
				}
				profile.AddCookie(cookies[0])
			}
			pw := httptest.NewRecorder()
			h.ServeHTTP(pw, profile)
			if pw.Code != 200 || !strings.Contains(pw.Body.String(), client.Email) {
				t.Fatal("authenticated fixture profile unavailable")
			}
			unauth := httptest.NewRecorder()
			h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, c.profile, nil))
			if unauth.Code != http.StatusUnauthorized {
				t.Fatal("profile allowed without fixture session")
			}
			profile.URL.Path = "/api/user/self"
			profile.AddCookie(&http.Cookie{Name: "session", Value: envelope.Data.AccessToken})
			if c.platform == "newapi" {
				profile.URL.Path = "/api/v1/auth/me"
				profile.Header.Set("Authorization", "Bearer "+w.Result().Cookies()[0].Value)
			}
			cross := httptest.NewRecorder()
			h.ServeHTTP(cross, profile)
			if cross.Code != http.StatusUnauthorized {
				t.Fatal("profile crossed platform boundary")
			}
		})
	}
}

func TestProviderRejectsWrongCredentialsAndFinancialWrites(t *testing.T) {
	f := testFixtures()
	h := newProvider(f)
	for _, c := range []struct {
		platform, path, field string
		status                int
	}{
		{"sub2api", "/api/v1/auth/login", "email", 401}, {"newapi", "/api/user/login", "username", 200},
	} {
		for _, password := range []string{"wrong", f.Clients[map[string]string{"sub2api": "newapi", "newapi": "sub2api"}[c.platform]].Password} {
			body, _ := json.Marshal(map[string]string{c.field: f.Clients[c.platform].Identifier, "password": password})
			r := httptest.NewRequest("POST", c.path, bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.status || strings.Contains(w.Body.String(), `"success":true`) || strings.Contains(w.Body.String(), `"user"`) {
				t.Fatal("wrong credentials accepted")
			}
		}
	}
	for _, path := range []string{"/api/v1/invoice-requests", "/api/user/topup", "/api/v1/auth/refresh", "/api/user/login/2fa"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if w.Code != 404 {
			t.Fatalf("unimplemented write route accepted: %s", path)
		}
	}
	for _, body := range []string{`{"email":"sub@example.invalid","password":"sub-fixture-random-password-00001","unexpected":true}`, `{} {}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("malformed login body accepted")
		}
	}
}

func TestHealthProbeContainsOnlyFixtureOwner(t *testing.T) {
	h := newProvider(testFixtures())
	h.owner = testOwner
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(response) != 2 || response["fixture"] != true || response["owner"] != testOwner {
		t.Fatal("fixture owner health proof missing")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/healthz", nil))
	if w.Code != 405 {
		t.Fatal("writable fixture health endpoint")
	}
}

func TestCLIShredIsGuardedAndDoesNotStartProvider(t *testing.T) {
	dir, guard, g := shredFixture(t)
	args := []string{"--shred-fixtures", dir, "--fixture-guard", guard, "--listen", g.BindIP + ":18080", "--owner", g.Owner, "--project", g.Project}
	wrong := append([]string{}, args...)
	wrong[len(wrong)-3] = strings.Repeat("f", 32)
	if err := run(wrong); err == nil {
		t.Fatal("CLI shred accepted wrong owner")
	}
	if _, err := os.Stat(guard); err != nil {
		t.Fatal("CLI refusal touched owner guard")
	}
	if err := run(append(args, "--generate-tls")); err == nil {
		t.Fatal("ambiguous destructive CLI mode accepted")
	}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	remaining, err := os.ReadDir(dir)
	if err != nil || len(remaining) != 0 {
		t.Fatal("CLI did not empty only the owned fixture mount")
	}
}
