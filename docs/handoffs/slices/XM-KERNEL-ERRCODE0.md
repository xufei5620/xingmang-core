# XM-KERNEL-ERRCODE0：Kernel.Execute 保留 Handler 的 Action 错误码

## status

READY（待验收线审读、复跑门禁并人工合入；本片不自动合并、不部署）

## branch

`ai/claude/XM-KERNEL-ERRCODE0`（worktree `K:/星芒统一控制平台/acceptance/
wt-kernel-errcode0`，base `release/v0.1-launch@42514cf`）

## commit

`e6ddbfe` — fix(action): preserve handler error codes through Kernel.Execute

## summary

验收线发现的真实故障：`Kernel.Execute`（`internal/platform/action/kernel.go`）
在 Handler 返回错误时，**无条件**把它改写成 `CodeExecutionFailed` 与固定文案
「action \<id\> 执行失败」——不管 Handler 自己有没有已经用 `action.NewError`
做过精确的域错误映射。

`assurance`（渠道目录校验失败 → `CodeInvalidParams`，文案「渠道目录里查无此
渠道 "xxx"」）、`alerts`、`credentials`、`finance` 等包的 Handler 普遍遵循同一
模式：内部有一个 `domainError(err)` 之类的函数，把仓储/校验错误映射成设计过
的 `*action.Error`（Code + 安全 Message）。但 `httpapi.safeMessage` 用
`errors.As(err, &ae)` 只认**最外层**的 `*action.Error`——Kernel 把 Handler 的
错误重新包了一层 `CodeExecutionFailed`，Handler 精心设计的 Code/Message 从此
不可达。后果：

- 真实 HTTP 调用方看到 502 `EXECUTION_FAILED` + 通用文案，而不是设计好的
  400 `INVALID_PARAMS`（或 409 `CONFLICT` 等）+ 具体原因；
- `ActionRun.ErrorCode` 与审计事件的 `ErrorCode` 也一并被污染成
  `CodeExecutionFailed`，事后复盘查错误分布时失真。

修法：`Kernel.Execute` 在 Handler 失败时，先用 `errors.As` 沿 Handler 返回的
错误链查找 `*Error`；查到就原样保留它的 `Code` 与 `Message`（`ActionRun`、
审计事件、对外返回错误三处一致），只是仍然 `newError(code, message, err)`
包住原始 `err`，保证 `errors.Is`/`Unwrap` 链条不断（内部日志仍能追到根因）。
查不到（裸的驱动/底层错误，如 `TestKernelRecordsHandlerFailureWithoutLeaking`
覆盖的场景）则完全维持原行为：`CodeExecutionFailed` + 固定文案，`Error()`
永不包含 cause 细节（规格 §18.4，`errors.go` 里 `(*Error).Error()` 本来就
不触碰 `cause`，这条属性天然保留，不需要额外处理）。

未改动任何前置校验路径（NotRegistered / 无身份 / PermissionDenied /
Schema 校验的 InvalidParams 等）——这些走的是 `fail()` 闭包，与本次改的
Handler 失败分支是两条独立代码路径。

## files_changed

- `internal/platform/action/kernel.go`（`Execute` 的 Handler 失败分支 +
  Go doc 注释；import 增加 `errors`）
- `internal/platform/action/kernel_test.go`（新增
  `TestKernelPreservesHandlerActionErrorCode`、
  `TestKernelPreservesHandlerActionErrorCodeIsGeneric`）
- `docs/handoffs/slices/XM-KERNEL-ERRCODE0.md`（本文件）

## decisions

- `Ruling: 用 errors.As(err, &ae) 判定「Handler 是否已做域错误映射」，命中就
  用 newError(ae.Code, ae.Message, err) 重建对外错误，而不是直接 return err`
  —— 直接返回 Handler 的 `*Error` 本身也能满足验收标准（`errors.Is` 到根因、
  `Error()` 不泄漏细节），但 `newError(...)` 包一层能保证「Kernel 对外返回的
  永远是 Kernel 自己 new 出来的 `*Error`」这条不变式不因 Handler 失败/成功
  分支而不同，读代码时更容易确认「返回值就是 Code/Message 对得上的那一个」；
  代价是多一次分配，可忽略不计。
- `Ruling: 不修改 docs/modules/{action,httpapi}/README.md，改在 Kernel.Execute
  上加 Go doc 注释` —— 按任务指示的顺序，先查了 `docs/` 与 `contracts/` 里
  `EXECUTION_FAILED`/「执行失败」的命中（11+1 个文件），逐一确认它们要么是
  某个具体 Action 自己的错误码清单（spec/plan/handoff 各自罗列，与 Kernel
  的通用映射逻辑无关），要么是 `docs/modules/httpapi/README.md` 里描述
  `safeMessage`/`StatusForCode` 的既有语句（描述的是「Action 错误的 Message
  才会出现在响应里」，这句话本身仍然成立，不需要改，只是它没有回答「Handler
  的错误怎么才算 Action 错误」这一层）。没有任何文档描述 Kernel 内部「Handler
  失败时 Code 从哪来」这一步，因此按任务指示的 else 分支，把这句话写成
  `Kernel.Execute` 的 Go doc 注释。

## tests_run

TDD 验证（先红后绿）：

1. 先在 `kernel_test.go` 加两条新用例，`git stash push -- internal/platform/
   action/kernel.go` 把实现临时打回验收线给的原始（有 bug）版本；
   `go test ./internal/platform/action/... -run 'TestKernelPreservesHandler
   ActionErrorCode' -v` —— **两条用例均 FAIL**，报
   `Kernel 应保留 Handler 的错误码, got EXECUTION_FAILED`，证明测试确实在测
   这个 bug。
2. `git stash pop` 恢复实现；同一条命令重跑 —— **PASS**。

后端全量（`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy
-u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy` 前缀绕开本机代理 TUN 对
`go test` 的干扰，见 windows-toolchain-quirks 记忆）：

- `go test ./internal/platform/action/... -v` —— PASS（全部用例，含本片新增
  的两条与既有的 `TestKernelRecordsHandlerFailureWithoutLeaking` 等，DB 相关
  的 `TestPgRunStore*`/`TestActionRunIsAppendOnly` 因未设
  `XM_TEST_DATABASE_URL` 而 SKIP，属预期）
- `go build ./...` —— PASS（无输出）
- `go vet ./...` —— PASS（无输出）
- 私有测试库（避免与本会话其他 Agent 撞库）：
  `docker exec invoice-test-pg psql -U postgres -c "CREATE DATABASE
  xm_test_kernel"` —— 成功新建（非「已存在」）；
  `go run ./cmd/migrate -database "postgres://postgres:test@127.0.0.1:55432/
  xm_test_kernel?sslmode=disable" -path db/migrations up` —— PASS
  （`迁移完成`）
- `XM_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/
  xm_test_kernel?sslmode=disable" go test -p 1 -count=1 ./...` —— **全绿**：
  71 行输出，`cmd/*`、`connectors/*`、`internal/platform/*`（含
  `action`/`alerts`/`assurance`/`audit`/`credentials`/`finance`/`httpapi`/
  `jobs`/`localauth`/`registry`/`savedviews`/`server` 等全部 `ok`），日志里
  `grep -n "FAIL\|panic:"` 无命中。**没有任何既有测试期望需要修改**——开工前
  已定位全仓库唯一会构造「真实 Kernel + Handler 返回错误」的 5 个测试文件
  （`registry/actions_test.go`、`registry/actions_audit_test.go`、
  `savedviews/actions_integration_test.go`、`alerts/audit_integration_test.go`、
  `audit/actionsink_test.go`），逐一确认：`registry`/`savedviews` 的 Handler
  用的是包内 `errors.New` 定义的裸错误（非 `*action.Error`），不受本次改动
  影响；`alerts`/`audit` 的相关用例只测到 `CodePermissionDenied`/
  `CodeNotRegistered` 这两条**前置校验**路径，同样不经过本次改的 Handler
  失败分支。这与全量跑绿的结果一致。
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `GOVERNANCE_BASE_REF=origin/release/v0.1-launch GOVERNANCE_REQUIRE_BASE=1
  bash scripts/check-governance.sh` —— PASS（exit 0，无输出，比对 CI 同款
  基线的迁移不可变检查同样干净）
- `grep -rn "EXECUTION_FAILED" web/apps/admin-web/src` —— 命中 4 个文件
  （`api/finance.test.ts`、`pages/PlatformUserDetailPage.test.tsx`、
  `api/users.test.ts`、`components/ManagedChannelTable.test.tsx`），逐一确认
  全部是**Query 侧只读端点**的「上游/精确查找未完成，请重试」类场景（走的是
  `httpapi` 直接 `action.NewError(CodeExecutionFailed, ...)`，不经
  `Kernel.Execute`），没有一处依赖「Action 校验失败会以 EXECUTION_FAILED
  形式到达」——本片不需要、也没有改动任何前端代码。
- `"$(go env GOROOT)/bin/gofmt" -d internal/platform/action/kernel.go
  internal/platform/action/kernel_test.go` —— 干净（0 输出）。只对本片改动
  的两个文件跑，未对整仓库跑 `gofmt -l .`（本机已知的 CRLF 假警报，见
  windows-toolchain-quirks 记忆）。
- `git diff --check origin/release/v0.1-launch...HEAD` —— 干净（无输出）。
- `gitleaks protect --staged -v`（提交前对暂存区扫描）—— `no leaks found`。

## not_run

- **未修改 `docs/modules/action/README.md` / `docs/modules/httpapi/
  README.md` 正文**：按任务指示的判断顺序，两处都没有精确描述 Kernel 内部
  「Handler 失败时 Code 从哪来」这一步，因此改用 Go doc 注释（见 decisions）。
  若验收线认为这条规则值得进模块 README，是一处可以顺手补的文档债，不影响
  本片正确性。
- **`xm_test_kernel` 测试库未清理**：任务指示是「建一个私有库避免撞库」，
  没有要求跑完即删；本库仅用于本次全量回归，未连接生产/staging，验收线或
  下一个 Agent 如不再需要可自行 `docker exec invoice-test-pg psql -U
  postgres -c "DROP DATABASE xm_test_kernel"`。
- **未新增 httpapi 层的真实 Kernel 端到端用例**：本片按任务范围只在
  `internal/platform/action` 包内加了 Kernel 单元测试；`internal/platform/
  httpapi` 的既有测试用的是假 Kernel 接口，不经过真实 `Kernel.Execute`，因此
  这条修复目前只在 `action` 包层面被单元测试锁定，HTTP 状态码层面的锁定
  依赖的是「假 Kernel 直接构造 `action.NewError` 传给 `WriteError`」这条
  既有链路（`httpapi/actions_test.go` 已覆盖且本次全绿），而不是「真实
  Handler 经真实 Kernel 到 HTTP 响应」这条完整链路。见 follow_ups。
- **未跑前端 typecheck/test/build、Storybook、Playwright/真浏览器实测**：
  本片没有改动任何 `web/` 下的文件（已用 grep 确认不需要），纯后端 Go 改动。
- **`gitleaks` 未做全历史扫描**：只跑了 `protect --staged`（对应本次要提交
  的暂存区改动），未跑 `gitleaks git --log-opts=...` 这种全 range 扫描——
  diff 只有两个 Go 源文件，风险面很小。

## risks

- **行为变化是刻意且外部可见的**：任何 Handler 只要用 `action.NewError`
  返回过域错误码，现在都会让该 Code/Message 真正穿透到 HTTP 状态码、
  `ActionRun.ErrorCode`、审计事件的 `ErrorCode` 三处——这正是本片要修的 bug，
  不是副作用。全量回归零失败、前端 grep 未发现任何依赖旧（错误）行为的用例，
  是这条改动安全的证据，但**没有独立验证 `assurance`/`alerts`/`credentials`/
  `finance` 每一个 Handler 自己的错误映射表是否本身就设计正确**——本片只保证
  「Kernel 不再遮蔽 Handler 的判断」，不重新审查各 Handler 的判断本身对不对。
- 若某个前端页面此前恰好依赖「校验失败也是 502/EXECUTION_FAILED」这个
  当前一律触发的通用错误态 UI（比如统一走某个 toast 兜底），本片会让它开始
  收到更精确的状态码/Code（400/409 等），可能需要该页面补一个更精确的错误
  态展示——已用 grep 确认 admin-web 当前没有这种页面，但 grep 覆盖不到未来
  新增的页面，验收线合入前建议再扫一遍最新前端代码。

## follow_ups

1. 建议后续在 `internal/platform/httpapi` 或某个真实 Handler 所在包
   （比如 `assurance`）补一条「真实 Kernel + 真实 Handler + 真实 HTTP 请求」
   的集成测试，对着一个会返回 `CodeInvalidParams`/`CodeConflict` 之类域
   错误的 Action（如 `assurance` 的声明类 Action 传一个不存在的渠道），断言
   真实 HTTP 响应的状态码与 body，把这条修复在 HTTP 边界上也锁定住，而不是
   只在 `action` 包的 Kernel 单元测试层面。
2. 建议 `assurance`/`alerts`/`credentials`/`finance` 四个包的负责人各自确认
   一遍自己的 `domainError`/`NewError` 映射表：这条 Kernel 级 bug 此前一直
   在生产环境把它们的判断遮蔽掉，现在这些判断会真正生效，值得各自 owner
   过一遍确认设计的 Code/Message 组合仍然是当前想要的行为（本片没有改动
   任何一个包自己的映射表，只是让它们「终于生效」）。
3. 上面 not_run 提到的 README 文档债：如果验收线认为 Kernel 的这条行为值得
   进 `docs/modules/action/README.md` 的正文（而不只是 Go doc 注释），可以
   顺手补一段——本片按任务指示判断「没有文档描述就写 Go doc 注释」，两者
   不冲突，后续加文档不需要改代码。

## acceptance handoff

验收线请审读 `internal/platform/action/kernel.go`、`kernel_test.go` 与本
commit，复跑 `tests_run` 里列的门禁（尤其是全量 `go test -p 1 -count=1
./...`），确认后在 `docs/handoffs/ACCEPTANCE-LOG.md` 追加
`MERGED <sha> XM-KERNEL-ERRCODE0`。本分支无 PR、无 GitHub Actions 依赖、
无部署操作，未 push、未合并。
