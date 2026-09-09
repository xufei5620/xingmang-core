package platformusers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type detailClient struct {
	detail connusers.UserDetail
	err    error
	called bool
	query  connusers.GetUserQuery
}

func (c *detailClient) ListUsers(context.Context, connusers.ListFilter) (connusers.UserPage, error) {
	return connusers.UserPage{}, nil
}

func (c *detailClient) GetUser(_ context.Context, query connusers.GetUserQuery) (connusers.UserDetail, error) {
	c.called = true
	c.query = query
	if c.err != nil {
		return connusers.UserDetail{}, c.err
	}
	detail := c.detail
	detail.Ref = query.Ref
	return detail, nil
}

func newDetailService(t *testing.T, client platformusers.Client) *platformusers.Service {
	t.Helper()
	service, err := platformusers.NewService(map[string]platformusers.Client{
		connusers.SourceSub2API: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service.WithClock(func() time.Time {
		return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	})
}

func actionCode(t *testing.T, err error) action.Code {
	t.Helper()
	var actionErr *action.Error
	if !errors.As(err, &actionErr) {
		t.Fatalf("expected action.Error, got %T (%v)", err, err)
	}
	return actionErr.Code
}

func TestGetUsesExactDetailReaderAndServerPeriod(t *testing.T) {
	client := &detailClient{detail: connusers.UserDetail{
		User:     connusers.User{ID: "u_10241"},
		Snapshot: connusers.EvidenceSnapshot{Source: "sub2api-fake"},
	}}
	service := newDetailService(t, client)
	detail, err := service.Get(context.Background(), platformusers.DetailInput{
		Platform: "sub2api", UserID: "u_10241", Day: "2026-08-27", Granularity: "week",
	})
	if err != nil || !client.called || detail.Ref.ID != "u_10241" {
		t.Fatalf("detail=%+v called=%v err=%v", detail, client.called, err)
	}
	if client.query.Ref.Platform != connusers.SourceSub2API || client.query.Day != "2026-08-27" || client.query.Granularity != connusers.GranularityWeek {
		t.Fatalf("query=%+v", client.query)
	}
}

func TestGetWithoutDetailReaderIsUnavailable(t *testing.T) {
	service := newDetailService(t, listOnlyClient{})
	_, err := service.Get(context.Background(), platformusers.DetailInput{Platform: "sub2api", UserID: "u_1"})
	if got := actionCode(t, err); got != action.CodeAdvancedControlsRequired {
		t.Fatalf("code=%q err=%v", got, err)
	}
}

func TestGetMapsIncompleteWithoutPretendingNotFound(t *testing.T) {
	client := &detailClient{err: connusers.ErrLookupIncomplete}
	service := newDetailService(t, client)
	_, err := service.Get(context.Background(), platformusers.DetailInput{Platform: "sub2api", UserID: "u_1"})
	if got := actionCode(t, err); got != action.CodeExecutionFailed {
		t.Fatalf("code=%q err=%v", got, err)
	}
}

func TestGetPreservesContextCancellation(t *testing.T) {
	client := &detailClient{err: context.Canceled}
	service := newDetailService(t, client)
	_, err := service.Get(context.Background(), platformusers.DetailInput{Platform: "sub2api", UserID: "u_1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type listOnlyClient struct{}

func (listOnlyClient) ListUsers(context.Context, connusers.ListFilter) (connusers.UserPage, error) {
	return connusers.UserPage{}, nil
}
