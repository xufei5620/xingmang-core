# monorepo 迁移：把两个工作仓库并成一个文件夹

负责人 2026-09-10 决定：GitHub 与本地都只要一个仓库/一个文件夹。本文记录现状、要改的东西与顺序。

## 现状（2026-09-10）

- 本仓库已含两条生产线的完整历史（`platform/` 1005 个提交、`invoice/` 530 个提交，经 `git subtree add`）。
- 原仓库仍是两条发布链的真相源：平台 `K:/星芒统一控制平台/acceptance/xingmang-platform`（服务器裸仓库 `/srv/git/xingmang-platform.git`），
  开票 `K:/发票/invoice-system`（发布经 tag 上的 git bundle 传到服务器 `/root/invoice-system/app/releases/<sha>/source`）。
- 签名 tag 不随 subtree 进来（`v0.1.0-rc107/108/109/110-signed` 仍在原开票仓库）。

## 切换前必须改的（按子系统）

### invoice/
1. `scripts/verify.ps1`、`release-image-gate*.ps1`、`verify-release-image-artifacts.ps1`：所有「仓库根」推导（`$PSScriptRoot\..`）本身仍成立，但
   `git rev-parse --show-toplevel`、`gitDirty`、`source.gitHead` 取的是 monorepo 根；镜像门禁包装里的「worktree/tag/HEAD 绑定」要按 monorepo 路径比。
2. 发布 bundle 与服务器 stage2：bundle 变成整个 monorepo 的 tag；服务器展开后源码根是 `…/source/invoice`，`roll-forward.sh`、
   `backup.sh`、`rehearsal/shadow-eval.sh` 的调用路径与手册第 3/9/11/12 节相应加 `invoice/` 前缀；`.env.production` 位置不变。
3. `test-release-image-gate.ps1` 对 `docs/PRODUCTION-RUNBOOK.md` 的逐行静态扫描路径改为 `invoice/docs/…`（或保持相对 `$projectRoot`）。
4. tag 命名：开票继续用 `v0.1.0-rcN-signed`，平台若也打 tag 用 `platform/v…` 前缀，避免撞名。

### platform/
1. 服务器裸仓库与 `/srv/deploy/xingmang-platform` 检出：改为 monorepo（`deploy-local.sh` 的 compose/override 路径加 `platform/` 前缀，或在服务器上用
   `git sparse-checkout set platform` 并把工作目录指到 `platform/`）。
2. `.githooks/commit-msg`（`Acceptance-Line: claude` trailer）、`scripts/check-governance.sh`、gitleaks 基线：路径前缀。
3. pnpm workspace（`web/pnpm-workspace.yaml`）与 `go.mod` 都在 `platform/` 下，不受根目录影响；CI/门禁命令在 `platform/` 内执行。

### 通用
- `.gitignore`：两份各在子目录，根目录不需要。
- 所有 worktree（`wt-XM-*`）都是原仓库的；迁移后新工作树从本仓库开，路径写进各自 handoff。
- 两次演练后再切：平台 `deploy-local.sh` 干跑一次、开票走一次完整 RC（源码门禁 → 镜像门禁 → bundle → stage2 → 影子评估）。

## 顺序

1. 本仓库推到 GitHub（私有），作为唯一远端。
2. 原仓库冻结（不再开新分支），新工作只在本仓库。
3. 先改开票发布链并跑一次 RC 演练（不上线），再改平台部署链并干跑；两条都过后把服务器真相源切过来。
4. 公开前按 `docs/PUBLIC-RELEASE-CHECKLIST.md`（待写）清洗运营数据；公开的是清洗后的快照。
