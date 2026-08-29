# Codex 冲刺指令(2026-08-29 中午,替代之前口头队列;常驻工作方式仍按 CODEX-PROMPT.md)

## 一、现状同步(已发生的事,别重做)
- 你的 19 个 PR(#101~#119)**全部处理完毕**:实现类 #101/102/103/104/105/109/119 已合入
  release 并部署;12 份规格全部批准(其中 #111/#113/#116/#117 由产品负责人亲自拍板,
  **#113 审计归档的存储商定为自托管 MinIO(零付费,Object Lock)**,签名密钥离线保管替代 KMS)。
- release 现在是 `6bfe049`(含你的 C001~C003、B001/B002A/B004、AUD1 与全部规格)。
- GitHub Actions 因 Free 额度用尽已整体停摆;**正在切换为服务器为中心的工作流**
  (设计稿 `docs/superpowers/plans/2026-08-29-server-centric-workflow.md`)。
- 流程纠偏已生效(交接文档第八节):切片为审查单位、规格先批后做且待批≤2 份、
  审批评论必须人写、C004 未闭环。

## 二、过渡期规则(DEPLOY0 落地前,立即生效)
1. **不再需要开 GitHub PR**:验收线与你在同一台机器,直接审你的本地分支。
   每片仍是独立分支 `ai/codex/XM-…`(基于 `release/v0.1-launch`),**Handoff 改为
   分支内文件** `docs/handoffs/slices/XM-….md`(内容同原交接字段:status/branch/commit/
   summary/files_changed/tests_run/not_run/risks/follow_ups + 格→数据源映射表);
2. **门禁自己跑、结果写进 Handoff**:typecheck / test / storybook build / go fmt+vet+
   `go test -p 1 ./...` / check-governance,全绿才把分支标 READY(在 Handoff 的 status);
3. 能推 GitHub 就顺手推一下分支(镜像备份,失败不阻塞,不要重试超过一次);
4. LOCAL 分支继续保留为你的集成线,但**只有拆出的切片分支会被合入**。

## 三、任务队列(严格按序,一片一分支;2026-08-29 14:00 按你的汇报调整顺序)
0. **先收尾当前 39 个未提交的 UI 改动**:按片整理成独立分支(NewAPI 概览卡片与财务
   两子页=**XM-C004**;详情页壳/筛选与日期范围/蓝图深链/组件 a11y/告警覆盖提示/dev
   proxy 各自一片或合理归并),每片补 Handoff 文件、**补跑 Go 全量门禁**后标 READY。
1. **XM-C-DEPLOY0 a→b→c**(服务器为中心工作流,设计稿
   `docs/superpowers/plans/2026-08-29-server-centric-workflow.md`);服务器 root 步骤
   脚本化交产品负责人执行。
2. **把 LOCAL 里已批规格的实现拆成切片**(基于最新 release rebase,门禁重跑):
   RUNWAY0-impl、B003a-impl(SavedView 持久化)、MAP0-impl(渠道绑定,含迁移)、
   USER0-impl(用户读 v2)、凭据/告警页签内容、审计子页。未在已批规格范围内的先补规格。
3. **CR-0003 开票用户平台隔离**(K:/发票 仓库)。
4. 已批规格实现按序:RL0 → DS0 → R210 → R215 → DBR0(需排维护窗)→ AUD2+(MinIO)。
   R213/R214 等 M4。
5. 队列空了按 CODEX-PROMPT.md 自拟任务卡。

## 四、每片交付时回报格式(给验收线)
一行:`READY <分支名> <commit> <Handoff 文件路径>`,验收线本地审读+门禁复跑后合入部署。
BLOCKED 时同样一行 + 原因写进 Handoff 置顶。

## 五、2026-08-29 晚间定稿:本地为中心(产品负责人)
- **开发与部署全部在本机**:分支+Handoff 交付 → 验收线以 `scripts/ci-local.sh`(本机
  Docker 四门禁)验收 → 合入 release → 重建本机栈(127.0.0.1:8088)。GitHub 不再是
  交付链任何环节,Actions 停用;GitHub remote 仅镜像备份(`mirror-github.sh`,失败不阻塞)。
- **服务器角色**:异地备份裸仓库(DEPLOY0-a 安装脚本,负责人择时安装)+ 最终上线时
  `deploy.sh prod`(DEPLOY0-b);服务器 staging 与 post-receive 门禁**暂不启用**。
- DEPLOY0-c 的 origin 切换脚本待服务器裸仓库就绪后再用;在此之前 origin 保持 GitHub
  仅作镜像推送。
- 队列不变:收尾 39 个 UI 文件(含 C004)→ LOCAL 已批实现拆片 → CR-0003 → 规格实现。

## 六、真实接入轨道(2026-08-29 晚,产品负责人:本地部署边测边接)

本机栈(127.0.0.1:8088)直接连生产 Sub2API/NewAPI 的**只读**接口(四道只读闸+伪GET
黑名单已就绪),凭据由负责人自配进 `deploy/compose/.env`(gitignored),平台代码零改动。

**接入顺序与前置**(负责人动作 → 验收线动作):
1. Sub2API:负责人在 Sub2API 后台用该 admin 账号做一次合规确认(否则 423),把
   `XM_SUB2API_MODE=real` + ENDPOINT/TARGET_ALLOWLIST/CREDENTIAL_REF/TOKEN 五个值填进
   .env → 验收线重启 worker,按 `docs/runbooks/SWITCH-SUB2API-REAL.md` 验证 → 影子对比
   14 天倒计时开始(`cmd/platform-shadow`);
2. NewAPI:同法按 `docs/runbooks/SWITCH-NEWAPI-REAL.md`(access token + user id);收入侧
   `XM_NEWAPI_REVENUE_DSN` 只读库账号由负责人建(runbook 有建角色 SQL);
3. reqlog:负责人提供 /root/reqlog 源码或端点清单 → Codex 核对契约 DRAFT 后实现 real
   客户端 → 本机经 SSH 隧道 127.0.0.1:9300 接入。

**Codex 任务卡 XM-REAL0(真实模式就绪包,接入前可先做)**:
a. `FinanceCollectSecrets` 真实模式 SecretProvider:按登记簿里的 CredentialRef 从 env/
   文件动态解析(b 片 Handoff 记录的缺口,不接通则成本采集 real 模式起不来);
b. Sub2API 余额解析器:接入后由验收线抓一份脱敏的 `/admin/accounts` 真实响应样本存
   `docs/evidence/`,Codex 据此实现 `UpstreamBalance`(d 片明确 not_supported 待样本);
c. NewAPI 余额:核对上游是否开 `CHANNEL_UPDATE_FREQUENCY` 并能读到刷新时间戳,
   否则保持 not_supported 并在 UI 说明;
d. 接入验证清单脚本 `scripts/verify-real-mode.sh`:worker 日志 metrics_failed=0、
   演示横幅消失、来源 instance id、新鲜度、finance 采集 rows_written>0。
真实数据一到,所有"未接入/覆盖不全"的格会逐个变实,UI 对齐工作在真数据上继续验收。
