# XM-CHANGES0：版本与发布页建成（/changes 从占位毕业）

- **status:** implemented，未上线。纯前端，无后端改动。
- **branch:** `ai/claude/XM-0030a-approval-core`
- **来源：** 产品负责人 2026-09-07 指示「管理后端未建的需要全部建立」。
- **同批：** XM-FINANCE-GLOBAL0、XM-DESIGN0。

## 改了什么

**`web/apps/admin-web/src/pages/ChangesPage.tsx`（新增）** —— 五格页面本体。

**五格里只有两条真实读数，且两条都很窄——页面上逐字说清了窄在哪：**

- **「发布与回滚」** 回答「控制平面**现在**跑的是哪个提交」（`GET /api/v1/ops/overview`
  的 build 字段）。它**不是发布历史**：一次回滚之后这条读数直接变成回滚后的提交，
  不留上一条。
- **「变更单」** 回答「审批中心在这套后台接没接」（`GET /api/v1/approvals` 通不通）。
  它回答的**不是**这一格要的变更单台账——页面上用一张 `ApprovalCenterScopeCard`
  明说这件事。

其余三格（自动测试与质量 / 发布包与安全检查 / 数据库变更）后端一条端点都没有，
保持蓝图态：列头照旧、表体「未接入」。

## 关键取舍：借蓝图的结构，不借它的归因

`blueprints/governance.ts` 的 `CHANGES_BLUEPRINT` 是**冻结的设计产出**
（`blueprints.test.ts` 与 `navigation.ts` 逐字对账），所以列结构直接复用
`BlueprintTabView`，不在这里重抄一份列头。

但那份规格里每一格的落款写的是「随 Foundation-B（XM-0030）上线」——**这句话现在
是错的**：Foundation-B 已交付并启用，这几格真正在等的是别的东西。所以本片加了
`honestBlueprintTab()`，只替换 `source` 与 `tables[].source` 两处归因，结构原样透传：

| 格 | 真正在等什么 |
|---|---|
| 变更单 | 产品负责人裁定「变更单要不要搬进平台登记」。今天真实的变更单是仓库 `docs/change-requests/` 下的 CR-xxxx（Markdown），后台没有读仓库文件的路径。**等的是裁定，不是排期。** |
| 发布与回滚 | 平台没有发布记录表，也没有发布/回滚端点。已发生的发布只在 `docs/handoffs/ACCEPTANCE-LOG.md` 与 `RELEASE-*.md` 里 |
| 自动测试与质量 | GitHub Actions 自 2026-08-29 起停摆，过渡期改由「分支内 Handoff + 服务器本地门禁状态」承担追溯义务（宪法 §六，**有效期至 2026-09-12**，产品负责人须在该日前复审）。复审之前接什么都定不下来 |
| 发布包与安全检查 | 全仓找不到 cosign / syft / trivy / grype 的调用；镜像由 `deploy-local.sh` 在目标机 compose build、不推 registry。**等的不是接线，是先要有这些环节** |
| 数据库变更 | 迁移是 Platform Lifecycle Operation（宪法 2、3 条），走版本化脚本 + 人工批准，**不经 Action 通道，所以等的不是 Foundation-B**。缺的是一条只读 Query，外加定它归 `ops.read` 还是 `registry.read` 并同步 dbroles 授权 |

**没有改冻结的蓝图规格文件本身**——那是设计真相源，改它要走设计流程。

## 顺带修掉的过期文案（enablement 造成的）

XM-0030 启用之后，四处写死的「等 Foundation-B / 尚未启用」全部失真：

1. `lib/workbench.ts` 的 `approvals` 与 `changes` 两条 `blockedBy`。
2. `lib/workbench.ts` 的 `expiring` 条：说「随人员与权限页上线」，但那页早建成了——
   **真正的缺口在更下一层：凭据模型里根本没有到期这个概念**（`secrets.CredentialRef`
   只有 scope/name，全仓找不到任何 `ExpiresAt` / `RotatedAt`）。
3. `api/approvals.ts` 的 `APPROVALS_NOT_MOUNTED_DESCRIPTION`：审批服务现在是**无条件
   注入**的，再看到整组 404 已经不是「等启用」，而是前端在对一个启用之前的旧
   platform-api 说话。改成指向后端版本，别把人支去等一个不会再来的排期。
4. `pages/ActionsPage.tsx` 的门禁横幅：旧措辞**两处都错**——「尚未在本环境启用」不再是
   编译期能断言的事实，「本页不提供任何执行入口」也不成立（「待审批」里已批准的单上
   就有执行按钮）。收回到唯一始终为真的那件事：**目录页不是执行入口**。

## tests_run

- 前端 typecheck / test / build 三项 ✅（2063 条全绿）
- `bash scripts/check-governance.sh` ✅ / `gitleaks protect --staged` ✅
- **变异验证**：`/changes` 退回 `built: false` → `navigation.test.ts` 三条同时变红。

## risks

- 「变更单」格用 `/approvals` 通不通来回答「审批中心接没接」，是**借了一个端点问另一个
  问题**。页面上说明了，但如果将来 `/approvals` 因别的原因 404，这一格的措辞会指向
  一个不相干的结论。

## follow_ups

- **XM-WORKBENCH-APPROVALS**：工作台「待审批」格接审批单取数（后端已启用，只差前端）。
- **需产品负责人裁定**：变更单要不要搬进平台登记（决定「变更单」格接什么）。
- **需产品负责人复审（2026-09-12 前）**：Actions/PR 双轨恢复还是正式废止。
- 凭据到期元数据（决定工作台「即将到期」格能不能算）。
