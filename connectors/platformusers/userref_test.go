package platformusers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserIDSegmentGoldenRoundTrip(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "contracts", "testdata", "platform-user-ref-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID      string `json:"id"`
		Segment string `json:"segment"`
	}
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		segment, err := EncodeUserIDSegment(tc.ID)
		if err != nil || segment != tc.Segment {
			t.Fatalf("encode %q=%q %v", tc.ID, segment, err)
		}
		id, err := DecodeUserIDSegment(segment)
		if err != nil || id != tc.ID {
			t.Fatalf("decode %q=%q %v", segment, id, err)
		}
	}
}

func TestUserRefRejectsInvalidSourcesAndIDs(t *testing.T) {
	for _, ref := range []UserRef{{}, {Platform: "cpa", ID: "1"}, {Platform: SourceSub2API, ID: ""}} {
		if err := ref.Validate(); err == nil {
			t.Fatalf("accepted %+v", ref)
		}
	}
	for _, segment := range []string{"", "u-", "raw-id", "u-0", "u-gg", "u-C2A0", "u-c0af", "u-" + strings.Repeat("61", maxUserIDBytes+1)} {
		if _, err := DecodeUserIDSegment(segment); err == nil {
			t.Fatalf("accepted %q", segment)
		}
	}
}
