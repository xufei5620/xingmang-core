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

## 七、持续完成模式 + 审批记录(2026-08-30 凌晨,产品负责人:「持续完成所有」)

### 7.1 持续完成规则(覆盖此前"待批≤2"的节流)
- **不停机**:队列空了立即按路线图(交接文档第四节 A→E + 第二轮 R2 四期)自拟下一张
  任务卡,追加进本文件并继续;不等验收、不等 MERGED 才开工。
- **技术决策自己下**:凡宪法/ADR/已批设计稿能推出结论的(迁移编号、字段形状、scope 名、
  fake 样本、组件边界等),按最小权限与 fail-closed 原则决定,把决定与依据写进 Handoff
  的 `decisions` 节即可,**不要为此 BLOCKED**。验收线审读时不同意会在 ACCEPTANCE-LOG 里
  写 `REJECT <分支> <理由>`,你再改。
- **只有这五类才 BLOCKED 等人**:①真实凭据/生产系统/上游三方源码;②宪法条款变更;
  ③采购/付费/外部基础设施(存储商、域名、证书);④删除既有能力或改变已上线口径;
  ⑤开票线 CR 级契约变更。其余一律自决继续。
- **验收信号改为文件通道**:验收线把每次合入追加到 `docs/handoffs/ACCEPTANCE-LOG.md`
  (格式 `<UTC时间> MERGED <sha> <分支列表>` / `REJECT <分支> <理由>`),并提交到 release。
  你每片开工前与完成后 `git fetch` 读该文件最新几行:有新的 MERGED 就先跑
  deploy-local.sh 部署并回报 `DEPLOYED`,有 REJECT 就先修。不再依赖人转述。

### 7.2 审批记录(验收线按技术授权裁定,2026-08-30)
- **RUNWAY0 迁移批准**:B003a(000015)与 MAP0(000016)已合入 release;RUNWAY0 的
  up/down 对以 release 最新基线重算编号(应为 000017)后**视为已批**,内容以你 Handoff
  的 migration review packet 为准;门禁绿即可 READY,不需要再等逐字节批准。
- **DAILY_USAGE_APPROVAL:批准**——DailyUsageReader 只读、按日粒度、复用
  `platform.users.read`,fake 完整实现,real 留骨架标注证据待补。
- **KEY_SCOPE_APPROVAL:批准**——新增 scope `platform.user_keys.read`,仅元数据
  (前缀/创建/最近使用/状态),**永不含完整 key**,admin 默认**不**带该 scope(同
  request.content.read 的最小权限先例),进 RoleScopeMap 与开发态默认清单。

### 7.3 自拟任务卡（XM-R210-2a，2026-08-30）
- **目标**：冻结 JobFleetManifestV1 / JobFleetInventoryV1 的严格离线校验与签名契约，
  为后续多副本证据提供可复验的纯函数基础。
- **涉及面**：仅 `contracts/jobs` 与 `internal/platform/jobs` 的 canonical JSON、哈希、
  Ed25519 公钥 keyring、epoch/nonce/replica/build/effective-hash 校验及测试；不接 DB、
  River worker、Compose、凭据或外部网络。
- **验收标准**：unknown/duplicate/non-canonical/过期/重放/错误 purpose-protocol-key/
  digest/build/effective/inventory 等输入 fail-closed；golden 与 Go/governance/
  gitleaks/diff 门禁全绿。该卡是**自拟、contract-only、待确认**，不解锁 R210-2 full、
  DB binding、两副本 failover、R210-3 或 R210-4。

### 7.4 自拟任务卡（XM-R213-1a，2026-08-30；原 7.3，R210-2a 先行合入后顺延）
- **目标**：冻结 CPA 管理面/推理面/回调面的 deny-by-default 边界契约，并提供只读离线
  配置与路由审计器，提前暴露通配管理路由、错误回调例外和凭据泄漏风险。
- **涉及面**：仅 `contracts/cpa`、`internal/platform/cpaboundary`、离线本地 evidence
  CLI、模板与 runbook；不连接 CPA、不读取管理 key/证书、不改网络/Compose/防火墙，目标
  版本与 route inventory 缺失时必须显示 incomplete。
- **验收标准**：严格 JSON/canonical hash、exact callback GET/POST、inference 与 management
  deny-list、mTLS/allow-remote/MANAGEMENT_PASSWORD 事实、capability 依赖与 response
  projection 全部 fail-closed；Go/governance/gitleaks/diff 全绿。该卡为**自拟、offline-only、
  待确认**，不解锁 R213-2/3/4 或任何真实 CPA 能力。

### 7.5 加速指令（2026-08-30，用户拍板：队列不变、提速）
- **预批**：DBR2/DBR3、AUD2 接线/AUD3/AUD4、RL2/RL3/R210-2/R215-2-3、DS1/DS2 无需再等 `APPROVED` 行，
  直接从最新 release 开工，验收线在合入时审读迁移/生成物/scope；五类用户门控与 M4 门不变。
- **不再出 docs-only 交接片/审批包**；Handoff 随实现片一起交。
- **部署节流**：只有含迁移或运行时行为变化的合入才跑 `deploy-local.sh`。
- **切片放大**：一个 slice = 计划中的一个完整 Task 组；迁移仍单独列文件哈希。
- **并行车道**：多个 Codex 会话时每会话只认领一条车道（A 数据库角色 / B 审计归档 / C R2 韧性 / D 指标降采样），
  共享文件只在本车道最终接线片改并 rebase 到最新 release 后再标 READY。
- 完整文本见 `docs/handoffs/ACCEPTANCE-LOG.md` 的 `PRIORITY 加速指令` 行。

### 7.6 用户拍板追加（2026-08-30 晚）
- **CR-0003 身份模型=方案一已批准**：车道 E（开票，K:/发票 仓库）可开实现分支；契约原文见 ACCEPTANCE-LOG `APPROVED CR-0003`。
- **新增车道 F：XM-CRED0 凭据管理 UI**：管理后台可输入/添加/修改/保存凭据；平台库仍只存 CredentialRef 名，
  值写入仓库外文件 SecretProvider 目录，经 `credential.upsert/rotate/revoke` Action + 审计事件；连接器 token 解析改为
  SecretProvider 优先、env 兜底；不做 KMS/审批中心。完整边界见 ACCEPTANCE-LOG `PRIORITY 新增车道 F`。
