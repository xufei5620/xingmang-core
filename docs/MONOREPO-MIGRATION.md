# Monorepo 迁移与发布链边界

负责人于 2026-09-10 确认使用一个 monorepo 保存平台与开票系统。工作区主仓库为 `G:\xingmang\01-core`；本次切根候选位于 `G:\xingmang\09-wt\core-mono-cutover`，分支 `ai/codex/XM-MONO-CUTOVER`。逐步验收结果见 [切根交接单](handoffs/MONOREPO-CUTOVER.md)。

## 生产真相源仍待负责人决定切换

本仓库已通过 subtree 纳入两套系统的历史与源码，但未因此改变服务器生产真相源。平台仍以原服务器裸仓库 `/srv/git/xingmang-platform.git` 及生产 checkout 为准；开票原生产身份为 `v0.1.0-rc110-signed`，提交前缀 `277063c`。本次签名演练标签、传包和镜像加载均不表示部署上线。

原开票签名 tag 没有随 subtree 导入。不得将旧仓库提交号或生产标签假定为 monorepo 中可直接使用的引用。新发布清单的 `source.gitHeadScope=monorepo`，`source.gitHead` 与签名标签共同绑定完整 monorepo 提交。

## 已适配的目录约定

- `invoice/` 为开票项目根。PowerShell 脚本从自身目录推导项目根；Git 脏树判断只覆盖 `invoice/`，发布提交号仍取整个 monorepo。
- 钉版上游根为 `G:\xingmang\06-upstream\pinned`，也可由 `INVOICE_UPSTREAM_ROOT` 显式指定。未能可靠解析时拒绝继续；`06-upstream/` 中跟随最新版本的镜像不可代替钉版树。
- 开票发布 bundle 包含签名 monorepo 标签。服务器新布局的源码项目根为 `releases/<sha>/source/invoice`；演练目录为 `releases/<sha>-rehearsal/source/invoice`。部署、备份、影子评估脚本的文档路径随此前缀调整，生产配置位置不随之自动移动。
- `platform/` 为平台项目根。`deploy-local.sh` 的 `repo_path` 是 Git 顶层，`project_path` 是其下的 `platform/`，compose/env/override、self-update 和相关脚本引用从项目根解析。
- 平台正式 checkout 名称 `xingmang-platform`、既有 origin 白名单及 `release/v0.1-launch` 分支常量保持。服务器 sparse 切换步骤由负责人执行，见切根交接单；本次不改变生产 checkout 或裸仓库。
- 两套 Go module 与 pnpm workspace 保持独立，命令分别在各自子目录执行。平台规定的 Go 测试使用 `-race -p 1`；开票既有源码门禁内部的 `go test -race ./...` 保持原样。
- 开票签名标签继续使用 `v0.1.0-rcN-signed`；平台如使用发布标签，应采用 `platform/v…` 前缀。

## 门禁与手册

开票源码门禁、签名标签验证、九镜像门禁、普通与严格产物验证以及服务器验签/展开/加载的证据分别记录，镜像门禁预期退出码 `42` 表示应用镜像合格、生产上线仍受待完成 canary 阻止。严格传输就绪不等于生产放行。

平台服务器干跑须使用包含新脚本的精确提交，保留正式身份和路径约束。本次可用新 release 内的隔离 checkout 和合成 staging 配置进行演练；它不证明生产配置、Compose 实际展开、迁移或容器运行态。

平台 Git hooks 是 `platform/deploy/git-hooks/{pre-receive,post-receive}`，由服务器裸仓库侧安装与强制；本仓库不存在此前说明中的 `.githooks/commit-msg`。服务器真相源转换时须另行核对这些 hooks 的项目路径。

已知限制：`npm audit` 依赖外部服务；其旧标签/旧路径豁免分支尚不能直接用于 monorepo，服务故障时应记录失败并等待恢复，不放宽门禁。公开或发布到 GitHub、合并 main、服务器生产切换、备份、影子评估、roll-forward 和容器重启均不因本次切根自动获得授权。
