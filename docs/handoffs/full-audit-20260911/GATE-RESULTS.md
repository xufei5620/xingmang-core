# Full audit gate results

Generated UTC: 2026-09-10T19:45:43.585683+00:00

| Command | UTC start | UTC end | Seconds | Exit | Evidence |
|---|---|---|---:|---:|---|
| `"C:\Program Files\PowerShell\7\pwsh.exe" -NoProfile -File .\scripts\verify.ps1 (started via scripts/run-detached.ps1)` | 2026-09-10T19:12:15.260074+00:00 | 2026-09-10T19:29:27.712045+00:00 | 1032.458 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify-r6.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify-r6.stderr.log>) |
| `go.exe test -race -p 1 ./...` | 2026-09-10T19:30:44.955448+00:00 | 2026-09-10T19:35:40.864745+00:00 | 295.916 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-go-test.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-go-test.stderr.log>) |
| `go.exe vet ./...` | 2026-09-10T19:35:40.945578+00:00 | 2026-09-10T19:36:06.541019+00:00 | 25.596 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-go-vet.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-go-vet.stderr.log>) |
| `pnpm.cmd install --config.verify-deps-before-run=false --frozen-lockfile` | 2026-09-10T18:21:48.577916+00:00 | 2026-09-10T18:21:50.378937+00:00 | 1.807 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-pnpm-install.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-pnpm-install.stderr.log>) |
| `pnpm.cmd -r run typecheck` | 2026-09-10T19:36:06.611585+00:00 | 2026-09-10T19:36:21.669638+00:00 | 15.062 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-typecheck.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-typecheck.stderr.log>) |
| `pnpm.cmd -r run test` | 2026-09-10T19:36:21.795145+00:00 | 2026-09-10T19:37:08.744043+00:00 | 46.953 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-test.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-test.stderr.log>) |
| `D:\Git\bin\bash.exe scripts/check-governance.sh` | 2026-09-10T19:37:08.827942+00:00 | 2026-09-10T19:37:16.406499+00:00 | 7.579 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-governance.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-governance.stderr.log>) |
| `D:\Git\bin\bash.exe tests/security/governance-protected-paths.test.sh` | 2026-09-10T19:44:16.877368+00:00 | 2026-09-10T19:44:20.756329+00:00 | 3.880 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-governance-protection-regression-r2.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-governance-protection-regression-r2.stderr.log>) |
| `D:\Git\bin\bash.exe tests/security/compose-env-services.test.sh` | 2026-09-10T19:37:17.197304+00:00 | 2026-09-10T19:37:17.574974+00:00 | 0.382 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-compose-env-regression.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-compose-env-regression.stderr.log>) |
| `D:\Git\bin\bash.exe tests/security/full-audit-regressions.test.sh` | 2026-09-10T19:44:20.828794+00:00 | 2026-09-10T19:44:59.049654+00:00 | 38.234 | 0 | [stdout](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-full-audit-regressions-r2.stdout.log>) / [stderr](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-full-audit-regressions-r2.stderr.log>) |

## Preserved earlier attempts

Earlier failed attempts remain evidence; they are not counted as successful gates.

| Attempt | UTC start | UTC end | Seconds | Exit | Evidence |
|---|---|---|---:|---:|---|
| invoice-verify | 2026-09-10T18:33:03.557637+00:00 | 2026-09-10T18:33:23.320712+00:00 | 19.772 | 1 | [record](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify.json>) |
| invoice-verify-r2 | 2026-09-10T18:41:25.455208+00:00 | 2026-09-10T18:44:22.397106+00:00 | 176.952 | 1 | [record](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify-r2.json>) |
| invoice-verify-r3 | 2026-09-10T18:45:55.959967+00:00 | 2026-09-10T18:50:08.896083+00:00 | 252.940 | 1 | [record](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify-r3.json>) |
| invoice-verify-r4 | 2026-09-10T18:51:45.907151+00:00 | 2026-09-10T18:56:01.559871+00:00 | 255.657 | 1 | [record](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify-r4.json>) |
| invoice-verify-r5 | 2026-09-10T19:04:02.280259+00:00 | 2026-09-10T19:10:55.312265+00:00 | 413.045 | 1 | [record](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify-r5.json>) |
| platform-governance-protection-regression | 2026-09-10T19:37:16.485808+00:00 | 2026-09-10T19:37:17.123665+00:00 | 0.643 | 1 | [record](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-governance-protection-regression.json>) |
| platform-full-audit-regressions | 2026-09-10T19:37:17.645946+00:00 | 2026-09-10T19:37:26.936147+00:00 | 9.299 | 1 | [record](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-full-audit-regressions.json>) |

The first invoice failures were: an overly broad historical-tag assertion (test-only correction 4fc80c7b); a missing import in an extracted CLI fixture (test-only correction 7df825b5); Git Bash symlink mode (process-only MSYS=winsymlinks:lnk); and Unix owner/mode fixtures on NTFS (bounded local WSL test routing 824728ee). No production guard was relaxed.

All selected gate commands run against the integrated checkout. Full and targeted tests were not run concurrently. Local isolated fixtures do not establish production deployment or server state.

Invoice r5 also exposed LF checkout bytes in the generated TypeScript module; the existing generator rebuilt CRLF and proved identical normalized bytes. Platform supplemental tests initially derived a nonexistent Bash from mingw64 Git; test-only correction b201754f restored actual execution. Their successful reruns are in the main table.

Supplemental corrections and normalized-hash proof: [final report](<G:/xingmang/01-core/docs/handoffs/FULL-AUDIT-20260911.md>).
