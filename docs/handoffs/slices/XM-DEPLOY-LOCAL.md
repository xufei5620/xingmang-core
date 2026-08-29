sprint-section: 6

# XM-DEPLOY-LOCAL · 本机 Docker 部署入口

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-DEPLOY-LOCAL`
- implementation commit: 见最终 `READY` 行（脚本、契约测试、实机证据与本 Handoff 同一切片）
- base: `release/v0.1-launch` at `cfcd72c`
- stack: Compose project `xingmang-launch`；Web `127.0.0.1:8088`

## summary

本片把部署驱动模式的本机闭环固化为 `deploy/scripts/deploy-local.sh`：

1. 校验 Docker/Compose、Git checkout、staging `.env`、固定 Compose 文件与工作树；
   清理调用者的 Compose 插值变量，并将 Docker CLI 绑定到临时空配置的本机
   `default` context；
2. 在收到 `MERGED <sha>` 时刷新 `origin` 的 release ref，并校验本地 `release/v0.1-launch`
   与指定 40 位 SHA 一致；过渡期镜像不可达时，只有「指定 SHA 已是本地干净 release HEAD」
   才允许继续，并明确输出 `git-fetch=unavailable local-sha-verified`；
3. 按 `config → 既有栈基线 → build → up → Worker 状态 → 幂等 bootstrap
   → healthz/readyz → 三条 smoke` 顺序执行；已有栈的基线失败只标记
   `baseline=not-ready`，不会把旧状态冒充新部署；
4. Compose 不传 `--project-directory`，避免 Windows Docker Desktop 把
   `build.context: ../..` 错解到盘符根目录；`up` 不使用 `--wait`，因为一次性
   `bootstrap` 正常退出(0)会让 Compose v5 返回假失败，HTTP 探针负责有界等待；
5. bootstrap 使用 Compose 的迁移依赖链和现有 `001_staging_seed.sql` 的
   `ON CONFLICT DO NOTHING`，失败即停并保留容器和卷；smoke 使用 staging
   开发身份，验证 200、JSON `items` 包络以及 `sub2api-staging` 登记；
6. 不执行 `down`、删卷、`reset`、自动回滚，也不回显 `.env` 内容。

## files_changed

- `deploy/scripts/deploy-local.sh`
- `tests/deploy/deploy-local.test.sh`
- `internal/platform/httpapi/finance_test.go`（恢复当前 Go 1.27 基线格式，仅机械 gofmt）
- `docs/evidence/screens/XM-DEPLOY-LOCAL/newapi-overview.jpg`
- `docs/handoffs/slices/XM-DEPLOY-LOCAL.md`

## tests_run

- `bash -n deploy/scripts/deploy-local.sh tests/deploy/deploy-local.test.sh` — PASS
- `tests/deploy/deploy-local.test.sh` — PASS（`DEPLOY-LOCAL-TEST-OK`；覆盖完整命令链、
  dry-run、脏工作树、环境/context 隔离、基线、Worker 退出、bootstrap 失败即停、
  seed 缺失、镜像不可达时精确 SHA 兜底，以及禁止 down/reset）
- `scripts/check-governance.sh`、全部现有 security/deploy 脚本测试 — PASS
- `gofmt -l .`、`go vet ./...`、`go test -p 1 ./...` — PASS
- WSL 一次性干净副本：`pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --offline`、
  `pnpm -r run typecheck`、`pnpm -r run test`、Storybook build、admin-web build — PASS；
  WSL Node 22 对仓库要求 Node >=24 仅产生 engine warning，构建保留既有大 chunk warning；
- 真实本机栈执行（`--sha cfcd72c68956c411763cfc681489fae8943eb47b`）：
  `baseline=healthy`，随后 `bootstrap=ok`、`healthz=200`、
  `readyz=200`、`services=200`、`metrics=200`、`alerts=200`、`worker-log=available`；
  本次 fetch 已恢复时不输出 fallback；若镜像再次不可达，会按上面的精确 SHA 条件兜底；
- 实机接口摘要（经 Web `127.0.0.1:8088`）：`/healthz` 200、`/readyz` 200；
  `/api/v1/services` 200（1 条 staging 服务）、`/api/v1/metrics` 200（14 条）、
  `/api/v1/alerts` 200（3 条），均为 `items` JSON 包络；
- Worker 实机日志：`worker_started`（staging，Sub2API/NewAPI/Finance fake 模式）、
  `platform_heartbeat` `success=true` 均已观察到；脚本还校验
  `platform-worker` 处于 running。告警通知未配置的 warning 属于本地 staging
  预期状态，不影响落库与 smoke；
- 浏览器实机证据：`docs/evidence/screens/XM-DEPLOY-LOCAL/newapi-overview.jpg`；
  NewAPI 概览页已打开，页签切换至「渠道管理」后内容面板正确更新，再返回概览。

## 格 → 数据源 / 证据映射

| 运行时格 | 来源 | 证据 |
|---|---|---|
| release SHA | 本地 Git `release/v0.1-launch`；远端 fetch 仅作刷新 | 脚本输出目标 SHA；不可达镜像时精确 SHA 兜底日志 |
| Compose 栈 | `deploy/compose/launch.yaml` + `deploy/compose/.env` | `config --quiet`、`build`、`up` 均成功 |
| 健康 | Web `/healthz`、`/readyz` | 两个端点各返回 200，正文状态分别为 `ok` / `ready` |
| 幂等登记 | `deploy/bootstrap/001_staging_seed.sql` | `INSERT 0 0` / `COMMIT`，重复执行成功 |
| 烟测 | Web `/api/v1/services`、`/metrics`、`/alerts` | 200 + `items`；1 / 14 / 3 条 |
| Worker 活性 | `platform-worker` 容器日志 | `worker_started`、`platform_heartbeat success=true` |
| UI 预览 | Vite `8792` → Docker Web `8088` | 浏览器截图与页签切换实测 |

## tests_not_run

- 未在真实 Sub2API/NewAPI 上游切换凭据；本片只验证 staging fake 连接器。
- 未执行 GitHub 推送、PR 或生产部署；当前按冲刺规则停在等待验收线 `MERGED`。
- 由于当前 `origin` 的 GitHub 端口不可达，正式运行采用了「目标 SHA 已在本地 release」的
  有条件兜底；后续 origin 切换到服务器裸仓库后会自动走正常 fetch 路径。

## risks

- `--sha` 省略时脚本部署当前本地 release HEAD；收到 `MERGED` 时应始终传入验收线给出的
  40 位 SHA，避免部署旧 checkout。
- 正式模式会先 fetch；当前 GitHub 镜像不可达时，仅当传入 SHA 与本地干净
  `release/v0.1-launch` 精确相等才使用 `local-sha-verified` 兜底；没有精确 SHA
  仍然失败。
- `.env` 由 Compose 读取但不打印；真实凭据仍需按 `CREDS <平台>` 与对应操作卡切换，
  本片没有读取或修改任何上游 token。
- 本机宿主到 Docker 端口可能受 TUN 代理影响；若浏览器无法访问 8088/8792，按
  `docs/runbooks/LAUNCH.md` 的容器网络探针排查，不要删卷重建。

## follow_ups

- 等验收线返回 `MERGED <sha>`；收到后执行：
  `bash deploy/scripts/deploy-local.sh --sha <sha>`，读取输出并回报
  `DEPLOYED <sha> healthz/readyz + services/metrics/alerts smoke 摘要`。
- 后续每片仍需在运行中的 `xingmang-launch` 栈上重建受影响服务，并把接口、Worker 与
  浏览器证据写入各自 Handoff；真实接入走 `SWITCH-*-REAL.md`，不在本片提前打开。
