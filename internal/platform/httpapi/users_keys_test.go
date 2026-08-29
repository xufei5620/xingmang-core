package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	platformusers "github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type fakeKeyMetadataQuerier struct {
	page  connusers.KeyMetadataPage
	input platformusers.KeyMetadataInput
}

func (f *fakeKeyMetadataQuerier) KeyMetadata(_ context.Context, input platformusers.KeyMetadataInput) (connusers.KeyMetadataPage, error) {
	f.input = input
	return f.page, nil
}

func TestListPlatformUserKeysHandlerUsesCanonicalRefAndNullTimes(t *testing.T) {
	q := &fakeKeyMetadataQuerier{page: connusers.KeyMetadataPage{
		Items: []connusers.KeyMetadata{{
			ID: "km-1", Prefix: "sk-abcd", Status: "revoked",
			CreatedAt: time.Time{}, LastUsedAt: time.Time{}, TodayPeakRPM: connusers.UnknownCount(),
		}},
		Snapshot: connusers.EvidenceSnapshot{ObservedAt: time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC), Source: "sub2api-fake", Watermark: "wm"},
	}}
	router := chi.NewRouter()
	router.Get("/platforms/{platform}/users/{userID}/keys", ListPlatformUserKeysHandler(q))
	req := httptest.NewRequest(http.MethodGet, "/platforms/sub2api/users/u-755f3130323431/keys?limit=50&cursor=c1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || q.input.Platform != "sub2api" || q.input.UserID != "u_10241" || q.input.Limit != 50 || q.input.Cursor != "c1" {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, q.input, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"prefix":"sk-abcd"`) || !strings.Contains(body, `"status":"revoked"`) || !strings.Contains(body, `"created_at":null`) || !strings.Contains(body, `"last_used_at":null`) {
		t.Fatalf("body=%s", body)
	}
}

func TestKeyMetadataDTOHasNoSecretMaterial(t *testing.T) {
	typ := reflect.TypeOf(keyMetadataItemResponse{})
	for i := 0; i < typ.NumField(); i++ {
		if regexp.MustCompile(`(?i)full.?key|secret|credential|token.?hash|plaintext`).MatchString(typ.Field(i).Name) {
			t.Fatalf("forbidden response field: %s", typ.Field(i).Name)
		}
	}
	dto, err := toKeyMetadataPageBody(connusers.KeyMetadataPage{Items: []connusers.KeyMetadata{{ID: "km-1", Prefix: "sk-abcd", Status: "active"}}, Snapshot: connusers.EvidenceSnapshot{ObservedAt: time.Unix(1, 0), Source: "sub2api-fake"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?i)full.?key|secret|credential|token.?hash|plaintext|email|phone|tax|bank`).Match(raw) {
		t.Fatalf("forbidden JSON material: %s", raw)
	}
	if strings.Contains(string(raw), "complete-key-sentinel") {
		t.Fatal("complete key sentinel leaked")
	}
	if _, err := toKeyMetadataPageBody(connusers.KeyMetadataPage{Items: []connusers.KeyMetadata{{ID: "sk-live-complete-key", Prefix: "sk-live"}}, Snapshot: connusers.EvidenceSnapshot{ObservedAt: time.Unix(1, 0), Source: "sub2api-fake"}}); err == nil {
		t.Fatal("credential-like ID must fail the page, not be silently dropped")
	}
}

func TestNormalizeKeyStatusFailsClosed(t *testing.T) {
	for raw, want := range map[string]string{"active": "active", "revoked": "revoked", "disabled": "disabled", "mystery": "unknown"} {
		if got := normalizeKeyStatus(raw); got != want {
			t.Fatalf("normalizeKeyStatus(%q)=%q want %q", raw, got, want)
		}
	}
}
