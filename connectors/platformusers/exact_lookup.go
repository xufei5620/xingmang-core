package platformusers

import (
	"context"
)

type ExactPageFetcher func(context.Context, string) (UserPage, error)

func ScanExactUser(ctx context.Context, userID string, maxPages int, fetch ExactPageFetcher) (User, error) {
	if maxPages <= 0 || fetch == nil {
		return User{}, ErrLookupIncomplete
	}
	cursor := ""
	seen := map[string]struct{}{}
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return User{}, err
		}
		result, err := fetch(ctx, cursor)
		if err != nil {
			return User{}, err
		}
		for _, user := range result.Users {
			if user.ID == userID {
				return user, nil
			}
		}
		if result.NextCursor == "" {
			return User{}, ErrNotFound
		}
		if _, ok := seen[result.NextCursor]; ok {
			return User{}, ErrLookupIncomplete
		}
		seen[result.NextCursor] = struct{}{}
		cursor = result.NextCursor
	}
	return User{}, ErrLookupIncomplete
}
