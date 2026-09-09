# XM-INVCON-KEYRING-ALG：控制台断言密钥清单 algorithm 字段契约不一致修复

## status

READY（待验收线审读、复跑并人工合入；生产热修复）

## branch

`ai/claude/XM-INVCON-KEYRING-ALG`（base `release/v0.1-launch`，工作树
`acceptance/wt-keyringalg`）

## incident

2026-09-03 12:43Z：开票侧（invoice-system）启用控制台断言兑换后，
`invoice-system api` 启动阶段直接拒绝服务：

```
console assertion keyring: key "2026-09" algorithm must be Ed25519
```

## root cause

`internal/platform/consoleassertion/consoleassertion.go:40` 定义了唯一一个
`Algorithm = "EdDSA"` 常量，同时被两处复用：

1. `claims.go:57`（`signCompact`）拿它填 JWS protected header 的 `alg`
   声明——这里必须是 `"EdDSA"`，没有问题。
2. `keyring.go:105-110`（`PublicKeyRecord.Validate`）与 `keyring.go:173`
   （`NewPublicKeyRecord`）也拿同一个常量校验/填充清单记录的
   `algorithm` 字段——这里错了：冻结技术规格
   `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`
   §3.3 与 `docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md`
   步骤 1 都明确要求这个字段等于密钥算法名 `Ed25519`，不是 JWS 的 `alg`
   声明名 `EdDSA`。开票侧消费者
   （`backend/internal/auth/console_assertion.go:165`，另一个仓库）正是
   按 `algorithm=Ed25519` 这条字面值校验的。

后果：`cmd/console-assertion-keygen` 用 `NewPublicKeyRecord` 生成的公钥
记录带着 `"algorithm":"EdDSA"`，这条记录被提交进本仓库
`contracts/auth/console-assertion-keyring.v1.json`（key_id `2026-09`），
又按既定流程原样复制进 invoice-system 仓库——两仓库对这个错误值达成了
"看起来一致"但实际不符合开票侧真正期望的假一致，直到开票侧真正启用
断言兑换、加载这份清单校验时才暴露。

`keyring_test.go` 里 `TestLoadKeyringJSONRejectsUnknownFields` 等原始测试
其实已经在裸 JSON 字面量里写的是 `"algorithm":"Ed25519"`——测试数据本身是
对的，只是产品代码（`Validate`/`NewPublicKeyRecord`）用错了常量去比对/
填充，测试从未通过真实 `NewPublicKeyRecord`/committed 文件路径跑过，
没有捕捉到这处偏差。

## fix

1. `internal/platform/consoleassertion/consoleassertion.go`：新增独立常量
   `KeyringAlgorithm = "Ed25519"` 专供清单 `algorithm` 字段使用；
   `Algorithm = "EdDSA"` 的文档注释改为明确写清"仅供 JWS header 使用，
   不得复用于清单字段"，并直接引用本次事故。
2. `internal/platform/consoleassertion/keyring.go`：
   `PublicKeyRecord.Validate` 改为比对 `KeyringAlgorithm`；错误消息改为
   明确点名字段：`key %q: manifest field "algorithm" must be %q, got %q`
   （原消息只说"algorithm must be Ed25519"，不点名是清单的哪个字段）。
   `NewPublicKeyRecord` 改为填充 `KeyringAlgorithm`——这样
   `cmd/console-assertion-keygen` 后续生成的记录会自动带上正确值，无需
   改动 `cmd/console-assertion-keygen/main.go` 本身（它只调用
   `NewPublicKeyRecord`，从不直接引用 `Algorithm`）。
3. `contracts/auth/console-assertion-keyring.v1.json`：key_id `2026-09`
   记录的 `algorithm` 字段由 `EdDSA` 改为 `Ed25519`；密钥材料
   （`public_key`/`fingerprint`）、`valid_from`/`valid_until` 等其余字段
   逐字未动。**此文件是两仓库各存一份的只读副本**，本片只改了本仓库这
   一份——需要验收线按既定流程（`docs/superpowers/plans/
   2026-09-03-cr0006-phase2-rollout.md` 步骤 1）把同一条更正后的 JSON
   原样交给开票线合入他们自己的副本、并同步覆盖已经 `scp` 到开票生产
   主机 `/root/invoice-system/config/console-assertion-keyring.json`
   的旧副本——不做这一步，开票侧仍会用错误值拒绝启动。
4. 新增 `contracts/auth/console-assertion-keyring.v1.md`：紧挨清单 JSON
   文件的契约说明文档（本仓库 `contracts/` 目录既有的 `.v1.md` 惯例，
   如 `contracts/connectors/credential-ref.v1.md`），用表格明确写清
   "清单 `algorithm` 字段=`Ed25519`（密钥算法名）" vs. "JWS header
   `alg`=`EdDSA`（签名方案名）"两者互不可混用，并记录本次事故。
5. 回归测试（`internal/platform/consoleassertion/keyring_test.go`）：
   - `TestAlgorithmConstantsAreDistinctAndCorrect`：断言
     `Algorithm=="EdDSA"`、`KeyringAlgorithm=="Ed25519"`
     且两者不相等——钉住两个常量不能再被合并成一个。
   - `TestRealManifestRecordsUseKeyAlgorithmName`：直接 `os.ReadFile`
     加载本仓库真实提交的 `contracts/auth/console-assertion-keyring.v1.
     json`，走 `LoadKeyringJSON` 解析，断言每条记录的 `algorithm` 字段
     等于 `KeyringAlgorithm`——这样以后清单文件再次手工或工具误写成
     `EdDSA`，本仓库自己的测试就会先炸，不必等到开票侧生产环境启动
     失败才发现。
   - 既有 `TestNewPublicKeyRecordSelfValidates` 的断言从比对 `Algorithm`
     改为比对 `KeyringAlgorithm`（原断言本身没错，只是名字要跟着新常量
     走）。
   - JWS header `alg` 仍为 `EdDSA` 这件事本就有覆盖：
     `signer_test.go` 的 `verifyForTest` 早就断言
     `header.Alg != "EdDSA"` 则失败（`TestSignProducesAssertionThat
     VerifiesAgainstAnIndependentVerifier` 等用例都走这条路径），本片
     未改动这部分，新增的
     `TestAlgorithmConstantsAreDistinctAndCorrect` 从常量层面补了一道
     直接断言。

## 关于"验证 platform signer 仍能加载修正后的清单"

核实后：`Signer`（`signer.go`）在运行期**从不加载**
`contracts/auth/console-assertion-keyring.v1.json`——它只经
`SigningKeyProvider.Resolve` 从密钥库解析私钥种子（`XM_INVOICE_CONSOLE_
ASSERTION_KEY_REF` 指向的 `secret://console-assertion/signing-key-<key_id>`），
清单文件是**开票侧验证方**加载校验用的静态清单，签发方（本仓库）从不
读它——这是签名设计本身的既定纪律（见 `signer.go` 顶部注释）。因此：

- `cmd/platform-api/consoleassertion.go` 的
  `buildConsoleAssertionHandlers`/`console_assertion_signer_loaded`
  日志路径与本次改动完全无关，`go build ./cmd/platform-api/...` 与
  `internal/platform/consoleassertion` 全部签名器测试（未改动，全绿）
  已确认这条路径不受影响。
- 本仓库唯一会真正加载这份 JSON 清单文件的代码路径是
  `LoadKeyringJSON`（`cmd/console-assertion-keygen` 的 `-manifest` 追加
  模式、以及本片新增的回归测试）——已通过新增的
  `TestRealManifestRecordsUseKeyAlgorithmName` 验证修正后的文件能正确
  加载且字段值正确；额外手工跑了一次
  `go run ./cmd/console-assertion-keygen -key-id 2099-01 -validity-days 1`
  （不带 `-manifest`，未落盘），确认修复后工具打印的公钥条目
  `"algorithm"` 字段就是 `"Ed25519"`。

## files_changed

- `internal/platform/consoleassertion/consoleassertion.go`（新增
  `KeyringAlgorithm` 常量，`Algorithm` 文档注释改写）
- `internal/platform/consoleassertion/keyring.go`（`Validate`/
  `NewPublicKeyRecord` 改用 `KeyringAlgorithm`，错误消息点名字段）
- `internal/platform/consoleassertion/keyring_test.go`（既有断言改用
  `KeyringAlgorithm`；新增两个回归测试；新增 `os`/`path/filepath` 导入）
- `contracts/auth/console-assertion-keyring.v1.json`（key_id `2026-09`
  的 `algorithm` 字段：`EdDSA` → `Ed25519`，仅此一个字段）
- `contracts/auth/console-assertion-keyring.v1.md`（新增，契约说明文档）

## tests_run

（`unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy
all_proxy` 前缀绕开代理对本机 Go 工具链的干扰）

- `go build ./...` —— PASS（0 输出）
- `go vet ./...` —— PASS（0 输出）
- `gofmt -l .` —— 仅报告一处与本片无关的既有漂移
  （`internal\platform\httpapi\finance_test.go`，`git diff --stat
  release/v0.1-launch -- internal/platform/httpapi/finance_test.go`
  确认本片未触碰该文件，属预先存在的漂移，非本片引入）；本片实际改动
  的 4 个 `.go`/`.json` 文件均未出现在 `gofmt -l` 输出中
- `go test ./internal/platform/consoleassertion/... -v` —— PASS（全部
  35 个用例，含本片新增的 `TestAlgorithmConstantsAreDistinctAndCorrect`
  与 `TestRealManifestRecordsUseKeyAlgorithmName`）
- `go test ./...`（全仓库，一次成功，未见 flake 需要复跑）—— PASS，
  exit 0，51 个含测试的包全部 `ok`，20 个包 `[no test files]`
  （`go list ./...` 确认总数 71，与 `go test ./...` 输出包数一致）
- `go build ./cmd/console-assertion-keygen/... ./cmd/platform-api/...`
  —— PASS；手工跑
  `go run ./cmd/console-assertion-keygen -key-id 2099-01 -validity-days 1`
  确认输出的 `"algorithm"` 字段现在是 `"Ed25519"`（未落盘、未影响仓库
  文件，仅终端验证工具产出的形状）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks git --no-banner --log-opts="release/v0.1-launch..HEAD" .`
  —— 见下方 gate 结果（提交后另行记录）

## not_run

- **开票侧（invoice-system）代码/测试**：不在本仓库范围内，任务约束
  明确禁止改动 invoice-system 代码；本片只能保证本仓库这一份清单文件
  与生成/校验它的 Go 代码内部一致，开票侧是否已同步更正需要验收线走
  既定跨仓库同步流程确认（见上方 fix 第 3 条）。
- **真实密钥轮换/生产部署验证**：本片是纯代码 + 契约文档 + 单份清单
  文件字段修正，不触碰 `XM_INVOICE_CONSOLE_ASSERTION_KEY_REF`
  等任何生产配置、不生成新密钥、不重启任何服务。
- **web/、deploy/**：任务范围明确排除，未触碰。

## risks

- **两仓库清单文件的同步是人工流程，本片无法替代**：本片只修正了
  `xingmang-platform` 仓库自己这一份 `contracts/auth/console-assertion-
  keyring.v1.json`；如果验收线只合并本片而不同步把更正后的
  `algorithm` 字段值交给 invoice-system 仓库更新它自己的副本、并覆盖
  已经 `scp` 到开票生产主机的旧文件，事故会原样复现——这一步是本次
  事故的直接触发点，必须显式确认完成，不能假设"两边看起来一样"。

## follow_ups

- 无新增 follow-up；本片是单点修复，范围内的代码/文档/测试改动均已
  完成并自洽。跨仓库同步义务已在上方 risks 中显式记录，等待验收线
  执行。
