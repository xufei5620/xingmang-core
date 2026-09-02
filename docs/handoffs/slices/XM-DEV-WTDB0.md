# XM-DEV-WTDB0：每个 worktree 独立测试库

## status

READY（待验收线审读合入 `release/v0.1-launch`）。纯开发者工具链新增（脚本 +
文档），无生产代码改动，无迁移/契约/前端改动，未触碰 CI workflow。

## branch

`ai/claude/XM-DEV-WTDB0`（base `release/v0.1-launch` @ `b65d224972e8c609deb47aef9abef44215b07642`），
worktree `K:/星芒统一控制平台/acceptance/wt-wtdb`。

## commits

- `c70fcdbee6085323ebeff241a4b3f1af1b235e47` — feat(dev): per-worktree test database helper
- `4fae3e9c4648837bdf4137305d9799a422e598c4` — docs(dev): document per-worktree test database workflow

## summary

多个 worktree/agent 长期共用同一个 `xm_test` 库。2026-09-02 门禁脚本第一次
导出 `XM_TEST_DATABASE_URL`（此前一直未导出，所有 DB 集成测试从未真正跑过，
见 `XM-AUTH-TOTP0.md`），随即在 2026-09-03 的 `XM-DBTEST-FIX0.md` 里暴露出
`internal/platform/credentials` 与 `internal/platform/jobs` 两个包共用同一个
`xm_test` 库时的真实冲突（`credentials` 包写 `core.connector_config` 的
`(sub2api|newapi, staging)` 两行且不清理，`jobs` 包同库测试撞唯一键）。本片
让每个 worktree 拿到自己独立的 `xm_test_<worktree 目录名>` 库，从根上消除
这类跑测顺序/并发敏感的耦合。

**`scripts/dev/worktree-testdb.sh`**（POSIX bash，Git Bash/Linux 通用）：
- 纯函数 `sanitize_name`/`derive_db_name`/`build_db_url` 与参数解析
  `parse_args` 不碰任何外部状态，文件末尾用 `[ "${BASH_SOURCE[0]:-$0}" =
  "$0" ]` 判断避免 `source` 时执行 `main`——测试据此在无数据库场景下直接
  调用这些函数。
- 默认动作：数据库不存在则 `CREATE DATABASE`（幂等，已存在则跳过），
  接着总是跑 `go run ./cmd/migrate -database <url> -path db/migrations up`
  （幂等，golang-migrate 对已应用迁移是 no-op），打印
  `export XM_TEST_DATABASE_URL=...`。`--print-url` 只打印 URL 本身。
  `--drop` 终止该库的活动连接后 `DROP DATABASE IF EXISTS`。`--list` 列出
  所有 `xm_test_*` 库及大小。`--pg-url` 覆盖管理连接。
- stdout/stderr 有严格分工：进度信息一律 `stderr`，默认动作/`--print-url`
  的 stdout 只有那一行连接串，保证 `export X=$(... --print-url)` 之类的
  捕获不会被进度信息污染（已用 `2>/dev/null` 单独验证）。
- 管理连接默认走 `docker exec invoice-test-pg psql -U postgres`（与团队
  既有手工配方逐字一致）；容器不在时回落本地 PATH 上的 `psql`。

**`scripts/dev/worktree-testdb.ps1`**：转发到同一份 bash 脚本的瘦包装，不
重新实现任何逻辑。

**`scripts/dev/test-worktree-testdb.sh`**：无库单元测试（source 后在独立
子进程里逐条调用函数，规避被测脚本自身 `set -Eeuo pipefail` 泄漏进测试
控制流）覆盖纯函数与 `parse_args`，另有 `XM_DEV_WTDB_E2E=1` 门控的真库
往返测试（建库→迁移→列库→删库→幂等重复），针对本机 `invoice-test-pg`。

### 与派工消息的偏离（先说清楚，再看细节）

以下是实现过程中做的、派工消息没有逐字规定的判断，不只是事后挑几条：

1. **超长名字截断加消歧哈希**：派工消息只说"最长 63 字符"。只截断不消歧
   会让两个不同的长 worktree 目录名截断后悄悄撞成同一个库名——这正好
   违背"每个 worktree 独立测试库"这件事本身，所以截断时额外拼一段
   8 位十六进制哈希（`derive_db_name`，见测试
   "两个不同的超长名字截断后不撞名"）。63 字符本身在当前 worktree 命名
   习惯（`wt-xxx`）下几乎不会触发，只是防御性处理。
2. **`--pg-url` 与 docker/本地 psql 的选择逻辑**：派工消息说"容器在场用
   docker exec，否则用 PATH 上的 psql"，但没规定 `--pg-url` 覆盖时是否
   仍然固定连 `invoice-test-pg` 容器。我的判断：显式覆盖管理连接就是在
   说"目标不是这个容器"，所以只要 `--pg-url` 非空就直接用本地 `psql`
   连那个连接串，不再尝试 `docker exec` 进固定容器名。
3. **防御性前缀断言**：`main()` 里在触达任何 CREATE/DROP 之前，断言派生
   的库名一定以 `xm_test_` 开头，不满足就内部报错退出——不是派工消息
   要求的，但对一个"专门隔离测试库"的工具来说这个不变量值得直接在代码里
   钉住。
4. **`go run` 前主动 unset 八个代理相关环境变量**：本机此前多次记录过
   `HTTP_PROXY`/`ALL_PROXY` 等转发代理会干扰 Go 拉取模块（不影响本次
   迁移要连接的真实 Postgres TCP 连接本身）。迁移命令通常不需要外部
   网络（模块已缓存），这个 unset 纯防御性、零副作用（只影响 `go run`
   这一个子进程），未见文档要求，但成本为零。
5. **`--list` 用未转义的 `LIKE 'xm_test_%'`**：技术上 `_` 在 SQL LIKE 里
   是单字符通配符，没有额外转义。这是个只读便利命令而非安全边界，判断
   不值得为此引入 ESCAPE 子句的复杂度。
6. **PowerShell 侧不翻译 `export` 语句语法**：默认动作打印的
   `export XM_TEST_DATABASE_URL=...` 是 POSIX 语法，`.ps1` 包装器原样
   透传这行文本，不生成对应的 `$env:` 语句——PowerShell 用户请用
   `-PrintUrl` 拿到纯 URL 后自己赋值给 `$env:`（已在脚本头注释和
   runbook 里写清楚用法）。这是"瘦包装"选择的直接后果，派工消息本身
   允许这个选择（"或更简单的原生实现"），但值得记录我确实没有做双向
   语法转换。
7. **修了一个实测踩到的真 bug**：`.ps1` 包装器最初直接信 PATH 上的
   `bash.exe`；本机（以及很可能不少装了 WSL 的 Windows 机器）PATH 上
   `bash.exe` 排在最前的其实是 `C:\Windows\System32\bash.exe`（WSL
   启动器），会把带中文的 Windows 路径翻译错，报一个很误导人的
   "No such file or directory" 且夹杂一段不相关的 WSL localhost 转发
   提示。已改成从 `git.exe` 的位置反推 Git for Windows 安装根目录
   （`<root>\cmd\git.exe` 与 `<root>\bin\bash.exe` 同级的标准布局），
   再补注册表 `HKLM:\SOFTWARE\GitForWindows` 与标准安装路径兜底，绝不
   回落到裸 `bash.exe` PATH 查找。
8. **文档示例统一加 `bash` 前缀**：发现仓库 `core.fileMode=false`（已用
   `git ls-files -s` 核对，连既有的 `check-governance.sh`/
   `verify-real-mode.sh` 也是 `100644`，可执行位从不随 Git 分发），于是
   把 runbook 和 CLAUDE.md 里的调用示例都改成 `bash scripts/dev/
   worktree-testdb.sh` 显式前缀形式，和 CLAUDE.md 原有的
   `bash scripts/check-governance.sh` 写法保持一致，而不是假设可以
   `./scripts/dev/worktree-testdb.sh` 直接执行。

## files_changed

- `scripts/dev/worktree-testdb.sh`（新增）
- `scripts/dev/worktree-testdb.ps1`（新增）
- `scripts/dev/test-worktree-testdb.sh`（新增）
- `docs/runbooks/GIT-WORKFLOW.md`（新增第 6 节"每个 worktree 独立测试库"）
- `CLAUDE.md`（常用命令加一行指针）

未改动任何 `.go` 文件、`db/migrations/`、`contracts/`、`.github/workflows/`。

## tests_run

全部在 `K:/星芒统一控制平台/acceptance/wt-wtdb` 下执行：

```
bash -n scripts/dev/worktree-testdb.sh                        # 语法检查，通过
bash -n scripts/dev/test-worktree-testdb.sh                   # 语法检查，通过
bash scripts/dev/test-worktree-testdb.sh                      # 单元测试（无库）
XM_DEV_WTDB_E2E=1 bash scripts/dev/test-worktree-testdb.sh     # + 真库往返
bash scripts/check-governance.sh
/c/Users/58439/.local/bin/gitleaks git --no-banner --log-opts="release/v0.1-launch..HEAD" .
```

结果：

- 无库单元测试：36 项断言全部 `ok`，以 `WORKTREE-TESTDB-UNIT-TEST-OK` 收尾，
  exit 0。覆盖 `sanitize_name`（短横线/大写/多个非法字符合并/去首尾下划线/
  空串与纯非 ASCII 兜底哈希及其确定性）、`derive_db_name`（basename 提取、
  超长截断到精确 63 字符、两个不同超长名不撞名）、`build_db_url`（保留/
  不保留 query、`postgresql://` scheme、拒绝非法 scheme）、`parse_args`
  （默认值、各 flag、`--drop`/`--list`/`--print-url` 三者两两互斥校验、
  未知参数、`--pg-url` 缺值与非法 scheme）、`--help`/`-h` 无需数据库即可
  退出 0、以及 `--pg-url` 指向不可达目标时清楚失败（本机没有本地 `psql`，
  实际验证的是"找不到可执行文件"这条报错分支）。
- 真库往返测试（`XM_DEV_WTDB_E2E=1`，针对本机 `invoice-test-pg`）：13 项
  断言全部 `ok`，以 `WORKTREE-TESTDB-E2E-TEST-OK` 收尾，exit 0。实测
  `--print-url` 建库（`xm_test_wt_wtdb`）+ 灌迁移、默认动作打印 export
  语句、幂等重复 ensure、`--list` 能看到该库、`--drop` 删除、删除后
  `--list` 不再出现、对已删除的库重复 `--drop` 仍成功。全程手工额外验证
  过 stdout 只含连接串一行（`2>/dev/null` 单独确认）、幂等 re-run 走
  "已存在，跳过创建"分支、`--list` 正确排除旧的共享 `xm_test`（不匹配
  `xm_test_%` 模式）。
- `bash scripts/check-governance.sh`：通过（exit 0，无输出）。
- 手工额外验证 `scripts/dev/worktree-testdb.ps1`（PowerShell 工具，非
  仓库自动化测试的一部分）：`-h`、`-PrintUrl`（建库+迁移，stdout 只含
  URL）、默认动作（export 语句）、`-ListDatabases`、`-Drop`、以及
  `-Drop -ListDatabases` 同时传入时按预期 fail-fast，行为与 bash 版本
  逐项比对一致。修复过程见"与派工消息的偏离"第 7 条。
- `gitleaks git --no-banner --log-opts="release/v0.1-launch..HEAD" .`：
  `2 commits scanned`，`no leaks found`，exit 0。

## not_run

- **shellcheck**：本机 PATH 上未安装（`command -v shellcheck` 无输出），
  未能运行。两个新脚本手工过了一遍常见 shellcheck 类问题（变量全部
  加引号、`[ ]` 而非 `[[ ]]`（除 `test-worktree-testdb.sh` 里
  `assert_match` 用 `[[ =~ ]]` 做正则匹配这一处）、函数内变量用 `local`、
  命令替换用 `$()` 不用反引号），但没有工具化验证，请验收线在有
  shellcheck 的环境复跑一次
  `shellcheck scripts/dev/worktree-testdb.sh scripts/dev/worktree-testdb.ps1
  scripts/dev/test-worktree-testdb.sh`（`.ps1` 会被跳过，shellcheck 不
  处理 PowerShell，仅列出以防有人直接丢一批文件进去）。
- **`.ps1` 包装器没有自动化测试**：派工消息第 3 条只要求 bash 测试脚本；
  `.ps1` 的验证是我手工用 PowerShell 工具跑的（见 tests_run），没有写进
  仓库可重复运行的测试文件。如果需要，后续可以加一个只在 Windows 且有
  Git Bash 时才跑的 Pester 测试或额外的 bash 测试分支。
- **未在 Linux 原生环境验证**：本机是 Windows + Git Bash；脚本设计上只用
  POSIX/GNU coreutils 常见语法（`sed -E`、`sha256sum`、`basename --`），
  但没有实际在一台 Linux 机器上跑过。

## risks

- 默认管理连接的明文口令 `postgres:test@127.0.0.1:55432` 是本机既有的
  local-only 测试夹具凭据，与仓库里已合入的多份 handoff（如
  `XM-ASSURE1-core.md`、`XM-USERS-V2-REAL.md`）里的写法完全一致，
  `gitleaks` 已确认不误报。
- `--pg-url` 覆盖时如果本地没有 `psql`（本机就是这种情况），只能报"找不到
  可执行文件"，不会尝试用 docker 兜底——这是"覆盖即表示目标未必是那个
  容器"的判断的直接后果（偏离第 2 条），如果验收线希望覆盖后仍可选择性
  回落 docker exec，需要另加一个显式 flag 区分"换连接参数"与"换执行
  方式"，当前实现没有做这个区分。
- 脚本假设 `git rev-parse --show-toplevel` 能正确解析当前 worktree
  （在非 Git 目录下运行会明确报错退出，不会静默用当前目录名代替）。

## follow_ups

- 如果后续想要 CI（而非仅本地/交接单场景）也用这套按 worktree 隔离的库，
  需要额外设计一个"CI 专属命名"（当前 `find_worktree_root` 依赖真实的
  Git worktree 目录名，CI 的 checkout 目录命名习惯可能不同），本片未
  处理，仅覆盖本地多 worktree 并行开发这一个场景。
- 建议有 shellcheck 的环境复跑一次（见 not_run）。
- 如果要让 `.ps1` 包装器有仓库内可重复运行的自动化测试，可以考虑加一个
  `XM_DEV_WTDB_E2E`同款门控的 Pester 测试，跟现有的
  `scripts/test-database-roles.test.ps1` 放在一起的风格对齐。

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01XtTQUB1k16Vw2K49WQJhpi
