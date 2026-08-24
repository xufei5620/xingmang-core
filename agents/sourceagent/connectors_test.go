package sourceagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNewAPIHTTPConnectorNeverVerifiesSuccess(t *testing.T) {
	var credentials *AtomicCredentialProvider
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/user/topup" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer current" {
			_ = credentials.Rotate([]byte("current"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":"","data":{"page":1,"page_size":100,"total":1,"items":[{"id":41,"user_id":7,"amount":10,"money":9.90,"trade_no":"must-not-cross","payment_method":"stripe","payment_provider":"stripe","create_time":1700000000,"complete_time":1700000060,"status":"success"}]}}`))
	}))
	defer server.Close()

	origin, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(origin.Port())
	client, err := NewRestrictedHTTPClient(server.URL, OriginPolicy{
		AllowedHosts: []string{origin.Hostname()}, AllowedPorts: []int{port},
		AllowedCIDRs: []string{"127.0.0.0/8"}, AllowPlainHTTP: true,
	}, TLSFiles{}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err = NewAtomicCredentialProvider("Authorization", "Bearer ", []byte("previous"))
	if err != nil {
		t.Fatal(err)
	}
	connector := &NewAPIHTTPConnector{HTTP: client, Credentials: credentials, Now: func() time.Time { return time.Unix(1700000100, 0) }}
	page, err := connector.Scan(context.Background(), ScanRequest{Mode: ScanIncremental, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Projections) != 1 || page.Projections[0].EntityType != EntityPaymentCandidate {
		t.Fatalf("unexpected projections: %#v", page.Projections)
	}
	payload, ok := page.Projections[0].Payload.(PaymentCandidatePayload)
	if !ok {
		t.Fatalf("unexpected payload type %T", page.Projections[0].Payload)
	}
	if payload.VerificationState != VerificationPendingManual || payload.SourceStatus != "success" {
		t.Fatalf("success was not fail-closed: %#v", payload)
	}
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), "must-not-cross") || payload.Currency != nil {
		t.Fatalf("raw trade reference or invented currency crossed boundary: %s", encoded)
	}
}

func TestSub2APIHTTPConnectorEmitsProratedGatewayRefund(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "read-only" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"items":[{"id":9,"user_id":3,"status":"PARTIALLY_REFUNDED","order_type":"balance","amount":100,"pay_amount":90,"currency":"CNY","refund_amount":20,"completed_at":"2026-08-20T10:00:00Z","refund_at":"2026-08-20T11:00:00Z","created_at":"2026-08-20T09:00:00Z","updated_at":"2026-08-20T11:00:00Z","payment_type":"stripe","provider_key":"stripe"}],"total":1,"page":1,"page_size":100,"pages":1}}`))
	}))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(origin.Port())
	client, err := NewRestrictedHTTPClient(server.URL, OriginPolicy{
		AllowedHosts: []string{origin.Hostname()}, AllowedPorts: []int{port},
		AllowedCIDRs: []string{"127.0.0.0/8"}, AllowPlainHTTP: true,
	}, TLSFiles{}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	credentials, _ := NewAtomicCredentialProvider("x-api-key", "", []byte("read-only"))
	connector := &Sub2APIHTTPConnector{HTTP: client, Credentials: credentials, Now: func() time.Time { return time.Unix(1700000100, 0) }}
	page, err := connector.Scan(context.Background(), ScanRequest{Mode: ScanReconcile, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Projections) != 2 {
		t.Fatalf("expected order and prorated gateway refund projections, got %d", len(page.Projections))
	}
	seen := map[string]bool{}
	for _, projection := range page.Projections {
		seen[projection.EntityType+":"+projection.ExternalID] = true
	}
	if !seen[EntityPaymentAdjustment+":9:refund"] {
		t.Fatalf("missing adjustment projections: %#v", seen)
	}
	adjustment := page.Projections[1].Payload.(PaymentAdjustmentPayload)
	if adjustment.Amount != "18" || adjustment.Basis != "absolute_cumulative_gateway_refund" {
		t.Fatalf("refund was not prorated to gateway money: %+v", adjustment)
	}
}

func TestSub2APIHTTPConnectorRereadsWatchedOrderByNumericID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/payment/orders" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":100,"pages":1}}`))
			return
		}
		if r.URL.Path != "/api/v1/admin/payment/orders/77" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"order":{"id":77,"user_id":3,"status":"REFUNDED","order_type":"balance","amount":100,"pay_amount":100,"currency":"CNY","refund_amount":100,"completed_at":"2026-08-20T10:00:00Z","refund_at":"2026-08-20T11:00:00Z","created_at":"2026-08-20T09:00:00Z","updated_at":"2026-08-20T11:00:00Z","payment_type":"stripe","provider_key":"stripe"},"auditLogs":[]}}`))
	}))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(origin.Port())
	client, err := NewRestrictedHTTPClient(server.URL, OriginPolicy{
		AllowedHosts: []string{origin.Hostname()}, AllowedPorts: []int{port},
		AllowedCIDRs: []string{"127.0.0.0/8"}, AllowPlainHTTP: true,
	}, TLSFiles{}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	credentials, _ := NewAtomicCredentialProvider("x-api-key", "", []byte("read-only"))
	connector := &Sub2APIHTTPConnector{HTTP: client, Credentials: credentials}
	page, err := connector.Scan(context.Background(), ScanRequest{Mode: ScanIncremental, WatchIDs: []int64{77}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Projections) != 2 || page.Projections[1].ExternalID != "77:refund" {
		t.Fatalf("watched refund was not projected: %#v", page.Projections)
	}
}

func TestRestrictedHTTPClientRejectsUnsafeOrigins(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		policy OriginPolicy
	}{
		{name: "path", origin: "https://127.0.0.1/api", policy: OriginPolicy{AllowedHosts: []string{"127.0.0.1"}, AllowedPorts: []int{443}, AllowedCIDRs: []string{"127.0.0.0/8"}}},
		{name: "userinfo", origin: "https://user@127.0.0.1", policy: OriginPolicy{AllowedHosts: []string{"127.0.0.1"}, AllowedPorts: []int{443}, AllowedCIDRs: []string{"127.0.0.0/8"}}},
		{name: "host", origin: "https://127.0.0.1", policy: OriginPolicy{AllowedHosts: []string{"10.0.0.1"}, AllowedPorts: []int{443}, AllowedCIDRs: []string{"127.0.0.0/8"}}},
		{name: "cidr", origin: "https://127.0.0.1", policy: OriginPolicy{AllowedHosts: []string{"127.0.0.1"}, AllowedPorts: []int{443}, AllowedCIDRs: []string{"10.0.0.0/8"}}},
		{name: "plain http", origin: "http://127.0.0.1", policy: OriginPolicy{AllowedHosts: []string{"127.0.0.1"}, AllowedPorts: []int{80}, AllowedCIDRs: []string{"127.0.0.0/8"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRestrictedHTTPClient(test.origin, test.policy, TLSFiles{}, time.Second); !errors.Is(err, ErrUnsafeOrigin) {
				t.Fatalf("expected unsafe origin rejection, got %v", err)
			}
		})
	}
}

func TestRestrictedHTTPClientRejectsMixedDNSAnswers(t *testing.T) {
	resolver := staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("10.0.0.2")}, {IP: net.ParseIP("203.0.113.10")}}}
	_, err := NewRestrictedHTTPClient("https://source.internal", OriginPolicy{
		AllowedHosts: []string{"source.internal"}, AllowedPorts: []int{443},
		AllowedCIDRs: []string{"10.0.0.0/8"}, Resolver: resolver,
	}, TLSFiles{}, time.Second)
	if !errors.Is(err, ErrUnsafeOrigin) {
		t.Fatalf("expected mixed DNS answer rejection, got %v", err)
	}
}

func TestRestrictedHTTPClientScopesMethodsPerOrigin(t *testing.T) {
	client, err := NewRestrictedHTTPClient("https://127.0.0.1", OriginPolicy{
		AllowedHosts: []string{"127.0.0.1"}, AllowedPorts: []int{443},
		AllowedCIDRs: []string{"127.0.0.0/8"},
	}, TLSFiles{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewRequest(context.Background(), http.MethodPost, sourceBatchPath, nil); err == nil {
		t.Fatal("GET-only upstream client accepted POST")
	}
}

type staticResolver struct{ addresses []net.IPAddr }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return append([]net.IPAddr(nil), r.addresses...), nil
}

func TestDBProjectionQueriesAreColumnScopedAndKeysetBased(t *testing.T) {
	query, _, err := sub2APIKeysetQuery(DialectPostgres, time.Unix(0, 0), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(query)
	for _, forbidden := range []string{"users", "user_email", "out_trade_no", "payment_trade_no", "provider_snapshot"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("Sub2API projection includes forbidden source %q: %s", forbidden, query)
		}
	}
	if !strings.Contains(lower, "sub2api_payments_v4('legacy_page'") || !strings.Contains(lower, "order by updated_at asc,id asc") {
		t.Fatalf("Sub2API projection is not keyset ordered: %s", query)
	}
	newQuery, _, err := newAPIKeysetQuery(DialectPostgres, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	newLower := strings.ToLower(newQuery)
	for _, forbidden := range []string{"users", "trade_no"} {
		if strings.Contains(newLower, forbidden) {
			t.Fatalf("New API projection includes forbidden source %q: %s", forbidden, newQuery)
		}
	}
	if !strings.Contains(newLower, "newapi_payments_v4('legacy_page'") || !strings.Contains(newLower, "order by id asc") {
		t.Fatalf("New API projection is not id-keyset ordered: %s", newQuery)
	}
	subIdentity, _, err := sub2APIIdentityQuery(DialectPostgres, "central", "https://id.example", time.Unix(0, 0), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	subIdentityLower := strings.ToLower(subIdentity)
	for _, forbidden := range []string{" users", "email", "metadata", " from auth_identities"} {
		if strings.Contains(subIdentityLower, forbidden) {
			t.Fatalf("Sub2API identity projection includes forbidden source %q: %s", forbidden, subIdentity)
		}
	}
	if !strings.Contains(subIdentityLower, "invoice_bridge.sub2api_identities_v4('page'") {
		t.Fatalf("Sub2API identity connector bypassed the row-filtered bridge: %s", subIdentity)
	}
	newIdentity, _, err := newAPIIdentityQuery(DialectPostgres, "central", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	newIdentityLower := strings.ToLower(newIdentity)
	for _, forbidden := range []string{" users ", "user_oauth_bindings", "custom_oauth_providers", "client_secret", "email", "access_policy"} {
		if strings.Contains(newIdentityLower, forbidden) {
			t.Fatalf("New API identity projection includes forbidden source %q: %s", forbidden, newIdentity)
		}
	}
	if !strings.Contains(newIdentityLower, "invoice_bridge.newapi_identities_v4('page'") {
		t.Fatalf("New API identity projection bypassed security-definer bridge: %s", newIdentity)
	}
}

func TestProviderEndpointsMustMatchTrustedIssuer(t *testing.T) {
	issuer := "https://id.example/realms/central"
	wellKnown := issuer + "/.well-known/openid-configuration"
	authorization := issuer + "/protocol/openid-connect/auth"
	token := issuer + "/protocol/openid-connect/token"
	userInfo := issuer + "/protocol/openid-connect/userinfo"
	if !providerEndpointsMatchIssuer(wellKnown, authorization, token, userInfo, issuer) {
		t.Fatal("complete endpoint contract for trusted issuer was rejected")
	}
	if providerEndpointsMatchIssuer(wellKnown, authorization, "", userInfo, issuer) {
		t.Fatal("incomplete provider endpoint contract was accepted")
	}
	if providerEndpointsMatchIssuer(wellKnown, authorization, "https://evil.example/token", userInfo, issuer) {
		t.Fatal("untrusted issuer endpoints were accepted")
	}
}

func TestSignedBatchRejectsTamperAndClockReplay(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := NewAtomicSigningKeyProvider("key-2026-01", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	projection := candidateProjectionForTest()
	buildTime := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	batch, err := (BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
		Now: func() time.Time { return buildTime },
	}).Build(1, "", []Projection{projection})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := SignBatch(context.Background(), batch, keys, buildTime)
	if err != nil {
		t.Fatal(err)
	}
	publicKeys := StaticPublicKeySet{"10000000-0000-4000-8000-000000000002": {"payments": {"key-2026-01": publicKey}}}
	if err := VerifyBatchSignature(batch.RawBody, metadata, publicKeys, buildTime.Add(time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("verify valid signature: %v", err)
	}
	tampered := append([]byte(nil), batch.RawBody...)
	tampered[len(tampered)-2] ^= 1
	if err := VerifyBatchSignature(tampered, metadata, publicKeys, buildTime, 5*time.Minute); !errors.Is(err, ErrBodyHashMismatch) {
		t.Fatalf("expected tamper rejection, got %v", err)
	}
	if err := VerifyBatchSignature(batch.RawBody, metadata, publicKeys, buildTime.Add(10*time.Minute), 5*time.Minute); !errors.Is(err, ErrReplay) {
		t.Fatalf("expected clock replay rejection, got %v", err)
	}
	validated, err := NewValidator("10000000-0000-4000-8000-000000000002", "payments").ValidateSigned(batch.RawBody, metadata, publicKeys, buildTime, 5*time.Minute)
	if err != nil || validated.Batch.SchemaVersion != SchemaVersionV2 {
		t.Fatalf("signed validator failed: batch=%#v err=%v", validated.Batch, err)
	}
}

func TestHTTPIngestClientSendsSignedExactBody(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := NewAtomicSigningKeyProvider("key-2026-01", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	batch, err := (BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
		Now: func() time.Time { return now },
	}).Build(1, "", []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	publicKeys := StaticPublicKeySet{"10000000-0000-4000-8000-000000000002": {"payments": {"key-2026-01": publicKey}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		sequence, parseErr := strconv.ParseUint(r.Header.Get("X-Sequence"), 10, 64)
		if parseErr != nil {
			t.Error(parseErr)
			http.Error(w, "bad sequence", http.StatusBadRequest)
			return
		}
		metadata := SignatureMetadata{
			SourceID: r.Header.Get("X-Source-ID"), BatchID: r.Header.Get("X-Batch-ID"),
			StreamID: r.Header.Get("X-Stream-ID"),
			Sequence: sequence, SentAt: r.Header.Get("X-Sent-At"),
			BodySHA256: r.Header.Get("X-Content-SHA256"), KeyID: r.Header.Get("X-Signature-Key-ID"),
			Signature: r.Header.Get("X-Signature"),
		}
		if verifyErr := VerifyBatchSignature(body, metadata, publicKeys, now, time.Minute); verifyErr != nil {
			t.Error(verifyErr)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(IngestAck{
			Accepted: true, SourceInstanceID: metadata.SourceID, BatchID: metadata.BatchID,
			StreamID: metadata.StreamID,
			Sequence: metadata.Sequence, AcceptedRecords: len(batch.Batch.Records),
		})
	}))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(origin.Port())
	httpClient, err := NewRestrictedHTTPClient(server.URL, OriginPolicy{
		AllowedHosts: []string{origin.Hostname()}, AllowedPorts: []int{port},
		AllowedCIDRs: []string{"127.0.0.0/8"}, AllowedMethods: []string{http.MethodPost}, AllowPlainHTTP: true,
	}, TLSFiles{}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := (&HTTPIngestClient{HTTP: httpClient, Keys: keys, Now: func() time.Time { return now }, AllowInsecureDevelopment: true}).Send(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.BatchID != batch.Batch.BatchID {
		t.Fatalf("unexpected ack: %#v", ack)
	}
}

func candidateProjectionForTest() Projection {
	return Projection{
		EntityType: EntityPaymentCandidate,
		ExternalID: "41",
		ObservedAt: "2026-08-20T12:00:00Z",
		Operation:  "upsert",
		Payload: PaymentCandidatePayload{
			ExternalOrderID: "41", ExternalUserID: "7", SourceStatus: "success",
			OrderType: "topup", QuotedAmount: "10", ObservedPayAmount: "9.9",
			VerificationState:  VerificationPendingManual,
			VerificationReason: "source contract does not prove settlement",
			CreatedAt:          "2026-08-20T11:00:00Z", ObservedAt: "2026-08-20T11:01:00Z",
		},
	}
}
