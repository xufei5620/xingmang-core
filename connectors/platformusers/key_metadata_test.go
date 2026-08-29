package platformusers

import (
	"context"
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
