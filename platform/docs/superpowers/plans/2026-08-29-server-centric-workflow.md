# 服务器为中心的开发与部署工作流(设计,2026-08-29)

> 背景:GitHub 托管 CI 额度用尽、本机到 github.com 推送频繁超时、gh 命令常被
> 拦截。目标:开发/门禁/部署全部在自有服务器闭环,GitHub 只做异地镜像备份。
> 状态:设计稿,产品负责人已表达意向;实施归 Codex(任务卡 XM-C-DEPLOY0)。

## 一、每项能力的替代

| GitHub 今天提供的 | 替代方案(服务器 fiberstate) |
|---|---|
| 远端仓库 / 异地备份 | 服务器裸仓库 `/srv/git/xingmang-platform.git`(SSH 推拉,内网直连不走代理);GitHub 保留为**镜像**,每次合入后 `git push --mirror github`(失败不阻塞) |
| CI 四门禁(治理/密钥扫描/后端/前端) | 裸仓库 `post-receive` 钩子:release 分支收到推送即在 Docker 容器里跑 `scripts/ci-local.sh`(四门禁同款命令),结果写 `/srv/ci/<sha>.log` + `<sha>.status`,并 Telegram 通知;**合入前提 = 该 sha 的 status 为 green** |
| PR 与 Handoff 制品 | 分支命名不变(`ai/codex/XM-…`);Handoff 从 PR 描述改为**分支里的文件** `docs/handoffs/slices/XM-….md`(随代码一起进仓库,反而更可追溯);验收线用 `git log release..branch` + Handoff 文件审读 |
| 合并到 release | 不变:验收线本地 `git merge --no-ff` 后推服务器裸仓库 |
| main 分支保护 / #43 一键合并 | 裸仓库 `pre-receive` 钩子:拒绝任何非快进推送;`main` 只接受由 `deploy/scripts/promote.sh` 发起的推送(脚本要求交互确认,由产品负责人执行)——替代 GitHub 的 PR 合并按钮 |
| 部署 | `deploy/scripts/deploy.sh <env>`:在服务器 `git fetch` → checkout release(staging)或 main(prod)→ `docker compose build` → `up -d` → 健康探针;staging 栈跑在服务器(端口经 nginx 反代+Basic Auth,替代本机 8088 成为共享验收环境) |
| Codex 冷审 Issue 通道 | 改为 `docs/reviews/` 目录下的审阅文件(提交即通知) |

## 二、安全与隔离

- 裸仓库与 CI 容器由独立用户 `gitci` 持有,仅 docker 组;CI 容器 `--network none` 除
  拉取依赖阶段(使用服务器本地的 Go/npm 缓存目录挂载,避免每次下载);
- CI Postgres 用临时容器映射到 55432(与生产库隔离);
- 钩子脚本与 ci-local.sh 都进仓库版本控制(`deploy/git-hooks/`),安装脚本一键装;
- 生产部署脚本要求显式 `--env prod` + 二次确认,并写审计日志(谁、何时、哪个 sha)。

## 三、实施切分(Codex)

1. **DEPLOY0-a**:`scripts/ci-local.sh`(把 ci.yml 四作业翻译成本地可跑脚本,本机与
   服务器同一份)+ `deploy/git-hooks/{pre-receive,post-receive}` + 安装脚本
   `deploy/scripts/install-git-server.sh`(建 gitci 用户/裸仓库/钩子/CI 目录);
2. **DEPLOY0-b**:`deploy/scripts/deploy.sh` + `promote.sh` + 服务器 staging compose
   覆盖文件(端口/反代/Basic Auth)+ Telegram 通知;
3. **DEPLOY0-c**:切换本机与 Codex 的 `origin` 到服务器;GitHub remote 改名 `github`
   并加镜像推送脚本;文档全量更新(CLAUDE.md/AGENTS.md/交接文档里的 PR 流程)。

服务器上的安装步骤(root)仍由产品负责人执行(脚本化,一条命令)。

## 四、过渡期

自托管 runner 方案(已写好脚本)与本方案不冲突:runner 装好可先解燃眉之急,
DEPLOY0 落地后 GitHub Actions 整体停用(工作流文件保留但 `on:` 改为手动触发)。
