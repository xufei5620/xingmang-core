# RELEASE RC105 —— 堵住 RC104 埋的「停放事件一唤醒就判死」，并让写off工具正确认出不可重投

- status: 候选（源码门禁 `verify.ps1` 运行中；未签名，未传输，未部署）
- branch: `ai/claude/XM-INV-RC105`
- base: RC104 生产提交 `14bf7e7`（2026-09-08 已部署）
- worktree: `K:/发票/wt-XM-INV-RC105`
- 内容：`d28bb8b`（手册五处落差 + 分层设计文档）→ `1895935` + `ff64b48`（XM-INV-BINDING-SKEW / L0）
  → `8973c00`（发布身份 RC104→RC105，**先于打 tag**）→ `aafdb15`（L0 交接单）

## 为什么紧接着 RC104 再发一版

RC104 把三条死信里的余额检查点合法写掉了，但两条 usage 重投后立刻又因
`source fact event time/watermark is invalid` 八次判死——RC104 的 CLAIM-BINDING 优先
用最新投递的映射却不看时间，`validateFactMetadata` 的 5 分钟规则必拒。写off工具的
护栏只看周期状态，坚持说「可重投」而拒绝写掉。**两个工具互相推诿，readyz 仍 503，
6 个客户仍开不了票。** 而且这条链对任何「绑定前就有停放事件」的客户都会复发。

RC105 的 L0 让认领按「时间合法 > 状态合法 > 首批次」排序，并让修复工具用**同一个**
比较函数判定，于是：那两条 usage 会被 `ingest-requeue-dead` 报 `ReplayBlocked=true`、
被 `ingest-acknowledge-unreplayable` 放行写掉 → Dead 归零 → readyz 200 → 6 个客户恢复。
写掉的是客户 2222 的两条**用量**（少算他的消费，方向保守，且他本人账号已冻结、0 张额度）。

**它不修根因**（对账放弃在途周期 → 孤儿；一个账号拖住全体）——那是设计文档的 L1–L4。

## 部署后的修复动作（负责人签字项，先 dry run）

| 流 | event_id | 预期 |
|---|---|---|
| usage | `63872270-e6d8-8223-a3ac-d20ab65242ad` | requeue dry-run：`ReplayBlocked=true`，reason 提到 validateFactMetadata / 5 分钟；acknowledge dry-run：`acknowledged: true` |
| usage | `bb412266-625b-8485-9aed-5f1b0d9b00d1` | 同上 |

调用形状（tools 镜像，两个 secret 只读挂载，见 RC104 记录）：
`--kind=ingest-acknowledge-unreplayable --event=<id>` 先不带 `--apply`；确认后加
`--apply --operator-id=99ed401b-e78a-4883-b9bf-f4cb4ba1cf17`。

然后：Dead=0 → readyz 200 → `funding_lots` 不再 `source_unavailable` → 通过管理端解冻 2222 的三个冻结。

## 门禁

- L0 分支后端全量（专用库 `invoice_test_bindskew`，八个代理变量 `env -u`）：
  `go test -p 1 -count=1 ./...` **退出 0，29 个包 ok**（`postgresstore` 254.8s、`application` 27.5s、
  `eligibility-repair` 16.1s）；`go vet ./...` 0；`check-no-secrets.ps1` 0。
- 发布身份改名后 `scripts/test-release-image-gate.ps1` exit 0。
- `verify.ps1` 完整源码门禁：**exit 0**（`logs/detached-runs/rc105-gate-20260908T135252Z-e9aa`，
  含隔离 PostgreSQL 集成测试与 PG15/PG18 来源契约、Sub2API v0.1.179 契约快照、New API 快照，
  `All local verification gates passed`）。
- 镜像门禁 / 产物校验 / 签名：待做。

## 顺序（对照 RC104 的教训）

1. ✅ 推进发布身份（`8973c00`）——**在打 tag 之前**
2. 🔄 `verify.ps1`
3. 把 `K:/发票/wt-XM-INV-AUTOLOGIN` detach 到本分支最终提交（证据目录 `release/` 在那个工作树）
4. 签名 tag `v0.1.0-rc105-signed`
5. 镜像门禁（预期 exit 42）→ 普通校验 → 严格可传输校验
6. `ssh-keygen -Y sign` 签 `SHA256SUMS`（bash 重定向验签）
7. **停下，把传输/部署/修复命令交给负责人**

## 补记：2026-09-08 部署结果（UTC；+08 加 8 小时）

| 步骤 | 开始 | 耗时 | 结果 |
|---|---|---|---|
| 签名备份（停服务） | 14:18:25 | 221s | `invoice-20260908T141825Z`，验签 Good，7 组件，dump 1.5G；暂存私钥已 shred |
| 传输 641MB + 服务器侧校验和 | 14:18:50 | 2m25s | 三个 OK |
| 加载镜像 / imageId 比对 / 服务器自验签名 | 14:22:11 | ~50s | 9/9 相符；签名 Good；66/66 OK |
| 展开源码 + 环境文件 | 14:23:01 | 1s | `4fa39a4f`；仅改 `INVOICE_IMAGE_TAG=0.1.0-rc105` |
| roll-forward（负责人执行） | 14:24 | ~3m | 步骤 0b–5 通过，第 6 步按预期红（readyz 503：`source_ingest_dead_events`） |
| `ingest-acknowledge-unreplayable` dry run ×2 | 14:28:2x | 40s | 两条均 `acknowledged: true`；重投工具同时报 `replay blocked`（同一理由：批次 90adb1e5 ceiling 09:29:16 > observed 07:08:33 + 5m）——**L0 让两个工具在生产上说同一句话** |
| apply ×2（operator `99ed401b-…`） | 14:29:40 | 2s | 两条 → `processed / UNREPLAYABLE_BINDING` |
| 验证 | 14:29:42 | — | dead/failed = 0；**readyz 200（内网与公网）**；十条流 pending/dead 全 0；19 张额度 / 6 用户不再 `source_unavailable` |

部署记录：`/root/invoice-system/deployment-records/rc105-deploy-20260908T141816Z/`。
客户 2222 的三个 EVENT_DEAD 冻结按设计保持开放，走管理端解冻流程。
