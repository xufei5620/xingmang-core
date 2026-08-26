# platform-worker

`platform-worker` is the River OSS queue worker for short-lived platform jobs.
It does not run workflows or perform implicit schema changes.

## Local run

Start the PostgreSQL service first:

```bash
read -s POSTGRES_DEV_PASSWORD
export POSTGRES_DEV_PASSWORD
docker compose -f deploy/compose/dev.yaml up -d --wait postgres
```

Run the explicit River lifecycle migration with a DSN that uses the same local
password (the DSN is read but never logged):

```bash
ENVIRONMENT=development \
  DATABASE_URL="postgres://xingmang:${POSTGRES_DEV_PASSWORD}@localhost:5433/xingmang?sslmode=disable" \
  go run ./cmd/platform-worker -migrate
```

Then start the worker:

```bash
DATABASE_URL="postgres://xingmang:${POSTGRES_DEV_PASSWORD}@localhost:5433/xingmang?sslmode=disable" \
  ENVIRONMENT=development \
  go run ./cmd/platform-worker
```

`HEARTBEAT_INTERVAL`, `HEARTBEAT_RUN_ON_START`, and
`HEARTBEAT_FAILURES` are optional development settings. The normal process
uses River's default retry policy and the `default` plus `maintenance` queues.

The integration test is deliberately opt-in and loopback-only so a shell's
production `DATABASE_URL` cannot be mutated by `go test`:

```bash
XM_RUN_INTEGRATION=1 \
  DATABASE_URL="postgres://xingmang:${POSTGRES_DEV_PASSWORD}@localhost:5433/xingmang?sslmode=disable" \
  go test ./internal/platform/jobs -run TestRiverWorkerPostgresIntegration -count=1 -v
```

The repository CI uses its dedicated `XM_TEST_DATABASE_URL` (loopback port
5432), which enables the same integration test without the local opt-in marker.

For non-development environments, do not put a password in `DATABASE_URL`.
Provide a password-free URL plus an explicit `DATABASE_PASSWORD_REF`; the
existing audited `SecretProvider` path then resolves the internally mapped
`DATABASE_PASSWORD` value. Inline passwords are accepted only with the explicit
`ENVIRONMENT=development` local exception above.
