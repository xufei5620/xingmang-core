package savedviews_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/savedviews"
)

func savedViewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 测试库: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "TRUNCATE ui.saved_view"); err != nil {
		t.Fatalf("清空 ui.saved_view: %v", err)
	}
	return pool
}

func owner(subject, environment string) savedviews.Owner {
	return savedviews.Owner{
		Issuer: "https://auth.example/realms/staff", Subject: subject,
		IdentityZone: "staff", Environment: environment,
	}
}

func view(name, query string) savedviews.SavedView {
	return savedviews.SavedView{
		ID: uuid.New(), TableKey: "platform.sub2api.channels", Name: name,
		State: savedviews.StateV1{
			SchemaVersion: 1, Query: query,
			Filters:        map[string]string{"status": "需关注"},
			Sort:           &savedviews.Sort{ColumnID: "grossProfit", Direction: savedviews.SortDesc},
			KnownColumns:   []string{"name", "status", "grossProfit"},
			VisibleColumns: []string{"name", "status"},
			Density:        savedviews.DensityCompact,
		},
	}
}

func TestStoreRoundTripsSavedViewStateV1(t *testing.T) {
	store := savedviews.NewStore(savedViewPool(t))
	ctx := context.Background()
	in := view("需关注", "openai")
	result, err := store.Set(ctx, owner("staff-sub-1", "staging"), in)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeHash != nil {
		t.Fatalf("create before hash = %q, want nil", *result.BeforeHash)
	}
	if result.After.State.Query != in.State.Query || result.After.StateHash == "" {
		t.Fatalf("round trip = %#v", result.After)
	}
	items, err := store.List(ctx, owner("staff-sub-1", "staging"), in.TableKey)
	if err != nil || len(items) != 1 || items[0].ID != result.After.ID {
		t.Fatalf("list = %#v, %v", items, err)
	}
}

func TestStoreIsolatesOwnerEnvironmentAndTable(t *testing.T) {
	store := savedviews.NewStore(savedViewPool(t))
	ctx := context.Background()
	created, err := store.Set(ctx, owner("alice", "staging"), view("A", "x"))
	if err != nil {
		t.Fatal(err)
	}
	// List is scoped by owner + environment + table_key: none of these three
	// substitutions (different owner, different environment, different table)
	// may see the row.
	listChecks := []struct {
		owner savedviews.Owner
		table string
	}{
		{owner("bob", "staging"), "platform.sub2api.channels"},
		{owner("alice", "production"), "platform.sub2api.channels"},
		{owner("alice", "staging"), "platform.newapi.channels"},
	}
	for _, check := range listChecks {
		items, listErr := store.List(ctx, check.owner, check.table)
		if listErr != nil || len(items) != 0 {
			t.Fatalf("foreign list = %#v, %v", items, listErr)
		}
	}
	// Remove takes no table_key (the action schema only accepts saved_view_id;
	// see savedviews.removeDefinition) — a row is addressed by id and scoped by
	// owner + environment only, since id alone already pins the exact row
	// (table_key included). So only a different owner or a different
	// environment must be refused here; a "different table" case has nothing
	// to assert (there is no table argument to be wrong about), and calling
	// Remove with alice/staging (the row's real owner) would just delete it.
	removeChecks := []savedviews.Owner{
		owner("bob", "staging"),
		owner("alice", "production"),
	}
	for _, foreignOwner := range removeChecks {
		if _, removeErr := store.Remove(ctx, foreignOwner, created.After.ID); !errors.Is(removeErr, savedviews.ErrNotFound) {
			t.Fatalf("foreign remove err = %v", removeErr)
		}
	}
	// The true owner can still remove it afterwards — proves the row survived
	// every foreign attempt above rather than having been silently deleted.
	if _, err := store.Remove(ctx, owner("alice", "staging"), created.After.ID); err != nil {
		t.Fatalf("owner remove err = %v", err)
	}
}

func TestStoreConcurrentCreatesCannotBypassQuota(t *testing.T) {
	store := savedviews.NewStore(savedViewPool(t))
	ctx := context.Background()
	who := owner("quota-user", "staging")
	var wg sync.WaitGroup
	errs := make(chan error, savedviews.MaxViewsPerTable+1)
	for i := 0; i < savedviews.MaxViewsPerTable+1; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Set(ctx, who, view(fmt.Sprintf("视图 %02d", i), fmt.Sprintf("q-%d", i)))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	successes, quota := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, savedviews.ErrQuotaExceeded):
			quota++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != savedviews.MaxViewsPerTable || quota != 1 {
		t.Fatalf("successes=%d quota=%d", successes, quota)
	}
}

func TestConcurrentSetsReturnSerializedHashChain(t *testing.T) {
	store := savedviews.NewStore(savedViewPool(t))
	ctx := context.Background()
	who := owner("chain-user", "staging")
	initial, err := store.Set(ctx, who, view("串行", "initial"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan savedviews.SetResult, 2)
	errs := make(chan error, 2)
	for _, q := range []string{"first", "second"} {
		go func(query string) {
			result, setErr := store.Set(ctx, who, view("串行", query))
			results <- result
			errs <- setErr
		}(q)
	}
	one, two := <-results, <-results
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	valid := one.BeforeHash != nil && two.BeforeHash != nil &&
		((*one.BeforeHash == initial.After.StateHash && *two.BeforeHash == one.After.StateHash) ||
			(*two.BeforeHash == initial.After.StateHash && *one.BeforeHash == two.After.StateHash))
	if !valid {
		t.Fatalf("non-serialized hashes: initial=%s one=%#v two=%#v", initial.After.StateHash, one, two)
	}
}
