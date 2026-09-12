# 历史开票身份归属：需服务器

本轮恢复审计基线 `d0adca1acf56ac717e0a4803652fe94e4ba4b267` 的离线 `identity-migrate` 工具及原测试闭包，并重新装入 `invoice-tools` 镜像。它不提供 OIDC 登录、回调、断言交换或新的业务 API。新员工身份仍是精确 `(INVOICE_STAFF_ORIGIN, platform staff UUID)`；工具名称及数据库 `oidc_*` 字段名仅描述历史数据。

**生产 `invoice_users` 核查、负责人映射批准、工具 dry-run/apply 均未执行，结果归入「需服务器」。** 本轮代理只恢复代码和定向验证；合成 PostgreSQL 的原迁移事务测试由主代理另行安排。没有把默认跳过的 DB 测试算作通过。

## 先做只读核对

负责人从确定的历史身份和员工记录核定准确两组 issuer/subject。禁止用邮箱、显示名、相似用户名猜配。完整历史 actor 核对继续使用 `invoice-actors.sql` 和 C2 crosswalk；交叉表仅是归属证据，不会自动授权登录或触发迁移。

以下是未来获准的只读命令模板，本次没有连接服务器。libpq 从负责人指定的 service/密码文件读取凭据，命令和归档不得含 DSN、密码、邮箱、密文或密钥正文。

```sh
psql -X -qAt --no-password --dbname service=invoice_identity_audit \
  -v ON_ERROR_STOP=1 \
  -v from_issuer='https://old-idp.example.invalid/realms/staff' \
  -v from_subject='11111111-1111-4111-8111-111111111111' \
  -v to_issuer='https://console.example.invalid' \
  -v to_subject='22222222-2222-4222-8222-222222222222' \
  -f deploy/unified/audit/identity-migrate-check.sql
```

查询在 `REPEATABLE READ READ ONLY` 事务中按原 tuple 匹配，输出原 invoice UUID、状态/平台标记、精确 tuple 哈希、需要重加密邮箱 AAD 的布尔值、关联数量和同 UUID 的历史迁移 witness；不读取邮箱内容、密文、会话标识或 token。正常待迁移应只有一个 active 历史管理员源行，且目标 tuple 不存在。已有目标行、客户平台行、状态异常、缺失或冲突映射均交负责人处理，不能自动并户、移动 FK 或以 dry-run 的成功代替身份归属批准。源行已消失时，仅原同 UUID + 前后哈希的迁移 witness 可支持“此前已迁移”；目标存在本身不够。

## 工具保留的边界

工具默认 dry-run：省略 `--apply`。不要写不存在的 `--dry-run` 参数。它先核对迁移集，然后在串行化事务里执行同样的匹配、唯一性、状态与解密检查，最后回滚；可能获得行锁，不能将它等同于上面的纯只读 SQL。路径参数必须是绝对路径。维护权限和合成演练通过后，负责人才能按逐笔批准输入使用它。

```sh
/usr/local/bin/invoice-identity-migrate \
  --database-url-file /run/secrets/invoice-maintenance-database-url \
  --field-keyring-file /run/secrets/field-keyring.json \
  --migrations-dir /app/migrations \
  --from-issuer https://old-idp.example.invalid/realms/staff \
  --from-subject 11111111-1111-4111-8111-111111111111 \
  --to-issuer https://console.example.invalid \
  --to-subject 22222222-2222-4222-8222-222222222222
```

只有负责人明确批准实际改写时，才在相同命令追加 `--apply --operator-id <批准操作员的 UUID>`。原防护保留：HTTPS issuer、UUID subject、源目标不能相同、apply 必须有 operator UUID；源精确匹配一行且 active、目标不得已属另一行、更新必须恰好一行、任一步失败事务回滚。没有批量模式，不会从邮箱选择用户。

apply 保留 **同一个 `invoice_users.id`**，仅改该行的 issuer/subject，使用原 keyring 将用户邮箱从 `invoice-user-email\n<old issuer>\n<old subject>` AAD 解密，再以新 tuple 的 AAD 加密；撤销该用户所有未撤销会话，保留已撤销会话原因，并写原 `identity.oidc_binding.migrated` 审计 action。历史申请、资金、抬头、审核/开票/文档 actor 等原 UUID 引用不移动；已有财务金额和文档不会被重写。其他密文字段的既有对象/UUID AAD 不改变。重复执行只在同一个 UUID 及前后 tuple 哈希有原 witness 时报告已迁移，不新增审计或再次撤销会话。

需服务器交回：只读原输出及 UTC/退出码、确认过的原 invoice UUID 与新 staff UUID 映射来源、C1 实际角色/TOTP 结果、逐笔 dry-run 原摘要；若另获 apply 授权，再交 apply/重跑幂等摘要与只读前后 FK 数量/UUID 核对。当前没有任何生产结果。
