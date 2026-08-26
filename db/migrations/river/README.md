# River OSS migration mirror

This directory is an auditable mirror of the PostgreSQL migrations bundled by
River OSS v0.45.0. The files are copied without SQL changes from the official
`riverdriver/riverpgxv5/migration/main` tree at the upstream release tag:

- Repository: <https://github.com/riverqueue/river>
- Release tag: `v0.45.0`
- Release commit: `eced75d6c957a66a52fdaa7e1ed2395c5d01122e`
- PostgreSQL driver module tag: `riverdriver/riverpgxv5/v0.45.0`
- PostgreSQL driver tag commit: `f98fdd171aec9b037341c4636ba50d4921ffc89e`
- Upstream source path: `riverdriver/riverpgxv5/migration/main/`

The `/* TEMPLATE: schema */` markers are part of River's official SQL. River
replaces them at runtime with the configured PostgreSQL schema; leaving them in
the mirror preserves provenance and makes an accidental hand-written schema
change visible in review. The platform's lifecycle migration entry point uses
River's embedded bundle from the pinned Go module; this directory is the
reviewable source mirror and is not a second, independently versioned migration
line.

## Files and SHA-256

The hashes below are calculated from the upstream tag's files. The SQL bodies
and filenames in this directory correspond one-to-one with those files (the
repository's patch format may normalize a missing final newline).

| Version | Direction | Upstream filename | SHA-256 |
| --- | --- | --- | --- |
| 001 | down | `001_create_river_migration.down.sql` | `34c87dc594bf7520bc3ae69f6f0da8d2d9a472616ab38b37e63d4e3838da06d2` |
| 001 | up | `001_create_river_migration.up.sql` | `79def9ab1643beee7776c499559ec199a03b5b26036c122dc3ba13ec3d078dc0` |
| 002 | down | `002_initial_schema.down.sql` | `8e7e73755b3e9cd1d46f0dffeadd427b86af13cea2f41f3d30af1624329db9b9` |
| 002 | up | `002_initial_schema.up.sql` | `8915c00d08ed98625865c705b6fd0bd14c113b7cdd0cb218ee894eca1d32ad03` |
| 003 | down | `003_river_job_tags_non_null.down.sql` | `bca44f6f0e926411c9e26e7ce2598bbdb5102b286f380135d9a5bcd96a77cbb8` |
| 003 | up | `003_river_job_tags_non_null.up.sql` | `dedb183bb302c005bc72caf2901ff693bbab11413308c5e0567ddffb51e667ef` |
| 004 | down | `004_pending_and_more.down.sql` | `91b5ced7b9d707a0de73f5b312596935950b70229f58aa9bf3ca362aa7a408c8` |
| 004 | up | `004_pending_and_more.up.sql` | `3f7418b0cf78ede9a9ec730bdfc4389a84e05989531b205fc4ece0d2bb10e390` |
| 005 | down | `005_migration_unique_client.down.sql` | `de84dca49a5d618d2a4973b13a69830fbebb0f9635babfa50c6f19577193425b` |
| 005 | up | `005_migration_unique_client.up.sql` | `b760f487152c7d92102869d46b8a64dc1e2094d5675e690ffbe52a747eee8431` |
| 006 | down | `006_bulk_unique.down.sql` | `726483f6e5aa7dd02cdd974cd7bf716973a8d0a97ba6dbc5dc5304aaf54c7ad7` |
| 006 | up | `006_bulk_unique.up.sql` | `3b133f7ce4662d3dc8bd4a57628e0e116a300b2635a79315557aa1369849f0fb` |
| 007 | down | `007_notification_outbox_sqlite_jsonb_and_sql_cleanup.down.sql` | `9131aae235187dbdaaa822dab2a475a884e917d9af05e3c98fb95c152eaa769a` |
| 007 | up | `007_notification_outbox_sqlite_jsonb_and_sql_cleanup.up.sql` | `47ec8031b88e69004de2def5bc3109d969f71ee4c33a1e7dac2fb8c9dd19182d` |

Do not replace these files with a newer River migration set without updating
`VERSIONS.lock` and the task/PR evidence together.
