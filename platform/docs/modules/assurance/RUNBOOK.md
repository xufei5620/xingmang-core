# 渠道主动探测运维手册（登记凭据 · 打开开关 · 排障）

范围：XM-ASSURE1-core（后端：迁移/Action/Job/Query）。检测任务/历史记录的
前端子页签还没接（那是 XM-ASSURE1-ui），本手册只覆盖能通过 Action 直接操作
的部分——fake 模式今天就能全链路验证，real 模式需要按下面的步骤完成登记。

## fake 模式（默认，零成本，今天就能用）

任何平台只要没有调用过 `connector.config.set@1`，或调用过但 `mode='fake'`，
`assurance.probe.run@1` 都会直接放行，走 `assurance.FakeClient` 的确定性
假响应——**不检查任何 Kill Switch/预算**（这是 ADR-019 的明确设计，不是
遗漏）。声明一个 `min_viable_request` 模板的检测任务、调用 `run@1`，几毫秒
内就能看到 `probe_run.status=succeeded` 与对应的 `probe_result` 行。

fake 模式的探测客户端里，声明一个模型名以 `-fake-fail` 结尾的 target
（如 `gpt-4o-mini-fake-fail`）会确定性触发失败，用来验证"批次里一个
target 失败不影响其它 target"这条纪律，不需要任何真实凭据。

## 切到 real 模式：完整步骤

真实探测需要**三层都打开**，缺一层都会被 `run@1` 拒绝并给出具体原因
（不是笼统的"未接入"）：

### 1. 该平台的连接器必须已经是 `mode=real`

```
connector.config.set@1
  platform=sub2api mode=real endpoint=https://<被管平台自己的域名>
  target_allowlist=<该域名> credential_ref=secret://<只读 admin 引用>
```

这是 ADR-018 既有的只读 admin 通道，探测不新增这一步，只是**前提**——探测
自己的凭据是下一步单独登记的，与这里的 `credential_ref` 完全分离。

### 2. 登记探测专用凭据

探测凭据走既有的 `credential.secret.upsert@1`（`internal/platform/
credentials`），与只读 admin 凭据的登记方式相同，只是**换一个 scope/name**，
比如 `secret://sub2api-probe/token`。**不要复用只读 admin 的那个引用**——
两个凭据分离是 ADR-019 的硬性要求，混用意味着探测账号与只读监控账号是同一
个账号，出问题时分不清是谁的流量。

建议在被管平台（Sub2API/NewAPI）自己的产品里为探测新开一个专用账号/Key，
并在该产品自己的后台给这个账号打上可识别的展示名/标签（这是账号侧的运营
约定，不是本片代码能强制的，见 ADR-019 威胁模型 §7.3）。

### 3. 打开平台级 Kill Switch

```
assurance.probe.kill_switch.set@1
  platform=sub2api probe_enabled=true probe_credential_ref=secret://sub2api-probe/token
```

- 需要权限 `assurance.probe.kill_switch`（**不是** `connector.manage`——见
  "权限模型"一节，这是刻意的独立授权）；
- 校验：该平台 `connector_config` 必须已存在（第 1 步）、当前 `mode`
  必须是 `real`、`probe_credential_ref` 必须非空且是合法引用；
- `probe_credential_ref` 省略（参数完全不传）保留原值不动；显式传空串
  清空；
- 关闭时（`probe_enabled=false`）同样可以省略/清空引用。

### 4. 打开全局 Kill Switch（部署环境变量）

```
XM_ASSURE_PROBE_ENABLED=true
```

**`cmd/platform-api` 与 `cmd/platform-worker` 都要配这个变量，且必须是同一
个值**——两个进程各自独立解析（互不可见对方内存，与 `XM_CONNECTOR_PROBE_
ENABLED` 的既有先例相同）：`run@1` 接受时读的是 `platform-api` 侧解析的值，
Job 执行时刻复检读的是 `platform-worker` 侧解析的值。只配一边会造成
"Action 说能跑，Job 执行时刻却说 global_kill_switch_off"的诡异体验。

可选：`XM_ASSURE_PROBE_DAILY_BUDGET`（每平台每日预算，默认 50，两个进程也
各自独立解析同一个变量）。

四层都打开后，声明一个真实检测任务并调用 `run@1`，会真的对
`target_host`（必须在该平台 `target_allowlist` 内）发一次 HTTP 请求。

## 权限模型（登记哪个角色）

- `assurance.probe.manage`（declare/cancel）与 `assurance.probe.run`：默认
  已在 `admin` 角色（`internal/platform/oidcauth.DefaultRoleScopeMap`）；
- `assurance.probe.kill_switch`：**不在** `admin`，专门角色
  `assurance-probe-admin`；本地登录（`XM_AUTH_MODE=local`）下用
  `staff.account.set_roles` 把这个角色授予负责审批"这个平台允许探测花钱"
  的人；OIDC 模式下在 `XM_OIDC_ROLE_SCOPES` 里显式映射，或在 Keycloak Realm
  建同名角色。

## 排障

| 现象 | 先查什么 |
|---|---|
| `run@1` 一直 `refused`，原因 `global_kill_switch_off` | 两个进程的 `XM_ASSURE_PROBE_ENABLED` 是否都设成 `true`（不是只设了其中一个） |
| 原因 `platform_kill_switch_off` | `core.connector_config.probe_enabled` 是否真的是 `true`（查 `assurance.probe.declare@1` 同一权限下能读到的检测任务表 `kill_switch_state` 列，或直接查库） |
| 原因 `probe_credential_missing` | `probe_credential_ref` 是否非空；引用指向的文件是否真的存在于 `XM_SECRET_ROOT`（`credential.secret.upsert@1` 是否真的落过盘） |
| 原因 `target_host_not_allowlisted` | 声明的 `target_host`（小写规整后）是否与该平台 `connector_config.target_allowlist` 里的某一项完全相等 |
| 原因 `daily_budget_exhausted` | 等到明天 00:00（UTC+8 业务日）自动重置，或临时调高 `XM_ASSURE_PROBE_DAILY_BUDGET`（需重启两个进程） |
| 原因 `cooldown_not_elapsed` | 距该声明上一次非 refused 的批次不足 60s，稍等重试 |
| 原因 `platform_run_in_progress` | 该平台已有一条 `pending`/`running` 批次，等它跑完（`assurance_probe` 队列的任务永远 `MaxAttempts=1`，卡住的批次不会自动重试，需要人工介入排查 Job 是否失败退出） |
| `probe_run.status='failed'`（不是某个 target 的 `failed`，是整个批次的 `failed`） | 这是**存储写失败**，不是探测失败——查 `platform-worker` 的 `job_failed`/`assurance_probe_result_write_failed` 日志，通常是数据库连接问题 |

## 未接线的部分（明确留白，不是遗漏）

- `schedule_cron`：声明时可以填，会存进 `probe_declaration`，但**不会
  自动触发任何调度**——本片没有周期任务去读这一列。Query 层的策略文案会
  显式说"已声明定时表达式，本片尚未接线自动触发"。
- 保留期清理：`assurance.probe_run`/`probe_result` 目前没有接入
  `internal/platform/jobs/retention.go` 的清理任务，见交接文档 follow_ups。
