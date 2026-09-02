# XM-DEPLOY-CPAWAIT0 · cpa-observations 等待预算改用完整周期

## status

READY（待验收线审读、复跑并人工合入）

## branch / commit / base

- branch: `ai/claude/XM-DEPLOY-CPAWAIT0`
- base: `release/v0.1-launch@1869517c9ea0a2632d49a30153acc60cff2ddb97`
- worktree: `K:/星芒统一控制平台/wt-xmCPAWAIT0`
- commits（从旧到新，见 `git log ai/claude/XM-DEPLOY-CPAWAIT0`）：
  1. `fix(deploy): derive cpa-observations wait budget from XM_CPA_SYNC_INTERVAL`
  2. `test(deploy): cover cpa-observations wait budget and polling`
  3. `docs(deploy): document cpa-observations wait budget in runbook`
  4. `docs(handoffs): add XM-DEPLOY-CPAWAIT0 handoff`（本文件）

## 问题

`deploy-local.sh` 的 `cpa-observations` 阶段过去复用与其它探针共享的
`probe_attempts`（默认 12）× 2s 睡眠，等待窗口约 24s。`cpa_sync` 周期任务默认
5 分钟跑一次，worker 容器刚被重建时，即使该任务的 `RunOnStart` 会让它一启动
就尝试同步一次，也没有一个可靠的亚分钟级上界能保证这次尝试已经把四条观测
写库完成；唯一确定的上界是"最迟不超过下一个正常周期节拍"。团队交接报告
2026-09-02 两次生产部署都因为这个 24s 窗口过短被误判失败——两次事后查询
都显示状态是完全正确的，只是晚了几分钟才出现。

## summary

### 1. 等待预算公式

```
budget_seconds = interval_seconds + 60
interval_seconds = parse(XM_CPA_SYNC_INTERVAL)      # 从 --env-file 读取
                  或 CPA_SYNC_DEFAULT_INTERVAL_SECONDS=300   # 未设置/解析失败时
```

`CPA_SYNC_DEFAULT_INTERVAL_SECONDS=300` 镜像
`internal/platform/jobs/cpa_sync.go` 的 `DefaultCPASyncInterval`
（`cmd/platform-worker/config.go` 在该变量未设置时也是用这个默认值）。
`parse_duration_seconds()`（新增的纯函数）把 Go `time.ParseDuration` 接受的
整数量值 + `ns/us/ms/s/m/h` 单位序列（如 `"300s"`、`"5m"`、`"1h30m"`）转成
整数秒；本仓库 `deploy/compose/.env.example` 与两份 compose 文件里全部
`*_INTERVAL` 配置都是这种形状，未支持 Go 允许但这里从未配过的小数量值。
解析失败（空值、非法格式）时按约定回退默认值，不阻断部署——因为
`cmd/platform-worker/config.go` 的 `configFromEnv` 对同一个变量做的解析是
启动期硬校验（失败 `os.Exit(2)`），真正非法的值会在更早的 `worker` 阶段
（容器未处于 running）就已经让部署失败，本函数的失败路径在实践中是安全网
而非主要判定。

轮询间隔固定 10s；预算耗尽时仍按 10s 步长跑完最后一轮再判定失败，不提前
掐断（不会因为预算不是 10 的整数倍而漏查一次）。

### 2. 新增日志行

- 阶段开始时打印一次预算公告：
  `cpa-observations=budget budget=<N>s interval=<N>s poll=10s expected_generation=<hash>`
- 每 10s 轮询一次，至多每 60s 打印一行等待进度：
  `cpa-observations=waiting elapsed=<N>s expected_generation=<hash>`
  （elapsed=0 的首次失败立刻打一行，此后每满 60s 再打一行，不是"过一分钟静默期后才第一次出声"）
- 命中时（不变的前缀，字段从 `attempt=N` 改成 `elapsed=Ns`，因为循环已经从
  按次数计数改成按时间计数）：
  `cpa-observations=ok generation=<hash> elapsed=<N>s`
- 预算耗尽时（`die()` 消息追加了期望/观测元组，前半句原文一字未改）：
  `DEPLOY LOCAL FAIL: cpa-observations: four same-generation successful CPA observations with run_at were not produced (expected=4|1|<hash>|4|0|1 observed=<最后一次观测到的元组或 <none>>)`
  ——`observed` 与 `expected` 的 generation 字段（第 3 段）不同即"旧
  generation 还在"；`count(*)`（第 1 段）小于 4 即"部分到齐"；`observed=<none>`
  即查询本身没拿到结果。退出码仍是既有的 `exit 1`（走 `die()`），未新增/更改
  退出码。

### 3. 实现结构：两个函数移到 main() 之外

`parse_duration_seconds()`、`cpa_observation_budget_line()`、
`cpa_observation_wait()` 三个新函数定义在 `main()` 之外（`set -Eeuo pipefail`
和 `umask 077` 之后、`main() {` 之前），这是本片除等待逻辑本身以外唯一的
结构性改动，原因和影响面都写在文件头部新增的注释里：

- **为什么要挪出去**：`cpa-observations` 阶段完整跑一遍需要
  `expected_environment=production` 且 `cpa_mode_value=file`；这条路径在
  `main()` 更早的阶段就会要求 `EUID=0`（生产 root，见 baseline 阶段的 CPA
  宿主准备）以及硬编码的宿主路径
  `/opt/xingmang/cpa-snapshot/current/cpa-snapshot`（不经任何测试可覆盖的
  变量）——两者都不是本测试文件能在便携环境下满足的前置条件，且都来自更早、
  与本片无关的 XM-CPA-SNAPSHOT 切片（见 `docs/handoffs/slices/
  XM-CPA-SNAPSHOT.md` 的 `not_run`：那个切片本来就还没有在这套 fake
  docker/psql/curl 契约测试里跑通过真实 production+file 全链路）。把这三个
  函数移到 `main()` 之外、纯函数/无副作用，测试就能直接 `source`
  本脚本后单独调用它们验证，不牵动 Docker/Git/生产 root 检查——测的是脚本里
  真实跑的那份代码，不是重新实现一遍；`main()` 内部调用它们的方式（同样的
  函数名、同样的参数，`cpa_observation_wait` 通过 bash 动态作用域看到
  `main()` 的局部变量/函数 `run_compose`/`POSTGRES_USER`/`POSTGRES_DB`）与
  测试里的调用方式完全一致。
- **对 XM-DEPLOY-SELFUPDATE0 自我更新安全性的影响**：无。那条修复的核心
  保证是"整份文件必须先被 bash 完整解析完，才会开始执行任何一条语句"，靠的
  是全部逻辑包在 `main()` 里、只在文件最后一行调用。新增的三个函数**只有
  定义，没有在文件顶层被调用**，和 `main()` 自己的函数定义一样，都要等到
  bash 读完整个文件、到达文件最后一行才会真正被调用——不影响这条保证覆盖的
  时间窗口。
- **文件末尾的调用方式也相应改了**：`main "$@"` 改成
  `if [ "${BASH_SOURCE[0]}" = "${0}" ]; then main "$@"; fi`（标准的
  "被 source 时不要自动跑 main" 惯用法）。正常执行（`bash deploy-local.sh`
  或自我更新 re-exec 用的 `exec bash "$self_path" ...`）时
  `BASH_SOURCE[0]` 与 `$0` 相等，`main "$@"` 照常执行，行为不变；测试
  `source` 本脚本时两者不相等，`main` 被跳过，只留下函数定义可用。

### 4. `--probe-attempts` 不再覆盖这个阶段

`cpa-observations` 阶段过去与 `healthz`/`readyz`/`services` 等探针共用
`probe_attempts`（可用 `--probe-attempts` 调），现在完全独立，不受这个参数
影响（其它阶段的 `probe_attempts` 行为一字未改）。阶段内新增了一行注释
说明这一点，避免以后有人传 `--probe-attempts 1` 却诧异 cpa-observations
还是等了一整个周期。

## files_changed

- `deploy/scripts/deploy-local.sh`：新增 `CPA_SYNC_DEFAULT_INTERVAL_SECONDS`
  常量与三个 `main()` 之外的函数（`parse_duration_seconds`/
  `cpa_observation_budget_line`/`cpa_observation_wait`）；重写
  `cpa-observations` 阶段用新等待循环替换原先复用 `probe_attempts` 的
  `for` 循环；文件末尾 `main "$@"` 改成 source-guard 形式；文件头部新增
  一段等待预算说明。
- `tests/deploy/deploy-local.test.sh`：新增 XM-DEPLOY-CPAWAIT0 一节（见
  tests_run），覆盖 `parse_duration_seconds` 的成功/失败用例、
  `cpa_observation_budget_line` 的默认值与随 interval 变化、
  `cpa_observation_wait` 的第三次轮询命中与预算耗尽两条路径。
- `docs/runbooks/DEPLOY-SERVER-QUICKSTART.md`：在退出码 3 说明之后新增一段
  cpa-observations 等待预算说明（沿用该文件已有的 ASCII 标点风格，未套用
  deploy-local.sh 头部注释用的全角标点）。
- `docs/handoffs/slices/XM-DEPLOY-CPAWAIT0.md`（本文件）。

无 `.go`/`package.json`/前端代码改动，Go/pnpm 门禁不适用，见 tests_not_run。

## tests_run

```
bash -n deploy/scripts/deploy-local.sh          PASS
bash tests/deploy/deploy-local.test.sh          PASS，DEPLOY-LOCAL-TEST-OK
                                                 （88 ok / 0 not ok；既有全部
                                                 用例原样通过，新增 18 条断言）
bash tests/deploy/verify-real-mode.test.sh      PASS，VERIFY-REAL-MODE-TEST-OK
                                                 （未改动逻辑，确认未被波及）
bash scripts/check-governance.sh                PASS（exit 0，无输出）
```

新增的测试（均在 `tests/deploy/deploy-local.test.sh` 末尾新增的
"XM-DEPLOY-CPAWAIT0" 一节，`source` 本脚本后直接调用，见 summary §3 的
"为什么要挪出去"）：

- `parse_duration_seconds`：5 组成功用例（`300s`→300、`5m`→300、`1h30m`→5400、
  `60s`→60、`24h`→86400）+ 4 组失败用例（空串、`abc`、`300seconds`、`0s`
  均返回非零）。
- `cpa_observation_budget_line`：默认 300s 周期给出 `budget=360s`；
  `XM_CPA_SYNC_INTERVAL` 对应的 600s 周期给出 `budget=660s`——对应团队交接
  测试项 (c)，证明预算确实随周期变化，不是写死的常数。
- `cpa_observation_wait`：
  - (a) 假 `run_compose` 前两次返回不匹配的元组、第三次返回匹配元组
    （配合被覆盖为空操作的 `sleep`）：断言返回成功、打印过
    `cpa-observations=waiting elapsed=0s ...` 进度行、命中后打印
    `cpa-observations=ok ... elapsed=20s`（用 elapsed 而不是旧版的
    attempt 计数）。
  - (b) 假 `run_compose` 恒返回另一个 generation 的元组，预算给 25s、轮询
    10s：断言返回失败、`CPA_OBSERVATION_LAST_STATE` 保留了最后一次观测到的
    （旧 generation 的）元组、耗尽前也打印过等待进度行。
  - 额外断言：`source` 本文件不会意外触发 `DEPLOY LOCAL PASS/FAIL`（确认
    source-guard 生效，测试没有失手连上真实 Docker/Git）。

实现过程中踩到并修正的两个测试坑（不影响交付逻辑，记在这里避免下次重踩）：

1. **假 `run_compose` 不能用普通 shell 变量计数跨调用次数**：
   `cpa_observation_wait` 用 `"$(run_compose ...)"` 命令替换拿返回值，命令
   替换总会 fork 子 shell，子 shell 里对变量的修改不会传回父进程——用普通
   变量当计数器会导致假函数永远认为"这是第一次调用"。改用文件当计数器
   （思路与本文件既有的 `FAKE_DOCKER_CORRUPT_SCRIPT_PATH.done` 标记文件
   手法一致）后行为正确。
2. **不能裸调用 `cpa_observation_wait`**：`source` 本脚本带进了
   `set -Eeuo pipefail`；`cpa_observation_wait` 返回 1（预算耗尽）时，
   如果是裸语句调用（不在 `if`/`&&`/`||` 里），`errexit` 会让整个子 shell
   立刻退出，后面拼 `RC=.../LAST_STATE=...` 的 `printf` 根本不会执行，看起来
   像是"整个测试卡住/无输出"。改用 `cmd && rc=0 || rc=$?` 这个 -e 安全的
   惯用法后行为正确——`main()` 里真正调用它的地方本来就是
   `if cpa_observation_wait ...; then ... else ... fi`，是安全的，问题
   只出在测试脚本裸调用这一处。

## tests_not_run

- **shellcheck**：本机环境未安装（`command -v shellcheck` 无结果），与
  XM-DEPLOY-SELFUPDATE0 那次一致，按团队交接"if available"的要求跳过。
- **Go/pnpm 相关门禁**：本片未改动任何 `.go`/`package.json`/前端代码，不
  适用，未运行。
- **真实服务器/staging 栈上的验证**：没有服务器访问权限，也没有安装真实
  `cli-proxy-api`/`cpa-manager-plus` 的环境，未在真实
  `/srv/deploy/xingmang-platform` checkout 上跑过完整的
  production+file 模式部署来验证新的等待预算确实能等到观测落库。全部证据
  来自本地 source-only 单元测试（见 tests_run 与 risks 第 1 条）。
- **main() 内 `die()` 消息的字符串拼接本身**：受 risks 第 1 条同一个前置
  条件限制，未能端到端跑到 `die "$phase: ... observed=..."` 那一行本身；
  `expected`/`observed` 两个子串各自的来源（`cpa_observation_expected_state`
  的拼法、`CPA_OBSERVATION_LAST_STATE` 的产出）分别被测试直接覆盖了。

## risks

1. **无法在这套便携测试里端到端跑通整个 cpa-observations 阶段**：这条路径
   在 `main()` 更早的地方就要求 `EUID=0` 与硬编码的
   `/opt/xingmang/cpa-snapshot/current/cpa-snapshot`（均来自更早的
   XM-CPA-SNAPSHOT 切片，那个切片自己的 Handoff 也承认"未验证...真实文件
   核对"、"完整门禁...生产 installer/systemd/Compose/CPA 页面真实验证尚未
   执行"）。本片选择把新增的等待逻辑抽成 `main()` 之外的纯函数直接单测
   （见 summary §3），而不是放宽/绕过那两个与本片无关的前置检查去凑一条
   端到端测试——放宽 root 检查或给硬编码路径加测试专用覆盖变量都会改动
   XM-CPA-SNAPSHOT 切片的代码，超出团队交接给的范围。如果验收线希望有
   真正端到端的覆盖，需要先在 XM-CPA-SNAPSHOT 那条线上把这两处前置检查
   做成可控（例如给 root 检查、`cpa-snapshot` 二进制路径都加测试模式下的
   覆盖变量，同 `docker_bin`/`curl_bin` 的既定模式），再回头补一条真正的
   production+file 全链路测试；本片的函数级测试仍然是对真实代码的验证，
   不是重新实现一遍，只是覆盖不到"三个新函数之间怎么用真实变量互相衔接"
   这最后一层胶水（该胶水代码只有 10 行左右，见 `cpa-observations` 阶段
   现在的样子）。
2. **`ByPeriod` 去重窗口目前是硬编码常量，不随 `XM_CPA_SYNC_INTERVAL` 变化**
   （顺带发现，不在本片范围内，供参考）：`internal/platform/jobs/
   cpa_sync.go` 的 `CPASyncArgs.InsertOpts()` 里
   `UniqueOpts.ByPeriod: DefaultCPASyncInterval`
   用的是编译期常量（300s），不是 `cfg.CPASyncInterval`。如果运营人员把
   `XM_CPA_SYNC_INTERVAL` 配得比 300s 短很多，River 的去重窗口可能会在
   两次预期内的调度之间意外拦掉一次插入；配得比 300s 长很多则相反，
   300s 去重窗口在两次真实调度之间只覆盖一部分时间，基本不起去重作用。
   这是 CPA 周期任务本身的调度细节，不影响本片"等待预算按配置的周期算"
   这条逻辑的正确性（预算公式用的是 `XM_CPA_SYNC_INTERVAL`，不是
   `ByPeriod` 常量），只是一个可能值得 XM-CPA0/XM-CPA-SNAPSHOT 那条线
   后续核实的独立观察。
3. **等待预算的默认值（360s）意味着 production file 模式下部署失败前最多
   多等 6 分钟**：这是本片故意的取舍（宁可部署变慢也不要误报失败），
   但如果 cpa_sync 真的坏了（比如凭据/挂载配错），失败信号会比过去晚
   6 分钟左右才出现。新增的 `cpa-observations=budget`/`=waiting` 日志行
   让运维在预算耗尽前就能看到"卡在等第几秒"，缓解了这一点，但没有消除它。

## follow_ups

- 如果验收线希望本片有真正端到端的服务器验证，等下一次真实
  production+file 模式部署时，人工核对：`cpa-observations=budget` 行里的
  `interval=` 是否与该环境 `.env` 的 `XM_CPA_SYNC_INTERVAL`（或未设置时的
  300s 默认）一致；正常情况下观测应当在预算内、通常远早于预算耗尽前
  出现 `cpa-observations=ok ... elapsed=Ns`。
- risks 第 1 条：如果需要真正端到端覆盖 `cpa-observations` 阶段（而不是
  本片这样的函数级单测），需要先在 XM-CPA-SNAPSHOT 那条线上给
  `EUID=0` 检查和 `/opt/xingmang/cpa-snapshot/current/cpa-snapshot` 路径
  补测试模式覆盖变量。
- risks 第 2 条：`ByPeriod` 硬编码 300s、不随 `XM_CPA_SYNC_INTERVAL` 变化
  这一点，建议 XM-CPA0/XM-CPA-SNAPSHOT 那条线核实是否需要改成
  `cfg.CPASyncInterval`。
