# D HTTP smoke 候选交接

本文件记录本轮独立候选的接口与验证来源。旧分支、工作树源码/index、数据库、服务器、旧验收证据均未改动。本轮没有启动真实服务，不能标 D PASS。

## 文件

- `smoke.py`：仅 Python 标准库，`run(config) -> dict`，以及 `--config <JSON路径> --output <报告路径>`。
- `test_smoke.py`：合同负例、内存 HTTP 协议边界夹具及 CLI/中断收口测试；没有把它们写成实际业务验收。
- 候选 SHA256：`50a24fa26a522ac9decc5a892560460f7ad727c7f624ff56d63b59fa88be6ef3`。
- 测试 SHA256：`8b2c80537b28601c707cc8224b1f3911de62e697522fa1d35d6488736ad1103c`。

## 配置接口

顶层：`schema="xingmang.unified.smoke/v1"`、`mode=local-synthetic|server-rehearsal`、`origins={admin,user}`、`ca_file`、`credentials`、`expected`。

`origins` 必须是带显式端口的 HTTPS loopback origin；服务器调用也经本机演练代理入口。CA 为公开证书文件。禁止远端 origin、自动 redirect、环境 HTTP 代理或 TLS 校验旁路。

- `credentials.staff`：`username`、`password_file`、`totp_file`；TOTP 文件为该合成员工的 Base32 种子，只在客户端运行时读取，聊天/日志不输出。必须已经注册 TOTP、完成首改密，真实密码登录必须返回 TOTP challenge。
- `credentials.sub2api`：`identifier`、`password_file`、`source_id`、`existing_profile_id`、`expected_email_file`。
- `credentials.newapi`：`identifier`、`password_file`、`source_id`。本次 NEW 登录与隔离，不额外要求 NEW 提交或抬头。
- `expected`：`minimum_available_minor`、`request_amount_minor`、可选 `required_staff_role`（默认 admin）。金额必须正整数，minimum 不能小于 request。

所有文件路径必须为存在的绝对常规文件，不能为符号链接；POSIX 上凭据需 0600/0400 等 owner-only 权限。Windows ACL 由演练 engine 建立并核对。配置只传命名凭据路径，不嵌入密码/TOTP/收件邮箱正文。

固定旧路由清单不能由配置削减。`expected.old_routes` 不决定验收内容。

## 真实执行内容与边界

固定 `steps` 顺序（每项有 name/status/exit_code/utc_start/utc_end）：

1. `readiness.before`
2. `sub.login`
3. `new.login`
4. `source.isolation`
5. `staff.totp`
6. `staff.page`
7. `legacy.rejected`
8. `sub.submit`
9. `staff.approve-upload-download`
10. `readiness.after`

每个 readyz 要求 modules.platform/invoice_sources/invoice_projection.ready 与 invoice_ready 精确为 true。SUB/NEW 使用独立 cookie jar、明确平台登录与各自来源 ID，资金列表必须非空且不串来源/ID，两个用户 principal 必须不同。只选基线已核验且已消费、未退款、active 的钱包资金，available 不得高于 consumed，不要求 funding v2/套餐 cap/分路摘要。

SUB 抬头必须来自恢复副本已有 profile，API 返回 owner 与登录 principal 相同、email_verified=true、邮箱与命名文件一致。脚本不创建验证证明、不 SQL 插入、不调用 0038 challenge，也不声称新收件邮箱验证通过。合成备份的身份/抬头起源由 engine 单独记录；未满足即阻断。

工作人员实际 password→TOTP challenge→TOTP completion→typed staff-session，不用 dev headers、bearer 或 mock 登录。随后核原生管理页 HTTP shell 和受保护管理列表；**此项不是浏览器 DOM/视觉验收**，报告内明确标注。SUB 真实提交→审核→开始开具→确认→真实扫描上传→用户与管理员下载 SHA 比对；NEW 越权读取该申请和 PDF 必须拒绝，用户管理权限也必须拒绝。附件 issued_at 采用确认响应的服务器 updated_at 并保留精度，避免擅改业务时间规则。

## 结果与失败

顶层结果包含 schema/status/exit_code/utc_start/utc_end/mode/steps/http；成功另含 request_id/amount_minor/document_id/document_sha256。HTTP 记录仅 method/path/status/UTC，不记录 body/header、邮箱、账户名、凭据或原始异常文本。

0 仅全部步骤明确 PASS；1 实际边界/HTTP/执行失败；2 配置或必需文件输入错误。中断写 FAIL/1 后原样抛出；不能由已完成业务步骤推导最终 PASS。engine 必须同时校验 OS 退出码、status、10 项精确完整列表，且为每轮提供独立输出目录。

## 本轮实测

| 项 | UTC 起止 | 退出码 | 说明 |
|---|---|---:|---|
| 01-contract-red | 2026-09-11 17:41:38.141354 → 17:41:38.239569 | 1 | 6 个测试、42 个实际断言失败，空校验器不能挡坏响应 |
| 02-full-unit-green | 17:48:18.934765 → 17:48:19.158148 | 0 | 17 单元测试，协议边界模拟，不是真服务 |
| 03-wallet-red | 17:49:50.242854 → 17:49:50.400193 | 1 | consumed 小于 available 的响应被原脚本误接受，新增负例真实报红 |
| 04-mutations | 17:50:38.694785 → 17:50:41.495550 | 每项 1 | 14 个隔离副本有效行为变异全红，候选原字节未改 |
| 05-restored-green | 17:51:11.073198 → 17:51:11.298818 | 0 | 17 测试、19 类关键流程故障注入通过 |

变异覆盖模块 verdict、来源隔离、抬头 owner/验证/邮箱、申请金额/版本、MFA、实际 TOTP challenge、下载 SHA、固定旧路由、钱包消费上限、真实 HTTP status、中断收口。原始 stdout/stderr 与 JSON 元数据均同目录；逐项变异见 `04-mutations.json` 和 `mutations/*/result.json`。

API 路径/DTO 查自 d0adca1a 基线；同进程 staff-session/前缀按本轮约定；PDF/HTTP流程仅参考旧公开 integration-qualification 源码，没有复用旧 PASS。待新版镜像与合成恢复环境就绪，由 Locke 运行真实 HTTP，再据真实结果处理适配问题。
