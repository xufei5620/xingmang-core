# XM-0030 Action Advanced Controls(审批中心)设计文档

> **状态:设计稿,待产品负责人拍板。不含实现。**
> 依据:规格 §4.3 审批模型、§22.6 Foundation-B 范围、ADR-003(Action 唯一写入口)、
> ADR-009(AI 复用 Action 不设后门)、宪法 17/24 条(合并与审批权属于人;
> AI 不作为 L3/L4 第二审批人)。

## 1. 要解决的问题

Foundation-A 的内核对 L2+ 一律返回 `ADVANCED_CONTROLS_REQUIRED`(fail closed)。
这挡住了:Connector/Connection 登记(L2/L3)、Kill Switch 拉闸(L2)、以及
Foundation-B 全部写场景(退款 L4、服务切换 L3、批量配置 L2)。
XM-0030 给内核补上「先批准、后执行」的通道,同时不给任何绕过 Action 的口子。

## 2. 核心对象:ApprovalRequest(审批单)

```
approval.request
  id uuid PK
  action_id / action_version      -- 要执行什么
  params_json jsonb               -- 全量参数,提交时冻结
  params_hash text                -- SHA256(canonical(params)),执行时复核
  risk_level                      -- 冗余记录提交时的等级(等级表可能演进)
  environment                     -- 外键 core.environment
  requester_id / requester_type   -- 提交人(可以是 AI)
  reason text NOT NULL            -- 为什么要做(审计要求)
  status: PENDING / APPROVED / REJECTED / EXECUTED / EXPIRED / CANCELLED
  expires_at                      -- 默认 24h,过期自动 EXPIRED(防陈年审批单复活)
  created_at / decided_at / executed_at
  execution_run_id uuid NULL      -- 执行后回填 ActionRun.ID(一单最多一跑)
approval.decision                 -- 审批记录,一单可多条(双人复核)
  id / request_id FK
  approver_id / approver_type     -- **必须 HUMAN**(见 §4)
  verdict: APPROVE / REJECT
  comment
  created_at
```

## 3. 流程(状态机)

```
提交(任何 Principal,含 AI)          审批(仅 HUMAN)             执行
Action 请求 L2+ ──► 内核不再直接拒绝,│
                    而是落一张 PENDING 审批单,返回 202 + request_id
                                      │
        PENDING ──APPROVE(满足所需票数)──► APPROVED ──任意持权者触发──► EXECUTED
           │                                   │                     (内核校验后跑原 Action)
           ├─REJECT──► REJECTED                └─过期──► EXPIRED
           └─提交人撤回──► CANCELLED
```

关键裁决点(需要拍板的写在 §7):

- **票数按等级**:L2 一票;L3 两票且**审批人≠提交人**;L4 两票且至少一名
  持特权 scope(`approval.l4`)——具体人选映射进 RoleScopeMap,不进 Keycloak。
- **AI 红线(宪法 24)**:decision.approver_type 必须 HUMAN,库层 CHECK +
  内核双重校验;AI 可以提交、可以催办、**不能投任何一票**。
- **执行 ≠ 审批通过自动发生**:APPROVED 后需要显式「执行」动作(任何持
  原 Action 权限者可触发,含提交人)。理由:审批的是意图,执行要挑时机
  (夜间维护窗等);自动执行会把审批人变成事实执行人,责任混淆。
- **params 冻结**:执行时内核重算 params_hash 与单上比对,不一致即拒——
  审批过的是这份参数,不是这个意图的任意版本。
- **幂等**:execution_run_id 唯一;重复触发执行返回首次结果(至多一跑)。
  Action Handler 自身的幂等仍由各 Handler 负责(与现状一致)。

## 4. 与现有设施的关系

| 设施 | 接法 |
|---|---|
| Action 内核 | `RequiresAdvancedControls()` 分支从「直接拒」改为「落审批单」;新增 `ExecuteApproved(requestID)` 入口,校验单状态/票数/hash/过期后走**原有 Execute 全链**(权限/环境/Schema 一项不少) |
| 审计链 | 提交/每票/执行/过期各落一条审计事件(复用 ActionSink;审批单 id 作 resource_id)。执行事件的 ActionRunID 与普通执行一致——审计视角看,审批只是执行前多了可追溯的前置事件 |
| HTTP API | `POST /api/v1/approvals`(由内核代落,一般不直调)、`GET /approvals?status=`、`POST /approvals/{id}/decide`(scope `approval.decide`,L4 另需 `approval.l4`)、`POST /approvals/{id}/execute` |
| 前端 | 「平台治理 → 变更与审批」页启用:待办队列(按等级分组)、单详情(参数 diff 视图+理由)、投票/驳回、执行按钮;导航禁用位已预留 |
| 告警 | 新规则:PENDING 超 4h 未决 → warning(有人在等) |

## 5. 明确不做(Foundation-B 首版边界)

- 不做委托/代理审批、不做审批链模板(固定按等级票数);
- 不做参数修改后重审(改参数=新单);
- 不做定时执行(APPROVED 后人工挑时机);
- 不接 IM 内一键审批(通知里给链接,决策必须回到平台——会话不可审计)。

## 6. 实施切分(拍板后)

1. XM-0030a 迁移 + approval 包(状态机/Store/票数策略)+ 内核接线,容器测试
   覆盖全部状态转移与红线(AI 投票被拒/票数不足/hash 漂移/过期);
2. XM-0030b HTTP + 审批页;
3. XM-0030c 告警规则 + Runbook(含「审批人失联怎么办」)。

## 7. 待拍板问题

| # | 问题 | 建议默认 |
|---|---|---|
| 1 | L3/L4 票数与 `approval.l4` 持有人 | L3=2 票、L4=2 票含 1 特权票;首批特权票仅产品负责人 |
| 2 | 审批单有效期 | 24h(L4 可缩至 4h?) |
| 3 | 提交人可否给自己的单投票 | L2 可(单票即自批,等级本意如此);L3+ 不可 |
| 4 | APPROVED 后执行窗口 | 批准后 24h 内须执行,否则回 EXPIRED |
