# XM-DEPLOY-SELFUPDATE0 · deploy-local.sh 自我更新安全性

## status

READY（待验收线审读、复跑并人工合入）

## branch / commit / base

- branch: `ai/claude/XM-DEPLOY-SELFUPDATE0`
- base: `release/v0.1-launch@4db114b`
- worktree: `K:/星芒统一控制平台/wt-xmDEPLOYSU0`
- commits（3，从旧到新）：
  - `4ef8a34903e1e61c3ba4fd252293e15e3c20b787` fix(deploy): make deploy-local.sh immune to self-triggered mid-run rewrites
  - `9843286989e351cd5df4a3cdbf575c4b7b2c0c55` test(deploy): cover self-update safety for deploy-local.sh
  - `6590820b9ffc38f7007a7603c09f7c5f31689dc4` docs(deploy): document deploy-local.sh self-update safety and exit code 3

## 开工前状态确认

按团队交接要求「先确认现状」：`deploy-local.sh` **确实会自我更新**——`grep -n
'fetch\|checkout\|merge' deploy/scripts/deploy-local.sh` 命中的 fetch/checkout/
merge --ff-only 段落作用在 `--repo`（默认是脚本自己所在的 checkout）上，而
`deploy/scripts/deploy-local.sh` 本身就是该 checkout 里被这几条命令改写的文件之一。
`docs/runbooks/DEPLOY-SERVER-QUICKSTART.md`/`GO-LIVE-CHECKLIST.md`/
`REQLOG-RECORDER.md` 记录的真实生产调用方式都是
`cd /srv/deploy/xingmang-platform && ... deploy/scripts/deploy-local.sh`（不带
`--repo`），确认 `--repo` 默认值与脚本自身所在目录就是同一份 checkout——这正是
团队交接里「if the script performs any git update of its own checkout」这一支，
按交接要求走的是 **re-exec 方案**，不是「不自我更新时才用」的 preflight-fail 方案。

## summary

### 1. main() 包裹（parse-before-execute）

整份脚本主体（原第 19～768 行）被包进 `main() { ... }`，只在文件最后一行以
`main "$@"` 调用。bash 对函数定义必须读到匹配的右花括号才能完成解析，因此在
`main "$@"` 真正开始执行任何一条语句（含脚本自己触发的 git 操作）之前，整份文件
已经被完整解析进内存；运行期间磁盘文件被改写（不管是这次 self-update 自己触发的，
还是操作员在另一个终端并发 `git pull`，或第三方 CI）都不会让**当前这次已经在跑**的
进程读到新旧混杂的字节。这是纯重构：不改变任何既有选项、退出码、日志行或烟测判断，
用回归测试 (a) 验证（见下）。

只有 `set -Eeuo pipefail`/`umask 077` 两行留在 `main()` 外面（触发不了任何文件改写，
挪不挪进去对「自我触发改写」这条主要威胁没有实质影响；对「完全独立的并发进程改写」
这一残余风险——见 risks——挪不挪同样没有实质差别，因为决定性的时间开销始终是解析
`main()` 那几百行本体）。

### 2. self-update 成功后 re-exec 一次

`target_sha`（fetch/checkout/merge --ff-only 全部完成后的最终 HEAD）算出来之后，
新增判断：

- 只有真的执行了 git 写操作（非 `skip_git`/`--test-mode` 显式跳过）**且**
  `target_sha` 相对本次运行开始时的 SHA 确实前进了，才需要考虑 re-exec；
- 只有「被更新的 `--repo` checkout」与「本脚本自己所在的文件路径」是同一份
  （`$repo_path/deploy/scripts/deploy-local.sh` 与 `${BASH_SOURCE[0]}` 解析后
  路径相等）时 re-exec 才有意义——`--repo` 显式指向别的 checkout 时，本进程当前
  执行的字节从未被这次 git 操作动过，继续用已解析在内存里的逻辑即可，输出
  `self-update=skipped reason=repo-not-self`；
- 满足以上条件时，输出 `self-update=applied from=<旧 sha> to=<新 sha>
  action=reexec`，用 `XM_DEPLOY_LOCAL_REEXEC=1`（仅内部使用，不供调用方设置）
  防止再次触发，`exec` 同样参数的、磁盘上刚更新完的脚本自身——确保**真正部署的是
  新版本脚本**，而不是旧版本已经解析在内存里的逻辑。

实现上有一个容易漏掉的坑：脚本顶部为了安全会 `unset` 掉调用者环境里所有
`XM_*`/`DOCKER_*`/... 前缀变量（防止遗留变量偷偷重定向正式部署），这意味着走到
re-exec 那一行时，`XM_DEPLOY_LOCAL_TEST_MODE`/`XM_DEPLOY_LOCAL_DOCKER_BIN` 等契约
测试用到的覆盖变量早已从当前进程环境里被清空——如果只是单纯 `exec`，子进程会以为
自己没收到任何覆盖（回归测试 (c) 第一次跑时就是这样炸的：`--test-mode 需要
XM_DEPLOY_LOCAL_TEST_MODE=1`）。修法是把这几个值在 `unset` 之前就已经读进的
`test_gate_value`/`docker_override_value`/... 局部变量，在 `exec` 前重新显式导出
给子进程；生产场景这些值本就是空/默认，显式传递不改变任何正式部署行为。

### 3. 退出码 3：checkout 无法自动 fast-forward（真正分叉）

两处 `git merge --ff-only FETCH_HEAD` 失败的分支（「指定 sha 无法快进」「fetch 后
的 SHA 无法快进」）从共享 `exit 1` 的通用 `die()` 换成新的 `die_checkout_behind()`
（`exit "$EXIT_CHECKOUT_BEHIND"`，值为 `3`），并把消息改成点名的手动修复指令
（`git fetch origin release/v0.1-launch && git merge --ff-only FETCH_HEAD`）。

这只在 fetch **已经成功**、但本地有 upstream 没有的提交（真正分叉，ff-only 因此
拒绝）时触发；单纯的「fetch 失败、网络/镜像不可达」仍然走既有的「有精确匹配 SHA 才
放行，否则 fail-closed」路径（`exit 1`，消息与判断条件完全未改），因为「无法确认
upstream 状态」和「已经确认落后/分叉」是两件不同的事——前者是团队交接里「fetch
failure only warns / never fail deploy just because fetch failed」的既有行为，不
在本片改动范围，后者才是团队交接里「exits non-zero with a distinct exit code when
the checkout is behind」对应的场景。

**关于 `--allow-behind`**：团队交接把它写在「脚本不自我更新，改用 preflight-fail」
那一支里，语义是「preflight 默认拒绝部署落后的 checkout，这个 flag 用来放行」。
`deploy-local.sh` 走的是 re-exec 那一支——它从不会因为「落后」本身而拒绝部署，只要
能 fast-forward 就自动做掉；只有「fast-forward 不了」（真正分叉，一种质变而不是
「落后多少」的量变）才会停。这种情况下部署一个分叉、状态不确定的 checkout 本身就是
不安全的，没有一个「我知道有风险，仍然继续」的合理放行场景可以对应
`--allow-behind`——本片没有实现这个 flag，选择诚实地不做，而不是为了凑齐团队交接
罗列的选项而伪造一个没有真实语义的开关。如果验收线判断这个判断不对，实现上是纯增量
（加一个 `--allow-behind` 分支，让它把 `die_checkout_behind` 降级为一条警告后继续
部署当前 HEAD）。

## files_changed

- `deploy/scripts/deploy-local.sh`：`main()` 包裹、`self_path`/
  `EXIT_CHECKOUT_BEHIND` 计算、`die_checkout_behind()`、self-update re-exec 判断、
  两处 `die` 换成 `die_checkout_behind`、文件头新增自我更新安全性与退出码 3 说明。
- `tests/deploy/deploy-local.test.sh`：fake docker 新增仅供测试 (a) 使用的
  「build 时改写目标脚本文件」钩子（受 `FAKE_DOCKER_CORRUPT_SCRIPT_PATH` 门控，
  默认不影响任何既有用例）；文件末尾新增三组回归测试（见 tests_run）。
- `docs/runbooks/DEPLOY-SERVER-QUICKSTART.md`：新增自我更新安全性与退出码 3 说明
  段落。

## tests_run

```
bash tests/deploy/deploy-local.test.sh     — PASS，DEPLOY-LOCAL-TEST-OK
                                              （70 ok / 0 not ok；既有全部用例
                                              原样通过，含新增 3 组共 15 条断言）
bash tests/deploy/verify-real-mode.test.sh — PASS，VERIFY-REAL-MODE-TEST-OK
                                              （未改动逻辑，确认未被波及）
bash scripts/check-governance.sh           — PASS（exit 0，无输出）
bash -n deploy/scripts/deploy-local.sh     — PASS
bash -n tests/deploy/deploy-local.test.sh  — PASS
```

新增的三组回归测试（均在 `tests/deploy/deploy-local.test.sh` 末尾，复用既有的
fake git/docker/curl 契约测试风格）：

- **(a) 运行中途脚本文件被改写**：把脚本复制一份单独执行，让 fake docker 在
  `build` 这一步用明显损坏（缺 `fi` 的 `if`）的内容覆盖这份复制品；断言本次运行
  仍然完成、仍然走到 `up`/探针/烟测、仍然输出 `DEPLOY LOCAL PASS`；额外用
  `bash -n` 反证被改写后的文件本身确实是语法损坏的（证明测试不是因为"意外没改坏"
  而通过）。
- **(b) 真正分叉（不是单纯落后）时退出码 3**：复用既有 `$fixture`（此刻已领先
  `$remote` 一个未推送的本地提交）、让 `$remote` 独立再推进一个不同的提交，制造
  双向分叉；断言退出码精确等于 `3`、stderr 同时含 `DEPLOY LOCAL FAIL` 前缀与
  `git merge --ff-only` 修复指令、且未继续到 `build`/探针。
- **(c) self-update 成功后 re-exec**：构造一个自带 `deploy/scripts/deploy-local.sh`
  的独立 checkout，让远端领先一个「修改 deploy-local.sh 自身、插入一行可观测标记」
  的提交；不带 `--sha` 直接跑本地这份脚本（`--repo` 默认值与脚本自身路径重合）；
  断言输出含 `self-update=applied`、新版本脚本注入的标记行真的被打印出来、部署仍以
  `DEPLOY LOCAL PASS` 收尾、且 `build` 只在 trace 里出现一次（re-exec 发生在任何
  docker/curl 调用之前，不会导致部署步骤重复执行）、磁盘上的脚本文件确实已快进到
  新版本。

## tests_not_run

- **shellcheck**：本机环境未安装（`command -v shellcheck` 无结果），按团队交接
  "if available" 的要求跳过，未执行。`bash -n` 与全部契约测试已覆盖语法与行为。
- **Go/pnpm 相关门禁**：本片未改动任何 `.go`/`package.json`/前端代码，团队交接
  已注明这些门禁不适用，未运行。
- **真实服务器/staging 栈上的验证**：没有服务器访问权限，未在真实
  `/srv/deploy/xingmang-platform` checkout 上重演过"运行中改写脚本文件"这个生产
  场景；全部证据来自本地 fake git/docker/curl 契约测试。
- **`--repo` 指向"包含旧版本 deploy-local.sh 但路径解析后不等于当前脚本路径"的
  边界情形**（例如符号链接指向的另一份拷贝）：`self_in_repo`/`self_path`
  比较依赖两者都经过同一套 `cd -- ... && pwd -P` 规整（脚本里 `repo_path`/
  `script_dir` 已有的既定处理，未额外验证符号链接场景，理论上应当一致，但未专门
  构造用例）。

## risks

1. **完全独立的并发进程改写仍是残余风险**：`main()` 包裹能确定性地防住"脚本
   自己触发的 git 操作改写自己"（本片修复的三次生产故障的根因），因为改写只能发生
   在整份文件解析完成之后。但如果一个**完全独立**的进程（例如操作员在另一个终端
   手动 `git pull`，团队交接原话提到的"concurrent fetch"）恰好在 bash 还没读完
   整个文件时就抢先改写磁盘，这属于操作系统级别的读/写竞态，`main()` 包裹这类
   纯脚本内技巧无法完全消除，只能靠"读文件是一次性、发生在极早期"来缩小窗口。
   本片没有引入文件锁或原子发布机制去彻底解决这个更宽的问题，团队交接的描述本身
   也把它列为"促成因素之一"而不是主因。
2. **`die_checkout_behind` 只覆盖 ff-only 失败，不覆盖"fetch 失败且无精确 SHA
   兜底"**：后者仍是既有的 `exit 1` 路径，消息与判断条件完全未改动——这是刻意
   保留既有行为（团队交接："never fail the deploy just because fetch failed"
   针对的是网络问题，不是"确认分叉"），但意味着退出码 3 目前只对应"分叉"这一种
   根因；如果验收线希望"fetch 失败且没有精确 SHA 时"也用一个独立退出码而不是通用
   `exit 1`，需要另外评估（这会改变既有的、本片承诺"byte-for-byte identical"的
   行为，故未在本片顺带做）。
3. **`--allow-behind` 未实现**：见 summary 第 3 节的推理；如果这个判断不对，
   实现是纯增量。
4. **re-exec 的判定用字符串相等比较路径，不用 `readlink -f`/`realpath`**：与
   脚本里 `repo_path`/`git_root` 早已使用的既定手法一致（`cd -- ... && pwd -P`），
   在 Windows Git Bash 与 Linux 生产服务器上都已被现有测试验证过等价路径能正确
   相等；但如果 `--repo` 目标目录本身是符号链接链条中的一环导致两条路径规整后
   仍不同，会被判定为"不是自己"，跳过 re-exec 而不是报错——这是刻意选择的安全
   方向（宁可少 re-exec、退回到"继续用已解析逻辑跑完"，也不去动一个不确定是不是
   自己的文件）。

## follow_ups

- 如果验收线认为 `--allow-behind` 或"fetch 失败时的独立退出码"确实需要，按 risks
  第 2/3 条给出的方向补，两处都是纯增量、不影响本片已交付的行为。
- 找机会在真实服务器 `/srv/deploy/xingmang-platform` checkout 上，用一次真实的
  "运行 `deploy-local.sh` 期间另开终端 `git pull`"复现并确认修复生效（本片受限于
  没有服务器访问权限，只做了本地契约测试层面的等价验证）。
- 待 shellcheck 在验收线可用的环境里跑一遍 `deploy/scripts/deploy-local.sh`，
  本片未能在本机验证 shellcheck 结果。
