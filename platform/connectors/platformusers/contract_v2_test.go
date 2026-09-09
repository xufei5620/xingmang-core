package platformusers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

func TestFakeV2GetUserIsExactAndSourceScoped(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC) }
	c := NewFakeClient(SourceSub2API, clock)
	got, err := c.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}})
	if err != nil || got.Ref.ID != "u_10241" || got.Snapshot.Source != "sub2api-fake" {
		t.Fatalf("GetUser=%+v err=%v", got, err)
	}
	if got.Capabilities[0] != CapabilityUserDetailRead || got.User.EmailMasked == "zhangwei@example.com" {
		t.Fatalf("detail capability/email=%+v/%q", got.Capabilities, got.User.EmailMasked)
	}
	_, err = c.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_1024"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial ID must not match: %v", err)
	}
	_, err = c.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "u_10241"}})
	if err == nil {
		t.Fatal("cross-platform ref must be rejected")
	}
}

func TestFakeV2OptionalCapabilitiesAreSourceScoped(t *testing.T) {
	sub := NewFakeClient(SourceSub2API, nil)
	if got := sub.V2Capabilities(); len(got) != 3 || got[1] != CapabilityUserDailyUsageRead || got[2] != CapabilityUserKeysMetadataRead {
		t.Fatalf("Sub2API capabilities=%v, want detail+daily+keys", got)
	}
	if got := sub.V2KeyCapabilities(); len(got) != 1 || got[0] != CapabilityUserKeysMetadataRead {
		t.Fatalf("Sub2API key capabilities=%v", got)
	}
	newapi := NewFakeClient(SourceNewAPI, nil)
	if got := newapi.V2Capabilities(); len(got) != 1 || got[0] != CapabilityUserDetailRead {
		t.Fatalf("NewAPI capabilities=%v, must not inherit Sub2API daily", got)
	}
	if got := newapi.V2KeyCapabilities(); len(got) != 0 {
		t.Fatalf("NewAPI key capabilities=%v, must remain unavailable", got)
	}
	if _, err := newapi.DailyUsage(context.Background(), DailyUsageQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "u_10241"}}); connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("NewAPI DailyUsage should be not_supported, err=%v", err)
	}
	if _, err := newapi.ListKeyMetadata(context.Background(), KeyMetadataQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "u_10241"}}); connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("NewAPI key metadata should be not_supported, err=%v", err)
	}
}
