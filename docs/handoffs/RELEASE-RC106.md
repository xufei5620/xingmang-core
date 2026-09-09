# RELEASE RC106 —— 开票用户端不再因为一个没见过的状态值整页报错、把已绑定用户领去绑定向导

- status: 候选（源码门禁 `verify.ps1` 待跑；未签名，未传输，未部署）
- branch: `ai/claude/XM-INV-RC106`
- base: RC105 生产提交 `4fa39a4f`（2026-09-08 已部署）+ `a265b90`（RC105 部署结果补记）
- worktree: `K:/发票/wt-XM-INV-RC106`
- 内容：`a3d7a84` → `3db6994` → `d558fe0` → `9c5a8cf` → `2bf67d4` → `1560598`（XM-INV-LOT-REASON-CONTRACT，三轮整改）
  → `55c8828`（发布身份 RC105→RC106，**先于打 tag**）→ 本文
- 切片交接单：`docs/handoffs/XM-INV-LOT-REASON-CONTRACT.md`

## 为什么发

2026-09-08 用户 34、12 打开开票中心看到「开票数据无法读取」，并被领到「关联平台账号」
三步向导——他们的绑定其实是 verified 的。根因是旧前端对 `eligibility_status` 等枚举
做**闭集校验**，遇到后端新值 `not_invoiceable_pending_reconciliation` 就抛错；四个请求
用 `Promise.all` 并发，一个抛错整组作废；catch 只清摘要、不写账号列表，账号列表停在
初始空数组；面板把「空」等同于「未绑定」。这个形状对**任何**后端先于前端发布的新值都会复发。

## 改了什么（用户可见）

- 未识别的资格状态 / 冻结原因 / 平台类型不再整条拒绝：保留原值、行上挂「账本状态待确认」
  或「未识别的平台」徽章（中文，复用既有徽章样式，无新增颜色）。
- 订单、档案、账号列表、资格摘要各自结算；哪一路失败只影响那一路，面板显示
  「这只是本次读取失败……也不需要重新绑定」，不再退回绑定向导。整体失败同样记入失败名单。
- 资格摘要响应信封允许后端加顶层字段。

## 改了什么（契约与门禁）

- `contracts/invoice-eligibility-wire.v1.json` 成为枚举唯一真身；`backend/internal/eligibilitywire`
  用 go/types 常量求值发现后端**所有**会写出的状态值（含命名常量、拼接的 COALESCE 默认值），
  解析不出常量的赋值**拒绝作答**（报 文件:行号），新值不进契约则后端门禁红。
- 前端 `eligibility-wire.generated.ts` 由契约生成；`labels` 对账测试钉住每个值都有中文。

## 门禁（全部实测，UTC）

| 门禁 | 开始 | 结束 | 耗时 | 结果 |
|---|---|---|---|---|
| 后端全量 `go test -p 1 -count=1 ./...`（专用库 `invoice_test_fereasons`，八个代理变量 `env -u`） | 03:04:30 | 03:11:13 | 402s | **exit 0，30 个包 ok**（postgresstore 275.5s、application 25.8s、eligibility-repair 14.8s） |
| `go vet ./...` | 03:11:13 | 03:11:14 | 1s | 0 |
| 前端 `npm run typecheck && npm test -- --run` | 03:11:14 | 03:11:24 | 10s | typecheck 0；**20 文件 / 330 用例全绿** |
| `scripts/check-no-secrets.ps1` | 03:11:24 | 03:11:26 | 2s | 0 |
| `scripts/test-release-image-gate.ps1`（改名后） | 03:2x | — | 2s | exit 0 |
| `scripts/verify.ps1` 完整源码门禁 | 待跑 | | | |
| 镜像门禁 / 产物校验 / 签名 | 待做 | | | |

三轮整改的复审：第一轮两位复审各 2/4 条 major → 第二轮 3 条 major → 第三轮 2 条同类残留，
每轮实现者做变异验证（共 39 条变异，逐条红/绿/还原记录在切片交接单）；最终两位复审
`pass / would_ship=true`。

## 顺序（对照 RC104/RC105 的教训）

1. ✅ 推进发布身份（`55c8828`）——**在打 tag 之前**
2. 🔄 `verify.ps1`（需先 `npm ci`，本工作树已装）
3. 把 `K:/发票/wt-XM-INV-AUTOLOGIN` detach 到本分支最终提交（证据目录 `release/` 在那个工作树）
4. 签名 tag `v0.1.0-rc106-signed`
5. 镜像门禁（预期 exit 42）→ 普通校验 → 严格可传输校验
6. `ssh-keygen -Y sign` 签 `SHA256SUMS`（bash 重定向验签）
7. **停下，把传输/部署命令交给负责人**；部署顺序：仅 API + web 镜像（无迁移、无代理改动）

## 部署后预期观测

- 用户 34 / 12 打开开票中心：账号面板正常列出已关联账号；资格状态显示「对账中暂不可开票」
  而不是报错；不再出现绑定向导。
- 它**不修**待对账自锁本身（用户 12 差一次相符、用户 34 账本缺管理员加余额）——那是
  `XM-INV-PENDING-RECON` 切片（设计已完成，待负责人拍板 D1–D9）。

## 回滚

重新部署 RC105 镜像即可（无迁移、无数据写入形状变化）。
