package platformusers

import (
	"context"
	"errors"
	"testing"
	"time"
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
