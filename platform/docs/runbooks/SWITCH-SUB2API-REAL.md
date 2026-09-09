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

## 方式 A:你在服务器自配(推荐,key 不经过 AI)

编辑 `deploy/compose/.env`(或你的部署环境变量),加:

```dotenv
XM_SUB2API_MODE=real
XM_SUB2API_ENDPOINT=https://<你的实例>
XM_SUB2API_TARGET_ALLOWLIST=<实例主机名>
XM_SUB2API_CREDENTIAL_REF=secret://sub2api-prod/read-token
XM_SUB2API_TOKEN=<你的 admin x-api-key>
```

`.env` 已被 gitignore(根 `.gitignore` 的 `.env`/`.env.*`),不会入库。重启 worker:

```bash
docker compose -p xingmang-launch -f deploy/compose/launch.yaml --env-file deploy/compose/.env up -d platform-worker
```

## 方式 B:本机演示(你把 endpoint+key 贴给我,我配本机栈验证)

我会写进本机 `.env`(gitignored)、重启 worker、帮你验证数据对不对。key 会进本机
文件但不入库、不进任何提交。适合先跑通看效果。

## 验证(切换后 5 分钟内)

1. **worker 日志**看**本轮生效模式**,不是启动缺省:
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

把 `XM_SUB2API_MODE` 改回 `fake` 重启 worker 即可,历史样本保留。
