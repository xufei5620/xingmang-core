# 操作卡:把 Sub2API 从 Fake 切到真实数据

> 目标:让平台读你真实 Sub2API 实例的 5 个指标,开始 14 天影子对比。
> 平台侧代码已全部就绪(XM-0017 真实客户端 + XM-R013 兼容 0.1.183)。
> 你只需配 3 个值 + 做 1 次前置。

## 前置(一次性,在 Sub2API 后台)

**合规确认**:用你要给平台的那个 admin 账号,在 Sub2API 后台完成一次合规确认
(等价于 `POST /admin/compliance/accept`)。**不做这一步,平台所有 admin 读请求
会返 423,同步任务会往看板写 auth 失败**——不是数据错,是读不到。

## 三个值

| 变量 | 填什么 | 例子 |
|---|---|---|
| `XM_SUB2API_ENDPOINT` | 实例根地址(https) | `https://api.solov.cc` |
| `XM_SUB2API_TARGET_ALLOWLIST` | 允许连的主机(通常就是 endpoint 的主机名) | `api.solov.cc` |
| `XM_SUB2API_TOKEN` | admin 的 x-api-key **明文** | `sk-...` |
| `XM_SUB2API_CREDENTIAL_REF` | 凭据引用(固定写法,worker 用它把上面的 token 装进 SecretProvider) | `secret://sub2api-prod/read-token` |
| `XM_SUB2API_MODE` | 切成 `real` | `real` |

> ⚠️ 你的 Sub2API 没有只读角色,这把 key 是**全权限**的。平台传输层只发
> GET/HEAD、拒重定向、按 allowlist 锁主机——机械上碰不到任何写端点(四道只读闸)。
> 但凭据本身的权限平台管不了,**建议后续找 Sub2API 加只读投影**。

## 方式 A：后台正式切换（推荐，凭据不经过 AI）

在目标环境的「设置 → 凭据管理」登记引用，再在对应平台的接入模式卡中设置
real、endpoint、target_allowlist 和 credential_ref，保存时走既有
`connector.config.set@1` Action。环境与操作者来自已登录身份，不写进参数。
下面是该 Action 的参数形状示例，实例域名须替换为已批准的真实目标；不是直接写库命令：

```json
{
  "platform": "sub2api",
  "mode": "real",
  "endpoint": "https://replace.invalid",
  "target_allowlist": "replace.invalid",
  "credential_ref": "secret://sub2api-prod/read-token"
}
```

这会更新 `core.connector_config`；worker 在后续轮次读取生效配置，不需为单纯的
后台模式修改重启。已存在的数据库行优先于 env 缺省，改 `.env` 不能覆盖它。
按此正式路径验收时，生效日志应为 real/database，版本应对应刚保存的配置。

### legacy env 缺省（仅没有该平台/环境数据库行时）

下面只为已有 env-only 环境保留：它不会创建数据库配置行，也不等于后台登记。
只有确认没有数据库行时才按 env 缺省生效，验收日志此时应是 real/env，不能要求
config_source=database。生产停止采集使用同步开关，不能用 fake 冒充真实数据。

编辑 `deploy/compose/.env`(或你的部署环境变量),加:

```dotenv
XM_SUB2API_MODE=real
XM_SUB2API_ENDPOINT=https://<你的实例>
XM_SUB2API_TARGET_ALLOWLIST=<实例主机名>
XM_SUB2API_CREDENTIAL_REF=secret://sub2api-prod/read-token
XM_SUB2API_TOKEN=<你的 admin x-api-key>
```

下面仅适用于已批准的 `xingmang-launch` 生产栈：从 monorepo 的 `platform/` 目录运行，
沿用本次部署的 `.env`（显式 `ENVIRONMENT=production`）与 `server-prod.yaml`。
生产重建不可省略 override，否则会丢失请求记录器的只读挂载。其他部署流程须沿用
其已批准的项目、base/override 和 env 参数；独立 staging 演示栈使用自己的配置。
仅修改后台动态接入配置不需要重建；确需重建 worker 时由获批操作员执行：

```bash
docker compose -p xingmang-launch -f deploy/compose/launch.yaml \
  -f deploy/compose/server-prod.yaml --env-file deploy/compose/.env up -d platform-worker
```

## 方式 B:本机演示(你把 endpoint+key 贴给我,我配本机栈验证)

我会写进本机 `.env`(gitignored)、重启 worker、帮你验证数据对不对。key 会进本机
文件但不入库、不进任何提交。适合先跑通看效果。

## 验证(切换后 5 分钟内)

1. **worker 日志**看**本轮生效模式**,不是启动缺省。以下 database 预期适用于正式后台路径；legacy 无数据库行时两处来源均应为 env：
   ```bash
   docker logs --since 6m xingmang-launch-platform-worker-1 \
     | grep -E 'connector_config_applied|"job_kind":"sub2api_sync"' | tail -3
   ```
   - `connector_config_applied` 应为 `"platform":"sub2api"`、`"mode":"real"`、
     `"config_source":"database"`,`endpoint_host` 是你填的主机;
   - 随后的 `job_completed` 应为 `"sub2api_mode":"real"`、
     `"sub2api_mode_source":"database"`、`"metrics_failed":0`。
   - **`worker_started` 里的 `sub2api_mode_default` 不用看**:那是环境变量
     缺省,后台切模式不重启容器它永远不变
     (见 `docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md` 三·1)。
   - 若 `error_code":"auth"` → 合规确认没做,或 token 错;
   - 若 `bad_response` → endpoint 不对或实例版本不在 0.1 线;
   - 若 `unavailable` → 看同一轮的 `upstream_read` 行:`elapsed_ms` 顶到
     `group_budget_ms` 且 `round_remaining_ms` 被抽干 = 上游整体变慢;
     `elapsed_ms` 远小于 `group_budget_ms` 而余量宽裕 = 那几个接口自己坏了;
   - 若 `metrics_failed` > 0 但非全失败 → 部分指标读到了,看哪条失败。

2. **浏览器** `http://<平台>/platforms/sub2api`:
   - 概览 5 张卡的**来源**应从 `sub2api-staging` 变成你的真实 instance id;
   - 数字应是你实例的真实数据;
   - 新鲜度徽章「数据新鲜」、数据时间是刚才;
   - **演示数据横幅消失**(因为来源不再是 demo 列表里的 fake 源)。

3. **收入指标特别看一眼**(这是 XM-R013 修的点):有真实充值的日子,
   `sub2api.revenue.daily` 应正常显示金额;若显示"部分数据"(IsPartial),
   说明那天的收入币种与契约默认币种不一致——见契约 v2 待办,不是故障。

## 影子对比倒计时

切通那一刻起,平台开始持续记录真实指标的历史样本(每 5 分钟)。
规格 §22.3 要求:**连续 14 个自然日、其中 7 个 T+1 结算日无未解释重大差异**
才能切换 admin.solov.cc、归档 SoloAI。所以越早切通,倒计时越早开始。

## 回退

非生产环境需要回到 fake 时，在同一后台接入卡经 `connector.config.set@1`
把 mode 改为 fake；仅在没有数据库行的 legacy 环境，才由 `XM_SUB2API_MODE=fake`
缺省生效。生产停止采集应由获批操作员设置 `XM_SUB2API_SYNC_ENABLED=false`
并沿用前文完整生产部署参数重建；保留历史数据。仅改 env MODE 不能覆盖已存在的
real 数据库行，fake 也不是生产回退方式。
