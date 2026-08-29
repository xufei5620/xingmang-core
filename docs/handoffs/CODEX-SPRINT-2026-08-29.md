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
   分支内文件** `docs/handoffs/slices/XM-….md`(内容同原 PR 描述:status/branch/commit/
   summary/files_changed/tests_run/not_run/risks/follow_ups + 格→数据源映射表);
2. **门禁自己跑、结果写进 Handoff**:typecheck / test / storybook build / go fmt+vet+
   `go test -p 1 ./...` / check-governance,全绿才把分支标 READY(在 Handoff 的 status);
3. 能推 GitHub 就顺手推一下分支(镜像备份,失败不阻塞,不要重试超过一次);
4. LOCAL 分支继续保留为你的集成线,但**只有拆出的切片分支会被合入**。

## 三、任务队列(严格按序,一片一分支)
0. **XM-C-DEPLOY0 a→b→c**(服务器为中心工作流:ci-local.sh + 裸仓库钩子 + 安装
   脚本 → deploy/promote 脚本 + 服务器 staging + Telegram → 切 origin/降级 GitHub/
   文档更新)。服务器 root 步骤脚本化,由产品负责人执行。**这是最高优先级。**
1. **把 LOCAL 里已批规格的实现拆成切片**(每片基于最新 release rebase,门禁重跑):
   RUNWAY0-impl(阈值预览+规则 UI)、B003a-impl(SavedView 持久化)、MAP0-impl
   (平台渠道↔上游账号绑定,含迁移)、USER0-impl(用户读 v2 核心)、
   平台「连接与凭据」「告警」页签内容、审计子页状态、dev proxy override。
   未在已批规格范围内的实现**先补规格再拆**。
2. **XM-C004 NewAPI 各页镜像**(A 期闭环)。
3. **CR-0003 开票用户平台隔离**(K:/发票 仓库,`docs/change-requests/CR-0003-…`)。
4. 已批规格的实现按序:RL0 → DS0 → R210 → R215 → DBR0(需产品负责人排维护窗)→
   AUD2+(按 MinIO 做 provider qualification)。R213/R214 的实现等 M4 启动,现在不做。
5. 队列空了:按 CODEX-PROMPT.md 自拟任务卡(追加进交接文档路线图)。

## 四、每片交付时回报格式(给验收线)
一行:`READY <分支名> <commit> <Handoff 文件路径>`,验收线本地审读+门禁复跑后合入部署。
BLOCKED 时同样一行 + 原因写进 Handoff 置顶。
