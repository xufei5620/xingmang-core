# C1 / C2 只读身份审计

本工具提供切换前的员工 TOTP 覆盖与历史开票 actor 对照，不执行身份迁移。生产查询未运行。当前 SQL 已用审计基线 `d407875429a7ff9c3b0b5017a4b632db6324eacf` 的真实迁移，在独立本地合成 PostgreSQL 中验证；不能把这份合成结果当成新统一分支或生产已经验收。

## C1：finance.read 员工的 TOTP 覆盖

`staff-mfa.sql` 在 REPEATABLE READ / READ ONLY 事务里统计全体持有有效 `finance.read` 的员工，以及其中启用员工的两个分母。角色重复不重复计人数，角色前后空白按 Go `strings.TrimSpace` 的 Unicode 集合处理。权限来自输入的**完整有效 role-scope map**，不能把 `admin` 名称硬当 finance.read，也不能把自定义配置与默认配置随意合并。

来源：`platform/internal/platform/localauth/resolver.go:73` 与 `store.go:99`；真实表为 `000021_staff_accounts.up.sql`、`000024_staff_totp.up.sql`。新 typed resolver 的 owner 已确认继续使用真实当前角色 / finance.read，历史账号 UUID 不变。

TOTP 已登记 = `totp_secret_ref` 非空并且 `totp_enrolled_at` 非 NULL。SQL 只输出布尔值，不输出引用本身，更不读密钥。只有待登记引用而无完成时间、只有完成时间而无引用，都不算已登记。该统计不证明引用文件可读、验证码有效、当前会话已二次验证。

输出 `staff_followup` 列明缺 TOTP、禁用、锁定、强制改密、强制登记等条件。负责人应在 D 前让仍需访问财务的员工走现有员工登录/登记流程并重新捕获；未完成者会被现有 MFA/登录策略拒绝。工具不会改角色、替人登记或为缺 MFA 设置旁路。`exit 0` 仅表示历史映射检查没有阻断，**不是 TOTP 全覆盖或切换批准**；负责人必须同时读取覆盖数字和 followup。

## C2：历史 actor 的来源与对照

运行时新身份精确为 `(INVOICE_STAFF_ORIGIN, platform staff UUID)`。开票用户仍按 `(oidc_issuer, oidc_subject)` 查找；不会按邮箱合并，也不读取本工具的 crosswalk。

`invoice-actors.sql` 只读取 invoice_users 的 ID/身份 tuple/状态/平台标记和以下聚合引用：审核、开票、上传文档、支付认定两人、冻结恢复，以及 `audit_events.actor_type='admin'` 且 actor_id 可关联 invoice UUID 的记录。非 UUID 的旧操作员、孤儿 admin actor 只输出未解决计数，要求负责人审查，不输出原始 actor 字符串；`console_assertion` 的平台 UUID 不冒充 invoice UUID。不会输出邮箱、客户抬头、密文、正文、会话或 token。

crosswalk 是负责人可核对并批准的**外部审计文件**，保存于受控切换证据目录；示例 schema 见 `crosswalk.template.json`。每项包含确切旧 issuer+subject、既有 invoice_user_id、明确 staff_id、批准证据引用。只能来自现有确定身份记录或负责人明确的归属证明，禁止按邮箱、显示名或相似用户名猜配。原输入 SHA256 写入结果。不得把交叉表放进业务库、自动读入 resolver 或据此改任何 FK/AAD。

状态含义：

| 状态 | 能证明的事实 |
| --- | --- |
| CONTINUOUS_CONSOLE_ID | 现有 tuple 已是配置的 console origin + 已存在员工 UUID，invoice_user_id 不变 |
| HISTORICAL_REBIND_RECORDED | 同一个 invoice_user_id 已有历史迁移审计记录，object_id 与旧/新 tuple 哈希全部一致；不是本工具执行了迁移 |
| ATTRIBUTION_ONLY | 负责人只能证明旧身份归属；新 runtime 不会复用此旧 tuple。明确报 LOGIN_IDENTITY_NOT_CONTINUOUS |
| UNMAPPED_HISTORICAL_ACTOR / UNKNOWN_STAFF / MIGRATION_EVIDENCE_MISMATCH | 历史 actor 未映射、员工不存在或证据冲突；负责人在 D 前解决 |
| UNCLASSIFIED_UNUSED_LEGACY_IDENTITY | 未被本次管理员操作引用的旧身份，可能是历史用户；不能猜成员工，也不能声称全部旧用户已迁移 |

历史已迁移的只读证据来自 `audit_events` 中 `identity.oidc_binding.migrated`，其 before/after 哈希是 `SHA256(issuer + '\n' + subject)`，同 `invoice/backend/internal/auth/identity_migrate.go:99`。该旧工具当时会重加密邮箱 AAD；本审计绝不调用它，不读取密文或密钥。重复/冲突 crosswalk 拒绝，前后哈希或 object_id 不符也拒绝。审计记录不是独立密码学签名，负责人仍需核实其数据库与出处可信。

## 负责人未来执行方式（本次未连接服务器）

上述 SQL、只读审计与捕获脚本、本说明及 `test_identity_audit.py` 已集成在当前仓库的 `deploy/unified/audit/`，无需另行搬移。两个 synthetic JSON 仅是示例，不能替代实际配置。

1. 负责人准备 libpq service 文件的两个**不同服务引用**，分别指平台库和开票库；密码只通过 `PGPASSFILE` / service 文件引用交给 libpq，禁止把 DSN、密码放命令行或日志。两个只读账号需能 SELECT 此处查询的准确字段。脚本不授予权限，也没有验证生产账号权限。
2. 从待部署二进制对应的默认 role map 或完整有效显式配置准备 `effective-role-map.json`，并在交接中记录其版本和来源。该文件只包含非秘密角色/权限。`INVOICE_STAFF_ORIGIN` 必须与实际准备使用的公开 origin 完全一致。
3. 准备负责人批准的 crosswalk；无记录可先空 entries 获取未映射清单。不得用合成映射替代实际归属。
4. 在批准的只读执行环境调用（以下为模板，未执行）：

```sh
python3 deploy/unified/audit/capture_metadata.py \
  --psql /usr/bin/psql \
  --platform-service platform_identity_audit \
  --invoice-service invoice_identity_audit \
  --role-map /approved-audit/effective-role-map.json \
  --crosswalk /approved-audit/crosswalk.json \
  --staff-origin https://console.example.invalid \
  --output-directory /approved-audit/new-capture-directory
```

输出目录必须新建；退出码 0=无身份映射 blocker，2=有明确 blocker，1=格式/捕获失败。脚本拒绝连接串形式的 service 参数、不打印连接失败 stderr，设置 default_transaction_read_only 与各 SQL 的 READ ONLY 事务。数据库 JSON、SQL/input 哈希、UTC 起止和退出码保存到新目录。两个数据库各自一致快照，不是跨库分布式同一事务；员工/权限或身份在捕获期间有变动时，应在变动结束后重做本次只读快照。

`capture_metadata.py` 的生产连接流程尚未执行；本地真实 SQL 验证通过 docker exec/Unix socket 完成，没有读真实 service/密码文件。合成 runner 位于当前外部证据目录，含固定 Windows 工具路径，**不要作为生产部署脚本移入仓库**。

## 本地验证边界

见本轮外部证据 [RESULT.json](G:/xingmang/logs/unified-deploy-endpoint-20260912/identity-audit/RESULT.json) 与 [RESULT.md](G:/xingmang/logs/unified-deploy-endpoint-20260912/identity-audit/RESULT.md)：18 个相关单元测试、55+32 份真实迁移、双库只读 SQL、前后合成身份/actor 指纹相同、历史 witness、3 个 SQL 行为变异、5 个 Python 行为变异。所有临时数据库容器已清理。没有应用全套 CI、没有生产查询、没有 secret 内容读取、没有新业务 schema、没有改旧分支或 Peirce 工作树。后续集成源码与新 HEAD 的验证由主线程负责。
