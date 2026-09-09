# Codex 部署驱动开发模式(2026-08-29 晚定稿,常驻)

> 与 CODEX-PROMPT.md(六阶段工作方式)、CODEX-SPRINT-2026-08-29.md(任务队列)叠加生效。
> 核心:**本地 Docker 栈由 Codex 全权持有**;所有开发、接入、验证都在跑着的栈上做。

## 一、栈的所有权与生命周期(Codex 负责)
- 栈 = `docker compose -p xingmang-launch -f deploy/compose/launch.yaml --env-file deploy/compose/.env`
  (web 8088 / api / worker / postgres);Codex 的 UI 实时预览 8792 代理到 8088。
- **验收线每次把切片合入 release 后,会回一行 `MERGED <sha>`**;Codex 收到即执行部署:
  `git fetch && git checkout release/v0.1-launch && git pull` → `compose build <受影响服务>`
  → `up -d`(迁移随 up 自动跑)→ 探针 `/healthz` `/readyz` → 三条烟测
  (`/api/v1/services` `/metrics` `/alerts` 200)→ 回报 `DEPLOYED <sha> <烟测摘要>`。
  建议把这套固化成 `deploy/scripts/deploy-local.sh`(首个小片,含幂等演示登记)。
- 栈异常(容器退出、迁移失败、探针红)由 Codex 先诊断修复,修不了写 BLOCKED。
- 验收线不再自行重建容器;需要看效果就访问 8088/8792。

## 二、在栈上开发(每片必做)
1. 开工前确认栈健康(探针+烟测),否则先修栈;
2. 后端片:本地跑单测/集成测试后,**在栈上重建对应服务实测**(worker 日志、API 响应);
   前端片:8792 预览对着真实 8088 后端验证,不接假接口;
3. READY 前把**实机证据**写进 Handoff:截图路径(存 `docs/evidence/screens/<slice>/`)、
   关键接口响应摘要、worker 日志行;没有实机证据不标 READY。

## 三、真实接入(凭据到位后立即执行)
- 负责人把凭据填进 `deploy/compose/.env` 后通知 `CREDS <平台>`;Codex 按对应
  `docs/runbooks/SWITCH-*-REAL.md` 切换模式、重启 worker、跑 `scripts/verify-real-mode.sh`
  (XM-REAL0-d,先做),把结果写进 `docs/evidence/EV-<日期>-<平台>-real-switch.md`;
- 切真后每片在**真数据**上验收;发现契约/字段偏差先记 evidence 再改代码;
- 影子对比:切真当天起每日跑 `cmd/platform-shadow`,报告归档,连续 14 天为上线前提。

## 四、随时调整的机制
- 调整只通过 `CODEX-SPRINT-2026-08-29.md` **追加编号新节**下达(不改旧节);
  Codex **每片开工前重读该文件最新一节**并在 Handoff 首行写 `sprint-section: N` 表示已读;
- 验收线/负责人的一行指令格式:`MERGED <sha>`、`CREDS <平台>`、`PRIORITY <任务>`(插队)、
  `HOLD <任务>`(暂停)、`ROLLBACK <sha>`(栈回退到该提交);
- Codex 的一行回报格式:`READY …`、`DEPLOYED …`、`BLOCKED …`、`SWITCHED <平台> real`。
