package platformusers

import (
	"context"
	"errors"
	"testing"
)

func TestKeyMetadataContainsNoSecretMaterial(t *testing.T) {
	for _, field := range []string{"ID", "Prefix", "Status", "CreatedAt", "LastUsedAt", "TodayPeakRPM"} {
		if field == "Secret" || field == "Token" || field == "FullKey" {
			t.Fatalf("forbidden field %s", field)
		}
	}
}

func TestKeyPrefixIsAtMostEightCodePoints(t *testing.T) {
	if err := ValidateKeyPrefix("123456789"); err == nil {
		t.Fatal("long prefix accepted")
	}
	if err := ValidateKeyPrefix("中文前缀"); err != nil {
		t.Fatal(err)
	}
}

func TestKeyMetadataIDMustBeOpaque(t *testing.T) {
	for _, id := range []string{"km-abc123", "key-meta-1", "42", "123456"} {
		if err := ValidateKeyMetadataID(id); err != nil {
			t.Fatalf("valid opaque ID %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"sk-live-complete-key", "secret://prod/key", "complete-key-sentinel", "person@example.test", "+86 13800138000", " id-with-space ", ""} {
		if err := ValidateKeyMetadataID(id); err == nil {
			t.Fatalf("credential-like ID %q accepted", id)
		}
	}
}

func TestFakeKeyMetadataIsBoundedAndMetadataOnly(t *testing.T) {
	client := NewFakeClient(SourceSub2API, nil)
	page, err := client.ListKeyMetadata(context.Background(), KeyMetadataQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}})
	if err != nil || len(page.Items) == 0 || page.Items[0].Prefix == "" || page.Snapshot.Source == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, item := range page.Items {
		if len([]rune(item.Prefix)) > 8 {
			t.Fatalf("prefix too long: %+v", item)
		}
	}
}

func TestFakeKeyMetadataCursorIsBoundToUserRef(t *testing.T) {
	client := NewFakeClient(SourceSub2API, nil)
	first, err := client.ListKeyMetadata(context.Background(), KeyMetadataQuery{
		Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}, Limit: 1,
	})
	if err != nil || first.NextCursor == "" || len(first.Items) != 1 {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := client.ListKeyMetadata(context.Background(), KeyMetadataQuery{
		Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}, Limit: 1, Cursor: first.NextCursor,
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	_, err = client.ListKeyMetadata(context.Background(), KeyMetadataQuery{
		Ref: UserRef{Platform: SourceNewAPI, ID: "u_10241"}, Cursor: first.NextCursor,
	})
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-source cursor should be rejected, err=%v", err)
	}
}

func TestFakeKeyMetadataOmitsUsersWithoutToken(t *testing.T) {
	client := NewFakeClient(SourceSub2API, nil)
	page, err := client.ListKeyMetadata(context.Background(), KeyMetadataQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10218"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("no-token user should have no key rows: %+v", page.Items)
	}
}
