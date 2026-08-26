package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
)

func selectIntegrationDSN() (string, bool, error) {
	if dsn := strings.TrimSpace(os.Getenv("XM_TEST_DATABASE_URL")); dsn != "" {
		if err := validateIntegrationDSN(dsn); err != nil {
			return "", true, err
		}
		return dsn, true, nil
	}
	if os.Getenv("XM_RUN_INTEGRATION") != "1" {
		return "", false, nil
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return "", true, fmt.Errorf("XM_RUN_INTEGRATION=1 requires DATABASE_URL")
	}
	if err := validateIntegrationDSN(dsn); err != nil {
		return "", true, err
	}
	return dsn, true, nil
}

// validateIntegrationDSN 确保集成测试只打本机库。
//
// 判定依据必须是 **pgx 实际会连哪里**，不是 DSN 字符串看起来像什么：
// postgres://u@127.0.0.1/db?host=prod.example.com 看着是 loopback，
// pgx 连的却是 prod.example.com（见 internal/platform/pgdsn 包注释）。
func validateIntegrationDSN(dsn string) error {
	if err := pgdsn.Validate(dsn, false); err != nil {
		return err
	}
	return pgdsn.RequireLoopback(dsn)
}

func TestRiverWorkerPostgresIntegration(t *testing.T) {
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set XM_TEST_DATABASE_URL (CI) or XM_RUN_INTEGRATION=1 with a local DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool, nil); err != nil {
		t.Fatalf("River migration: %v", err)
	}

	var riverJobTable string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('river_job')`).Scan(&riverJobTable); err != nil {
		t.Fatal(err)
	}
	if riverJobTable != "river_job" {
		t.Fatalf("River migration did not create river_job: %q", riverJobTable)
	}

	periodicRunID := fmt.Sprintf("periodic-%d", time.Now().UnixNano())

	client, err := NewClient(pool, Config{
		Environment:         "test",
		HeartbeatInterval:   time.Second,
		HeartbeatRunOnStart: true,
		HeartbeatRunID:      periodicRunID,
		HeartbeatFailures:   1,
		MaxWorkers:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := client.Stop(stopCtx); err != nil {
			t.Errorf("stop River client: %v", err)
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var periodicCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'run_id' = $2`, HeartbeatJobKind, periodicRunID).Scan(&periodicCount); err == nil && periodicCount >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var periodicCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'run_id' = $2`, HeartbeatJobKind, periodicRunID).Scan(&periodicCount); err != nil {
		t.Fatal(err)
	}
	if periodicCount < 1 {
		t.Fatal("RunOnStart periodic heartbeat was not inserted")
	}
	var queueCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_queue WHERE name IN ('default', 'maintenance')`).Scan(&queueCount); err != nil {
		t.Fatal(err)
	}
	if queueCount != 2 {
		t.Fatalf("River queues = %d, want default + maintenance", queueCount)
	}

	args := HeartbeatArgs{
		RunID:                 periodicRunID + "-manual",
		FailuresBeforeSuccess: 1,
	}
	first, err := client.Insert(ctx, args, nil)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if first.UniqueSkippedAsDuplicate {
		t.Fatal("first insert unexpectedly skipped")
	}
	second, err := client.Insert(ctx, args, nil)
	if err != nil {
		t.Fatalf("duplicate insert: %v", err)
	}
	if !second.UniqueSkippedAsDuplicate {
		t.Fatal("duplicate heartbeat insert was not skipped")
	}

	deadline = time.Now().Add(20 * time.Second)
	var attempts int
	var state string
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `SELECT attempt, state FROM river_job WHERE id = $1`, first.Job.ID).Scan(&attempts, &state); err == nil && attempts >= 2 && state == "completed" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("heartbeat did not retry and complete; final attempts=%d state=%s", attempts, state)
}
