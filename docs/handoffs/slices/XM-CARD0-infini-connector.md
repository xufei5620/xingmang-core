# XM-CARD0：Infini 卡服务连接器与供应商写通道

## status

IN PROGRESS（第一片的代码部分完成；真实端点验证被密钥与 IP 白名单阻塞）

## branch

`ai/claude/XM-CARD0-infini-connector`（base `release/v0.1-launch` @ `2bd90d3`）

## commit

`9bd74eb` — feat(connector): add vendor write channel and Infini request signing
（本文件与端点层、替身、契约在后续提交）

## summary

平台要接入 Infini 的 API 开卡能力。第一片交付连接器与它所需的传输通道，
不含领域层、投影表、Action 与管理端。

### 为什么要动 internal/platform/connector

`ReadOnlyTransport` 在 `RoundTrip` 层拒绝一切非 GET/HEAD 请求（ADR-018 闸 4，
错误类型 `write_attempt`），而 Infini 的卡接口除查询外全是 POST。平台此前
**发不出任何 POST 给任何上游**。

ADR-018 的适用范围是「平台对 NewAPI/Sub2API 建立独立只读账号」——那两个上游
的业务真相不归平台所有，写它们的库是越界。Infini 是平台作为客户购买服务的
供应商，调用它公开的写接口不属于宪法条款 5 禁止的「直写第三方原始表」。

因此新增并列的第二条通道 `VendorWriteTransport`，闸门与只读通道一一对应：
方法白名单收到 GET/HEAD/POST、主机 allowlist 完全相等匹配、allowlist 为空
fail closed、拒绝重定向、强制超时。两条通道唯一实质差别是「包内无写路径」
这一条写通道消灭不掉，只能靠上层的「所有写操作走 Action」+ 审计。

**这个决定需要验收线确认，并补一份 ADR**（暂定 ADR-021：供应商写通道），
否则这段代码在评审时会被当成绕闸。ADR 未写入本片。

### 签名

口径来自供应商文档第 4 章：待签名串 `{keyId}\n{METHOD} {path}\ndate: {GMT}\n`，
HMAC-SHA256 输出 base64，`Authorization` 用 Signature 方案，
`headers="@request-target date"` 固定。

两处反直觉、已用测试钉住的地方：**请求体不参与签名**（`Digest` 只是独立的
头）；**待签名串以换行结尾**，少一个就是 401。签名与 `Date` 头取自同一时刻，
避免跨秒的偶发 401。

HMAC 与 Digest 的期望值由 openssl 独立算出，不是拿本包实现自证。

### 金额与敏感数据

余额与流水金额经 `money.CurrencyScale` + `money.ParseMinorUnits` 换算成整数
最小单位，全程不落 float；币种不认识时报错而不是猜 2 位。

申请单的三个金额与充值/赎回的 `card_balance` **保持文本形态**：单位是 token
（USDT/USDC），`money.CurrencyScale` 只登记法币，猜错的那 10000 倍不会有
任何症状。

`RevealedCard` 的 `String()`/`GoString()` 返回打码文本，`%v`、`%+v`、`%#v`
都印不出卡号与 CVV。库里只存上游给的 `mask`。

## files_changed

新增：

- `internal/platform/connector/vendor_transport.go` — 供应商写通道
- `internal/platform/connector/vendor_transport_test.go`
- `connectors/infini/signing.go` / `signing_test.go` — HMAC 签名
- `connectors/infini/client.go` / `request_test.go` — 已签名请求组装
- `connectors/infini/envelope.go` / `envelope_test.go` — 信封与错误分类
- `connectors/infini/card.go` / `card_test.go` — 卡片解码与金额换算
- `connectors/infini/endpoints.go` / `endpoints_test.go` — 九个端点
- `connectors/infini/contract.go` — 能力字符串与 `CardClient` 接口
- `connectors/infini/fake.go` / `fake_test.go` — 测试替身
- `contracts/connectors/infini.card.v1.md` — 契约（草案）
- `docs/handoffs/slices/XM-CARD0-infini-connector.md` — 本文件

修改：

- `internal/platform/connector/types.go` — 新增 `KindMethodNotAllowed`、
  `KindRejected` 两个错误分类

## tests_run

- `go test ./connectors/infini/ ./internal/platform/connector/` —— 全绿
- `go vet ./...` —— 干净
- `go build ./...` —— 干净
- `bash scripts/check-governance.sh` —— 退出码 0

全部测试用替身或 `httptest` 驱动，**没有一次真实上游调用，没有花钱**。

## not_run

- **真实端点验证**：密钥与 IP 白名单尚未就绪，签名口径、字段形状、金额单位
  全部只在文档层面成立。
- 全量 `go test ./...`：本片未改动其他包，只跑了受影响的两个包与全量
  build/vet。合入前应由验收线跑全量。
- 前端门禁：本片无前端改动。

## risks

1. **签名口径未经真实验证**。文档第 4 章的阅读结果若有偏差，首次调用会 401。
   排查顺序：签名串结尾换行 → `headers` 值 → Date 格式 → 时钟偏差 → IP 白名单。
2. **`card_alias` 能否在开卡时设置尚未验证**。整个幂等方案建立在这个假设上。
   若上游忽略该字段，超时后的对账要退回「持卡人 + 时间窗 + 金额」的模糊匹配，
   可靠性显著下降。**这是真实验证的头号项目。**
3. **金额单位未验证**。`available_balance` 若不是「美元」而是别的单位，
   换算结果会静静地错 100 倍——不报错、不告警，只有对账时才看得出。
4. **时间格式未验证**。认不出的格式会报错而不是落零值，所以失败是可见的，
   但会让整条读路径不可用。
5. **写通道是新增的攻击面**。平台从此具备向外部发 POST 的能力，虽然被
   allowlist 与方法白名单框住，但这条通道的误用后果是花真钱。
6. **上游无幂等键**是这个功能的结构性风险，不是本片能消除的，只能在领域层
   用台账 + 对账收敛来控制。

## follow_ups

- **ADR-021（供应商写通道）**：必须补，否则写通道在评审时被当成绕过 ADR-018。
- **产品负责人待办**（挡住真实验证）：申请 API key（keyId + secret）→ 申请
  `card.create` 与 `card.reveal` 权限 → 查出平台出口 IP 并登记白名单。
  若服务器走动态出口或代理，白名单会时灵时不灵，须先解决出口固定性。
- **XM-CARD1**：卡表 + 迁移 `000026` + `card.issue` Action + 幂等台账 + 限额
  （建议起步单笔 ≤ 100 USD、单日 ≤ 500 USD，可配置）+ 审计。
- **XM-CARD2**：`card.reveal` 与充值/冻结/解冻/赎回的 Action。
  已定：`reveal` 走 Action 而非普通读接口，理由是 Query 层没有审计钩子，
  而「谁在何时看了哪张卡的明文」是本功能最该留痕的一条。契约里须注明
  「不得引为先例」。
- **XM-CARD3**：管理端页面、开卡表单、reveal 弹窗、流水。
- **同步作业**：`jobs/card_sync.go`，承担开卡异步流程的轮询与不确定态对账。
  建议起步周期：卡状态 5 分钟、流水 15 分钟、`pending` 申请单 30 秒，
  不确定态宽限 30 分钟后亮红条并锁死同参数重试。
- 冻结后的 `status` 取值、限流阈值、申请单终态判定，均待真实验证补入契约。
