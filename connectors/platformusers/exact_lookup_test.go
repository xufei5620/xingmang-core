package platformusers

import (
	"context"
	"errors"
	"testing"
)

func TestScanExactUserBoundaries(t *testing.T) {
	scan := func(pages []UserPage, userID string, max int) (User, error) {
		index, expected := 0, ""
		return ScanExactUser(context.Background(), userID, max, func(_ context.Context, cursor string) (UserPage, error) {
			if cursor != expected {
				t.Fatalf("cursor=%q expected=%q", cursor, expected)
			}
			if index >= len(pages) {
				t.Fatal("too many pages")
			}
			page := pages[index]
			index++
			expected = page.NextCursor
			return page, nil
		})
	}
	got, err := scan([]UserPage{{Users: []User{{ID: "u_1-copy"}}, NextCursor: "c2"}, {Users: []User{{ID: "u_1"}}}}, "u_1", 5)
	if err != nil || got.ID != "u_1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := scan([]UserPage{{Users: []User{{ID: "u_1-copy"}}}}, "u_1", 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	if _, err := scan([]UserPage{{NextCursor: "same"}, {NextCursor: "same"}}, "u_1", 5); !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("cycle err=%v", err)
	}
	if _, err := scan([]UserPage{{NextCursor: "2"}, {NextCursor: "3"}}, "u_1", 1); !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("limit err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanExactUser(ctx, "u_1", 2, func(ctx context.Context, _ string) (UserPage, error) { return UserPage{}, ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}
