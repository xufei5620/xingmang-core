# Upstream integrity boundary

The invoice system is an external integration. The following source trees are
read-only evidence and must never receive invoice feature changes:

| Upstream | Local path | Baseline commit |
| --- | --- | --- |
| Sub2API | `G:\xingmang\06-upstream\pinned\sub2api-upstream-v0.1.157` | `a2779cd5f30d6d3904a9d59088aed09507678dfe` |
| Sub2API v0.1.179 contract snapshot | `G:\xingmang\06-upstream\pinned\_research\sub2api-contract-v0.1.179` | `75f88be5f75c27771836b586f7de1503afa0e3bc` |
| New API (research snapshot) | `G:\xingmang\06-upstream\pinned\_research\new-api-agent` | `f116414284162ad15d8925f7bca494c109b83e93` |
| New API (second research snapshot) | `G:\xingmang\06-upstream\pinned\_research\new-api-local` | `f116414284162ad15d8925f7bca494c109b83e93` |

The integrity gate accepts an absolute `INVOICE_UPSTREAM_ROOT` override. Without
an override it locates the workspace through the Git common directory, so both
the main checkout and linked worktrees use `06-upstream/pinned`. A missing root,
missing snapshot, failed Git command, changed HEAD or dirty snapshot fails closed.
The latest mirrors at `G:\xingmang\06-upstream\new-api` and
`G:\xingmang\06-upstream\sub2api` are not substitutes for these pinned trees.

Rules:

1. Do not edit, patch, commit, mount, vendor, or generate files in these trees.
2. Do not run invoice migrations against either upstream database.
3. Upstream data access is implemented through separately deployed read-only
   source agents and versioned contracts.
4. Production configuration changes (OIDC, menu entries, read-only database
   roles, Nginx, DNS, SMTP, or deployment) require a separate approval.
5. Before every delivery, compare `git status --porcelain=v1` and `HEAD` with
   the baseline above.
