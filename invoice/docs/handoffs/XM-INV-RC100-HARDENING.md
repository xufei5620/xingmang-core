# XM-INV-RC100-HARDENING：把 2026-09-06 那次 27 分钟中断的两个成因关掉

- **status:** implemented，未发布。**纯部署面改动**：一个 nginx 配置、一个
  roll-forward 脚本、一个新的前置闸脚本、两处 runbook、一条门禁断言。
  不改任何 Go 代码，不改数据库。
- **branch:** `ai/claude/XM-INV-AUTOLOGIN`（发布线），worktree `K:/发票/wt-XM-INV-AUTOLOGIN`。
- **依据：** RC100 上线当晚的事故（见 `docs/superpowers/plans/2026-09-05-invoice-rc100-release-recovery.md`
  的执行记录与 `docs/handoffs/ACCEPTANCE-LOG.md` 的 RC100 条目）。

## 事故回顾（两个独立成因叠在一起）

**成因一：入口代理把 api 的地址缓存死了。** `deploy/nginx/ingest-mtls.conf` 里写的是
`proxy_pass http://api:8088;`——**字面量**上游名，nginx 在 worker 启动时解析一次并
缓存到进程结束。roll-forward 的第 4 步 `docker restart api` 之后 api 换了地址，代理
仍然把每一批源数据 POST 给旧地址：`connect() failed (111: Connection refused)`，
所有采集 502，readyz 503，持续 27 分钟。

脚本里**本来就有**第 5 步「重启 ingest-proxy 以重新解析」，注释也写清了理由。它没
生效，是因为第 4 步等待 api「listening」超时后 `exit 1`，脚本在第 5 步之前就退出了。
**一个只在顺利路径上运行的恢复步骤不是恢复步骤**——而且脚本失败退出时，系统比它
运行之前更糟（代理被留在坏状态）。

**成因二：运行时钉子变更没有排空前置闸。** 见下面第二节。

## 改了什么

### 一、代理在请求时解析，而不是启动时解析一次

`deploy/nginx/ingest-mtls.conf`：

```nginx
resolver 127.0.0.11 valid=10s ipv6=off;   # Docker 内嵌 DNS，覆盖它 600s 的 TTL
resolver_timeout 5s;
...
set $ingest_upstream api;
proxy_pass http://$ingest_upstream:8088;  # 变量才会触发每次请求解析
```

**在 proxy_pass 里用变量是关键**：写字面量时 nginx 只在启动解析一次。改成变量后，
api 无论因为什么原因换了地址，代理十秒内自愈——顺序不再是唯一保障。`proxy_pass`
后面不带 URI 路径，所以请求 URI 原样透传，与改动前完全一致。

`scripts/verify-nginx-configs.ps1` 加了三条断言（缺 resolver、写回字面量上游、
proxy_pass 不走变量，任一条都让门禁红），并同步修了它原来那句只对字面量生效的替换。

### 二、代理重启改成 EXIT trap，先注册再重启 api

`deploy/roll-forward.sh`：把第 5 步包成 `restart_ingest_proxy`，在 `docker restart api`
**之前** `trap ... EXIT`，顺利路径上照常显式调用一次然后 `trap - EXIT`。于是等待超时、
或者第 4 步之后任何一处失败退出，代理都会被重启。等待窗口同时从 60 秒放宽到 120 秒
（`--since` 也放宽到 180 秒，免得早打出来的那行被窗口切掉）：冷启动的 api 在负载高的
机器上确实要超过一分钟才绑定端口。

### 三、钉子变更的前置闸：所有待发 spool 必须为空

新增 `deploy/check-pending-spools.sh`。事故的另一半是：把 Sub2API 的审计钉从
`0.1.179` 抬到 `0.2.1` 时，identities 流手里有一个**在旧钉子下封存、尚未被确认接收**
的待发批次。api 按「每个批次声明的运行时必须等于审计钉」拒收它（409），而铁律是
未确认的批次必须逐字重放、不得删除，于是那条流 fail-closed 停住。往回退钉子让它排
空了，却又把另外四条流在新钉子下封存的 spool 卡住——**钉子每变一次，就会卡住上一个
值下封存的那一批**，所以除了「先排空」没有别的出路。

脚本用 `find` 扫状态目录里所有 `pending.enc`（不写死十条路径：多一个源或多一条流时
这道闸不能悄悄漏掉）。退出码 0 = 可以改钉子；1 = 有待发批次、不要改，并把每条路径与
字节数打出来，附上「不要删 spool，走 inspect-pending 的恢复检查」。

`docs/PRODUCTION-RUNBOOK.md` 第 7 节的源升级 CAS 里，「stop/drain」从一句叮嘱变成
一道要求退出码 0 的闸；「Cutover ordering」那段补上这次的成因与两处改动。

## 文件

- 修改：`deploy/nginx/ingest-mtls.conf`、`deploy/roll-forward.sh`、
  `scripts/verify-nginx-configs.ps1`、`docs/PRODUCTION-RUNBOOK.md`
- 新增：`deploy/check-pending-spools.sh`

## 测试

- `scripts/verify-nginx-configs.ps1` 全套通过（含新加的三条断言；ingest 配置在真实
  nginx 镜像里 `nginx -t` 成功）。
- `bash -n` 两个脚本；`check-pending-spools.sh` 在临时目录上跑过两条路径：空目录回
  `PENDING-SPOOLS-EMPTY` 退出 0，放一个 `pending.enc` 后回 `PENDING-SPOOLS-PRESENT`
  退出 1 并列出路径。
- roll-forward 的 trap 只能在真实上线时验证（它要 docker 与生产容器名）；改动本身是
  把已有的第 5 步挪进 trap，逻辑没有新分支。

## 上线

这三处随下一个 RC 一起发即可，**没有单独的上线动作**：nginx 配置是 bind mount，
随 compose 重建生效；两个脚本在部署主机上从发布目录取用。下一次源升级时，
`check-pending-spools.sh` 必须先回 0。

## follow_ups

- roll-forward 的第 6 步「verify」目前只看容器数、healthz、readyz。它没有看
  ingest-proxy 的错误日志——这次 502 持续了 27 分钟而 readyz 一度是 200（identities
  还没超时）。加一条「最近 2 分钟没有 `connect() failed`」的检查会更早发现。
- `check-pending-spools.sh` 是人工闸。让 `bootstrap-sources` 自己拒绝更彻底，但它跑在
  没有挂载宿主机状态目录的 tools 容器里，要先改挂载面。
