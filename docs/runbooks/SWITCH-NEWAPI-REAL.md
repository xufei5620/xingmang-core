# 操作卡:把 NewAPI 从 Fake 切到真实数据

> 目标:让平台读你真实 NewAPI 实例的 5 个指标,开始 14 天影子对比。
> 平台侧代码已全部就绪(XM-0038 真实只读客户端)。
> 你只需配 3 个值 + 做 1 次前置。

## 前置(一次性,在 NewAPI 后台)

**生成一个管理员 access token**:用一个 role ≥ 10(管理员)的账号,在 NewAPI
后台点「生成系统访问令牌」(等价于 `GET /api/user/token`)。**这一步必须你自己
在后台点,平台发不出这个请求,也不该发**——那是个 GET 但会写库的端点,
它每被调用一次就会把旧 token 作废。

> ⚠️ **NewAPI 没有只读角色。** 平台要读的渠道/用户/日志/用量端点全部挂
> `AdminAuth()`(渠道路由还叠了 casbin 的 ChannelRead 权限),所以这把 token
> 在上游是**全权限**的。平台侧靠两层机械护栏兜住:
>
> 1. 四道只读闸——只发 GET/HEAD、拒重定向、按 allowlist 锁主机、包内无写路径;
> 2. **写端点黑名单**——NewAPI 有一批 GET 方法却会写库的端点,四道闸对它们
>    全部放行(方法是 GET、主机是同一个),只能靠黑名单机械挡住。名单里包括
>    `/api/channel/test`、`/api/channel/update_balance`、`/api/channel/fetch_models`、
>    `/api/user/token`、`/api/user/aff`、各种支付回调与 OAuth 回调。
>
> 这两层平台管得了;**凭据本身的权限平台管不了**,建议后续找 NewAPI 加只读投影。

> ⚠️ **不要把这把 token 用在别处的自动化脚本里。** 任何一次
> `GET /api/user/token` 都会静默轮换它,平台这边会立刻变成 auth 失败。

## 三个值

| 变量 | 填什么 | 例子 |
|---|---|---|
| `XM_NEWAPI_ENDPOINT` | 实例根地址(https) | `https://xm.solov.cc` |
| `XM_NEWAPI_TARGET_ALLOWLIST` | 允许连的主机(通常就是 endpoint 的主机名) | `xm.solov.cc` |
| `XM_NEWAPI_TOKEN` | 管理员 access token **明文** | `sk-...` |
| `XM_NEWAPI_CREDENTIAL_REF` | 凭据引用(固定写法,worker 用它把上面的 token 装进 SecretProvider) | `secret://newapi/readonly-token` |
| `XM_NEWAPI_MODE` | 切成 `real` | `real` |

可选第四个值:

| 变量 | 什么时候需要 |
|---|---|
| `XM_NEWAPI_USER_ID` | 只有**老版本** NewAPI 才需要:它要求 `New-Api-User` 头(管理员的用户 id)。当前上游源码里这个头已经不参与鉴权,所以默认留空;如果配好之后一直报 `auth`,而 token 又确认没错,填上管理员的用户 id 再试。 |

## 方式 A:你在服务器自配(推荐,token 不经过 AI)

编辑 `deploy/compose/.env`(或你的部署环境变量),加:

```dotenv
XM_NEWAPI_MODE=real
XM_NEWAPI_ENDPOINT=https://<你的实例>
XM_NEWAPI_TARGET_ALLOWLIST=<实例主机名>
XM_NEWAPI_CREDENTIAL_REF=secret://newapi/readonly-token
XM_NEWAPI_TOKEN=<你的管理员 access token>
```

`.env` 已被 gitignore(根 `.gitignore` 的 `.env`/`.env.*`),不会入库。重启 worker:

```bash
docker compose -p xingmang-launch -f deploy/compose/launch.yaml --env-file deploy/compose/.env up -d platform-worker
```

## 方式 B:本机演示(你把 endpoint+token 贴给我,我配本机栈验证)

我会写进本机 `.env`(gitignored)、重启 worker、帮你验证数据对不对。token 会进
本机文件但不入库、不进任何提交。适合先跑通看效果。

## 验证(切换后 5 分钟内)

1. **worker 日志**应显示 `newapi_mode":"real"` 且 `metrics_failed":0`:
   ```bash
   docker logs --since 6m xingmang-launch-platform-worker-1 | grep newapi_sync | tail -1
   ```
   - 若 `error_code":"auth"` → token 错、账号不是管理员(role < 10)、
     或者实例是老版本需要 `XM_NEWAPI_USER_ID`;
   - 若 `error_code":"forbidden_target"` → endpoint 主机与 allowlist 对不上;
     **也可能是上游把请求 301 了**(平台拒绝一切重定向),检查 endpoint 有没有
     写成带路径的形式;
   - 若 `bad_response` → endpoint 不对,或者上游的 `quota_per_unit` 被改成了 0
     (见下面「金额口径」);
   - 若 `not_supported` → 三个变量没配齐,日志里会写清缺哪个。

2. **浏览器** `http://<平台>/platforms/newapi`:
   - 概览 5 张卡的**来源**应从 `newapi-staging` 变成你的真实 instance id;
   - 数字应是你实例的真实数据;
   - 新鲜度徽章「数据新鲜」、数据时间是刚才;
   - **演示数据横幅消失**(因为来源不再是 demo 列表里的 fake 源)。

3. **金额口径核对(第一梯队)。** NewAPI 内部的钱只有 quota 一种单位,
   换算成美元的唯一口径是 `美元 = quota / quota_per_unit`,而 `quota_per_unit`
   **运行期可改**(root 在后台改选项就生效)。平台每轮现读一次,不写死。
   - 拿后台「用户额度」随便挑一个人,对一下平台上的余额合计量级;
   - 若平台报 `bad_response` 且日志指向 users/orders/models,先去后台确认
     `quota_per_unit` 不是 0——上游写选项时把解析错误丢掉了,非法值会把它置 0,
     平台此时**拒绝换算**而不是拿默认值顶上(那会产出看起来正常的错数字)。

4. **业务日边界(第一梯队)。** 平台按 UTC 切业务日,而你的 NewAPI 多半按
   本地时区展示。切换当天核对一下「今天的充值」两边差多少——差一整天的头尾
   说明时区口径不一致,要在这里对齐,不是数据错。

5. **这几个数字天生不完整,不是故障**:
   - **订阅金额恒为 0,且这条指标永远标记「部分数据」**。NewAPI 把订阅订单存在
     `subscription_orders` 表里,但**没有任何 HTTP 端点列它**(已核实)。
     能读到的只有「某个人现在有哪些订阅」,不是「这一天收了多少订阅费」。
     这条要等 DSN 直查那条线(成本/收入线),见 follow-up。
   - **订单数只含充值订单**,同样因为订阅订单列不出来。
   - **渠道余额可能大面积是「未配置」**。NewAPI 的 `channel.balance` 是**手填/
     手动刷新**的额度,只有点过「更新余额」才会有值,而那个端点是写端点、
     平台不碰。判据是 `balance_updated_time == 0` → 平台报「未配置」(而不是
     报 0,那会变成假的「没钱了」警报)。订阅型上游账号更是永远静默为空。
     有值的渠道,余额可能是几个月前刷新的——最旧的刷新时刻写在这条指标的
     水位里(`balance_oldest:<unix>`)。
   - **渠道多于 40 个时,超出的那些不算错误率**,它们会被单独标记为部分数据。
     错误率在上游没有现成端点,平台靠逐渠道两次日志 COUNT 自己算
     (`type=5 / (type=2 + type=5)`,最近 24 小时),渠道太多会把一轮同步的
     预算吃光。启用的渠道优先算。
   - **逐模型用量有最多 5 分钟延迟**:上游的 `quota_data` 表由后台每 5 分钟
     落盘一次,最近 5 分钟的用量还没入库。
   - **活跃用户是平台自定口径**:最近 30 天内登录过(NewAPI 上游没有「活跃用户」
     这个概念)。判据写在这条指标的水位里(`active:last_login_30d`)。

## 影子对比倒计时

切通那一刻起,平台开始持续记录真实指标的历史样本(每 5 分钟)。
规格 §22.3 要求:**连续 14 个自然日、其中 7 个 T+1 结算日无未解释重大差异**
才能切换 admin.solov.cc、归档 SoloAI。所以越早切通,倒计时越早开始。

## 回退

把 `XM_NEWAPI_MODE` 改回 `fake` 重启 worker 即可,历史样本保留。
生产环境不接受 `fake`(worker 会拒绝启动),生产上的回退动作是
`XM_NEWAPI_SYNC_ENABLED=false`——采集停掉,看板不会假装新鲜:
`observed_at` 不再前进,新鲜度自然降级。
