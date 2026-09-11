# 操作卡:影子对比(平台 vs SoloAI)

> 目标:并行 14 天,逐(账号,业务日)在**分粒度**确认平台算的钱与 SoloAI 一致。
> 规格 §22.3 要求**连续 14 个自然日、其中 7 个 T+1 结算日无未解释重大差异**,
> 才能切换 admin.solov.cc、归档 SoloAI。这份报告就是那个判据。

## 范围:只有计量型渠道

只比 `access_method='upstream_key'` 的渠道(sub2api / newapi)。
**订阅型与 official_api 不在范围内**——SoloAI 那边根本没有对应的对象(设计稿 §9),
混进来会全部变成「平台有、SoloAI 无」的假差异,把真差异淹掉。范围收窄在 SQL 里,
不需要你做任何事。

## 一次性:建一个 SoloAI 库的只读角色

平台侧强制的是「不发写语句」,**做不到「服务端拒绝写」**——后者只有发一个真正的
只读角色才做得到。在 SoloAI 的库上执行:

```sql
CREATE ROLE xm_shadow LOGIN PASSWORD '<换成一个强口令>';

-- 会话级只读:即使工具发了写语句也写不进去。
-- 平台的连接同时会在启动包里带 default_transaction_read_only=on,
-- 两层都设是有意的:中间若有 pgbouncer 吞掉启动参数,这一条仍然生效。
ALTER ROLE xm_shadow SET default_transaction_read_only = on;

GRANT CONNECT ON DATABASE <SoloAI 库名>      TO xm_shadow;
GRANT USAGE   ON SCHEMA public               TO xm_shadow;
GRANT SELECT  ON public.relay_profit_daily   TO xm_shadow;
```

> 工具只读这**一张表**。四道机械闸:连接串显式写 `default_transaction_read_only=off`
> 会被拒;启动包强制只读;**每条新连接**都向服务端复核两个 GUC,不通过就拒用;
> 所有 SQL 过 SELECT 白名单(含分号拦截)。实现在
> `internal/platform/pgreadonly`——与读 new-api 库用的是同一份代码。

## 三个值

| 变量 | 填什么 | 例子 |
|---|---|---|
| `XM_SHADOW_SOLOAI_DSN` | SoloAI 库连接串,**不含密码** | `postgres://xm_shadow@10.0.0.9:5432/soloai?sslmode=require` |
| `XM_SHADOW_SOLOAI_PASSWORD_REF` | 口令引用(固定写法) | `secret://soloai/shadow-db` |
| `XM_SHADOW_SOLOAI_PASSWORD` | 上面那个引用的落点:只读角色口令**明文** | `...` |

> ⚠️ 连接串里**不许带密码**,`?password=` 也不行——pgx 会把 query 参数当连接
> 设置读并覆盖 DSN 的表面声明,那是真实的绕过口子。工具会拒绝并说明原因。

平台库连接用 `-database` 或 `XM_DATABASE_URL`;环境用 `-environment` 或 `ENVIRONMENT`。

可选旋钮:

| 变量 | 默认 | 说明 |
|---|---|---|
| `XM_SHADOW_TOLERANCE_MINOR` | **0(严格)** | 每格每项允许的绝对差额,单位**分**。§12 拍板:严格 0 + 旋钮默认关。 |

## 每天跑一次

在 `platform/` 项目目录构建并直接运行二进制；构建失败时不执行旧二进制。

```bash
go build -o ./platform-shadow ./cmd/platform-shadow &&
  ./platform-shadow -environment production
```

直接执行保留工具的 0/1/2 退出码；`go run` 会把非零工具退出码统一包装为 1，
不能用于下面按退出码分类的正式记录。构建产物按当前平台选择可执行文件后缀。

不带 `-from/-to` 就是**昨天往前的 14 天**(CST)。为什么到昨天为止:今天的行还在被
采集任务反复覆盖(§5.3「今日可覆盖、过去冻结」),拿它对比会得到一个随时间变化的
结论——早上跑是差异、下午跑又对上了,那种报告作不了验收证据。

只看某一天:`-from 2026-08-27 -to 2026-08-27`。

### 退出码

| 码 | 含义 |
|---|---|
| 0 | 全部对上(clean) |
| 1 | 有差异 / 缺行 / 未知 / 口径错误(dirty)——**报告已产出** |
| 2 | 参数、连接或配置错误——**没有产出报告**,这一天没有结论 |

1 与 2 必须分开看:前者是「工具跑通了,结论是不一致」,后者是「工具没跑成」。
把一次连不上库记成一天的差异,会让 14 天的计数变得没有意义。

## 报告怎么读

第一行就是结论。之后依次是统计、**口径错误**、未对上的格、下一步清单。

**口径错误排在差异前面**,这是有意的:币种或切日偏移对不上意味着两边根本没在量
同一个东西,此时下面那些差额都是没有意义的数字——先修这个,再看差异。

三类「没对上」要分清,它们的排查方向完全不同:

| 判定 | 含义 | 先查什么 |
|---|---|---|
| `differs` | 两侧都读到了,数不一样 | 走下面的口径清单 |
| `missing_on_platform` / `missing_on_soloai` | 一侧压根没有这一格 | 那天那一侧的采集跑了吗? |
| `unknown_on_platform` | 有这一格,但平台那一侧是 NULL | 那一次上游读取为什么失败? |

> **缺行不会被当成 0。** 当成 0 的话,一个采集没跑的账号会显示成「差了 123.45 美元」,
> 于是所有人去查金额,而真正的问题是那天根本没采到。同理未知也不当 0。

## 出现差异:先排口径,**不要先放宽容差**

设计稿 §9 的清单(报告里也会打印一份,出问题时不必翻文档):

- [ ] 业务日两侧都是 CST +08:00?收入与成本共用同一时间权威?
- [ ] newapi 成本用 `type=2`(仅 consume)?quota 刻度两侧一致(500000 或同为上游现值)?
- [ ] sub2api 成本用 `/v1/usage` 的 `today.actual_cost`,而不是面板的 `trend[].cost`?
- [ ] `recharge_ratio` 取的是同一时点值?平台的 `ratio_snapshot` 等于 SoloAI 当时的 `recharge_ratio`?
- [ ] 未知两侧都跳过(不写 0)?已知 0(`today==null`)两侧都写 0?
- [ ] 累计口径都是「自上次重置起每日快照之和」,而不是上游的 30 天窗口?
- [ ] 平台四桶行数恒等式成立(分桶没丢行)?

**容差旋钮只兜一种情况**:真值恰好落在半分边界,SoloAI 的 float64 除法与平台的整数
定点除法各进各的,差 1 分。除此之外把旋钮调大,只会掩盖这个工具唯一要找的东西——
而且报告会把当时的容差一起归档,事后看得出哪几天是放宽换来的。

## 归档

每次跑都会往 `docs/shadow-reports/` 写一份
`shadow-<环境>-<结束业务日>.json`,同一天重跑会覆盖(归档回答的是「那一天的结论
是什么」,不是「跑过几次」)。

**为什么落文件而不是数据库表**:这个工具的作用正是判断平台算得对不对,把结论存进
被审查的那个库里是循环论证;而且 14 天里每天要能回看、能逐日 diff、能随 PR 被人
看见——git 里的 JSON 天然满足。详见 `docs/shadow-reports/README.md`。

用 `-out ""` 可以只打印不归档(临时排查用)。**正式的 14 天倒计时期间不要用它**:
没有归档的那一天等于没有证据。

## 回退 / 停用

这是个只读工具,没有需要回退的东西。不跑它就是了。
删掉 `XM_SHADOW_SOLOAI_DSN` 之后再跑会以退出码 2 告诉你没配基准——
**不会**产出一份「零差异」的假报告。
