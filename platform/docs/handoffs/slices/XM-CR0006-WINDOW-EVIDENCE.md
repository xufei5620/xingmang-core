# XM-CR0006-WINDOW-EVIDENCE：观察窗口的取证脚本（只读）

- **status:** implemented，未运行过生产。**一个只读脚本**，不改代码、不改配置、
  不碰生产。
- **branch:** `ai/claude/XM-CR0006-WINDOW-EVIDENCE`（自 `release/v0.1-launch`）。
- **依据：** `docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md` 步骤 5。
  窗口 2026-09-04 ~02:00Z 起 ≥3 天 → **2026-09-07 收口**。

## 为什么

步骤 5 要求窗口内逐日检查两组审计计数（第三项"无 Keycloak 恢复演练"已于
2026-09-05 单独 PASS）。以前每次都是现场手敲 SQL——**手敲的查询没法逐日比对，
也没法在收口那天证明"每天问的是同一个问题"**。收口要写进 ACCEPTANCE-LOG 的
是数字，而数字只有在口径固定时才可比。

## 脚本做什么

`deploy/scripts/cr0006-window-evidence.sh --since 2026-09-04T02:00:00Z`
打印三组数字：

1. **开票侧窗口内 `actor_type='oidc'` 的行数**（门槛：必须为 0）。
2. **两侧断言计数**：平台 `audit.audit_event` 的
   `staff.console_assertion.issue` 按 `result` 分组；开票 `audit_events` 的
   `auth.console_assertion.exchanged` / `.rejected` 按 `action` 分组。
3. **被拒断言按 `reason` 的分布**——不是只数总数：`ADMIN_STEP_UP_REQUIRED`
   （TOTP 过期要重做）是正常的，`ASSERTION_INVALID` 与 `ADMIN_NETWORK_DENIED`
   堆积才要查，三者处理方式完全不同。

末尾附一段「怎么读这些数字」。**脚本不给结论**：它把同一组问题每天问一遍，
结论由人写进 ACCEPTANCE-LOG。

## 三处安全/正确性上的讲究

**只读到底。** 每个会话先 `SET default_transaction_read_only = on`——即便有人
拿一个可写角色来跑，事务本身也拒绝写。

**容器名不写死，按"能不能连上这个库"发现。** 两侧 compose 项目名不同（平台是
`--project-name xingmang-launch`，开票没给 `--project-name`、跟目录走），写死一个
名字的下场要么是"没这个容器"，要么更糟——**连上另一个库把数字读错**。发现不
唯一就停下来列出候选，让人用 `CR0006_*_PG` 显式指定。

**窗口左闭右开 `[since, until)`。** 逐日跑时相邻两天不会把同一行数两遍。

## 测试：四条 SQL 都在真实 PostgreSQL 上跑过

不是"看着对"——**逐条在真库上执行过**，用的是本机开票测试库
（`invoice_test`，schema 与生产同源）；平台那条在同一个库里用临时 schema 造出
同名同型的两列跑，验的是语法与分组。

**这一步不是走过场，它抓到了一个真错误**：第 3 条原本写
`GROUP BY 1` —— PostgreSQL 直接报
`aggregate functions are not allowed in GROUP BY`（`1` 指向的是含 `count(*)` 的
输出列）。改成按分组表达式本身分组。**这个错在生产上会等到收口那天才暴露。**

平台那条还顺带钉住了两件事：分组要按 `result` 拆开（不然"失败了几次"看不见）、
别的 `action_id` 不能混进来。

另外确认了 `now()` 与 `now()` 比是 false（同一事务时间戳），这正是左闭右开的
真实语义。

门禁：`bash -n` 通过、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 无泄漏。**没有对生产跑过**——那是收口那天的事。

## 收口那天要做什么（需要负责人在场）

1. 跑一次 `bash deploy/scripts/cr0006-window-evidence.sh --since 2026-09-04T02:00:00Z`。
2. 按脚本末尾那段读数字，把结论（3 天洁净 + 演练 PASS 是否达成）记进
   `ACCEPTANCE-LOG`。
3. **门槛达成不等于下一阶段开工**：计划步骤 6 明确要求产品负责人**另行点头授权**
   `XM-INV-KEYCLOAK-RETIRE` 立项（决策清单第 4 项）。这一片不碰那件事。

## follow_ups

- 脚本没有把结果落盘成可归档的证据文件（今天靠 `tee`）。收口只有一次，
  真要逐日归档再说。
- 平台侧那条只在临时表上验过语法，**没有对平台的真实 schema 跑过**
  （本机没有起平台测试库）。列名取自 `db/migrations/000003_init_audit.up.sql`，
  收口那天第一次真跑时留意列名报错。
