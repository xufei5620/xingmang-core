# FiberState deployment record — 2026-08-21

Target host alias: `fiberstate` (`fxg-0321`, amd64). This record intentionally
contains no credential, user row, order row, email address or invoice field.

## Pre-change backup gate

Before creating any invoice role, view, network or container, the operator
created a root-only predeployment backup set at:

```text
/root/invoice-predeploy-backups/20260820T212817Z
```

Contents and evidence:

- Sub2API PostgreSQL custom archive: approximately 461 MiB;
- New API PostgreSQL custom archive: approximately 1.8 MiB;
- both clusters' global-role definitions exported without role passwords;
- SHA-256 verification passed for all four files;
- both custom archives passed complete `pg_restore --list` parsing.

The archives were then restored with `--exit-on-error` into disposable,
network-isolated PostgreSQL containers backed only by tmpfs. Aggregate counts
matched the running databases exactly:

| Source | Evidence tuple |
|---|---|
| Sub2API | users `2648`, payment orders `2930`, auth identities `2647`, public tables `98` |
| New API | users `36`, top-ups `25`, OAuth bindings `0`, public tables `34` |

Both restore containers were removed automatically after the comparison. At
this checkpoint no Sub2API/New API source, schema, role, setting, container,
network or reverse-proxy state had been changed.

## Discovered deployment corrections

The restore proof caught the PostgreSQL 18 data-directory convention change:
new PostgreSQL 18 containers require the named volume at
`/var/lib/postgresql`, not the pre-18 `/var/lib/postgresql/data` mount. The
invoice/IdP production Compose files and restore drill were corrected before
first deployment; existing upstream PostgreSQL containers were not changed.

## Remaining change sequence

1. Freeze and verify the local release candidate.
2. Transfer it directly over SSH; no GitHub publication is authorized.
3. Generate host-local secrets and apply reviewed projection-only roles/views.
4. Start the independent stack with public/admin entry points closed.
5. Configure central OIDC and existing-account self-binding without email
   auto-merge.
6. Complete production backup, source, SMTP, PDF, logout and finance canaries
   before adding public menus or traffic.
