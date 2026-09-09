# 密钥与凭据总台账

**这份文档不含任何密钥值,也永远不会含。** 它回答的是另一个问题:
**「那把钥匙在哪、叫什么名字、怎么取、怎么换」**。产品负责人不需要记住任何值,
只需要记住这份文档在哪。

值分三处存放,除此之外任何地方出现的密钥值都是事故:

| # | 固定位置 | 放什么 | 谁在用 |
|---|---|---|---|
| 一 | **平台凭据库**(服务器上的加密卷) | 平台自己要调用外部系统的凭据 | 平台 API / Worker |
| 二 | **开票主机密钥目录**(服务器) | 开票系统运行期要读的密钥与证书 | 开票 api / 十个采集代理 |
| 三 | **本机 `~/.ssh/`** | 只属于你个人的签名与身份密钥 | 发版签名、备份签名、演练解密 |

数据库里还有第四类:**被加密后存在业务表里**的值(SMTP 授权码、企业微信地址等)。
它们由第二处的字段密钥保护,不单独存放。

---

## 一、平台凭据库

- **值在哪**:服务器 Docker 卷 `xingmang-launch_xm-secrets`,平台 API 里挂成
  `/run/xm/secrets/<作用域>/<名字>`,每个文件权限 0600。
- **台账在哪**:平台库 `core.credential_ref` 表。**只存元数据**——引用、指纹、
  版本、撤销时间,一列都不许承载值本身。
- **指纹**是值的 sha256 前 16 位:够你核对「粘的是不是同一把」,短到不可能反推。
- **怎么看/怎么换**:平台管理端 → 设置 → 凭据管理。缺失的粘一次即保存,
  已配置的再粘等于轮换。**这是唯一允许写入的通道**(走 Action,有审计和署名);
  直接改服务器上的文件会绕过台账,凭据管理页会显示为缺失。

已登记的引用(按作用域):

| 引用 | 用途 |
|---|---|
| `secret://sub2api-prod/read-token` | Sub2API 管理端只读 token |
| `secret://newapi/readonly-token` | NewAPI 管理员 access token |
| `secret://newapi/revenue-db` | NewAPI 收入库只读口令 **(缺失)** |
| `secret://alerts/wecom-webhook` | 平台告警群机器人地址 |
| `secret://cards/notify-webhook` | 卡片事件推送群地址 |
| `secret://sms/notify-webhook` | 接码验证码推送群地址 |
| `secret://sms62/api-key`、`secret://hero-sms/api-key` | 两家接码供应商 |
| `secret://infini-chris/*`、`secret://infini-linfeng/*` | 两个 Infini 账号各三把(keyId / 私钥 / 回调验签) |
| `secret://console-assertion/signing-key-2026-09b` | 控制台断言签名私钥(当前在用);`-2026-09` 已于 2026-09-06 撤销,见第五节 |
| `secret://staff-totp/<账号 id>` | 员工二次验证种子 |
| `secret://archive/minio-runtime`、`secret://archive/minio-kms` | 归档存储 **(缺失)** |

---

## 二、开票主机密钥目录

- **值在哪**:`/root/invoice-system/secrets/`,53 个文件,权限 `0400`,root 独占。
  compose 把用到的挂进容器的 `/run/secrets/`。
- **没有网页台账**——这一类不进平台凭据库,因为它们是开票系统自己启动时读的
  文件,不经过平台。**这一节就是它们的台账。**

| 文件名形状 | 数量 | 是什么 |
|---|---|---|
| `invoice_field_keyring.json` | 1 | **字段加密主密钥**。库里所有密文(SMTP 授权码、企业微信地址、用户邮箱)都靠它。当前版本 `2026-08`。丢了等于这些密文全部作废 |
| `invoice_owner_database_url` / `invoice_app_database_url` | 2 | 库主 / 运行时角色的连接串 |
| `invoice_session_binding_key` | 1 | 会话绑定密钥 |
| `invoice_oidc_client_secret` | 1 | 开票在 Keycloak 的客户端口令 |
| `invoice_admin_break_glass_cidrs` | 1 | 管理端应急放行网段 |
| `invoice_pdf_scanner_capability` | 1 | PDF 扫描器能力票 |
| `keycloak_owner_db_password` | 1 | Keycloak 自己的库口令 |
| `<源>_<流>_reader_database_url` | 10 | 十个采集只读角色的连接串(两个源 × 五条流) |
| `<源>_<流>_signing_key.pem` | 10 | 十条流各自的上报签名私钥 |
| `<源>_<流>_spool_key` | 10 | 十条流各自的待发队列加密钥 |
| `<源>_cutover_key` | 2 | 两个源的切换清单加密钥 |
| `<源>_balance_snapshot_key` | 2 | 两个源的余额快照加密钥 |
| `<源>-agent_key.pem` / `_cert.pem`、`ingest_server_key.pem`、`source_agent_ca.pem` | 若干 | 采集通道的双向 TLS 材料 |

**备份提醒(每次备份脚本自己会喊)**:`invoice_field_keyring.json`、age 私钥、
十把 spool 钥、两把 cutover 钥、两把余额快照钥,**备份脚本一个都不复制**,
必须单独离线保管。

同目录旁边还有两处只读参考文件,不是密钥:

- `/root/invoice-system/trust/` —— 五份 `allowed_signers`(验 git tag、发布产物、
  异地备份、Keycloak SMTP 金丝雀)
- `/root/invoice-system/config/` —— 备份收件人、备份验签公钥、源实例与信任清单、
  **控制台断言公钥清单**、SMTP 测试收件人

---

## 三、本机 `~/.ssh/`

| 文件 | 是什么 | 什么时候用 |
|---|---|---|
| `fiberstate_ed25519` | 登录服务器 | 一直 |
| `invoice_release_signing_ed25519` (+`.pub`, `invoice_release_allowed_signers`) | **发版签名钥**,指纹 `SHA256:5MWY6RAaQcgWjBs67XzKwqB2swGgOLVLg+I8TSWGnTA`,与 git tag 签名同一把 | 每次 RC:签 tag、签 `SHA256SUMS` |
| `invoice_backup_signing_ed25519` (+`.pub`, `invoice_backup_allowed_signers`) | 备份清单签名钥,指纹 `SHA256:uq6azg4FMYswPU5n6ukxd0S8C53bVSY549eMaYxIuPw` | 每次备份(临时送到服务器内存盘,用完 shred) |
| `invoice_backup_age_identity_20260825` | **备份解密身份** | 影子评估、恢复演练(同上,用完即毁) |
| `invoice_offsite_signing_ed25519` | 异地备份签名 | 异地流程 |
| `invoice_smtp_canary_signing_ed25519` | Keycloak SMTP 金丝雀签名 | 金丝雀验证 |
| `xm_console_assertion_signing_2026-09b.seed` | 控制台断言签名私钥的种子(base64),见第五节。**权威副本在平台密钥库**,这份是备份 | 只在粘进平台凭据库时用一次 |

---

## 四、数据库里的密文

这些不是「文件」,是业务表里的列,统一由 `invoice_field_keyring.json` 加密:

| 表.列 | 内容 | 密钥版本 |
|---|---|---|
| `admin_setting_secrets.smtp_secret_ciphertext` | SMTP 授权码 | `2026-08` |
| `notice_webhook_setting.webhook_ciphertext` | 企业微信通知地址 | `2026-08` |
| `invoice_users.email_ciphertext` | 用户邮箱 | 随主密钥 |
| `oidc_authorization_flows.code_verifier` | 登录流程临时值 | 随主密钥 |

**轮换字段密钥会让所有旧密文失效**,必须先重加密再换,不能直接替换文件。

---

## 五、控制台断言签名密钥(**当前在用:`2026-09b`**)

你在开票后台做敏感操作时,开票系统不自己判断你是谁,而是让**平台控制台签一张
短期通行证**,开票系统用配对的公钥验签。私钥在平台,公钥清单在开票。

| | 位置 |
|---|---|
| 私钥 | 平台凭据库 `secret://console-assertion/signing-key-<版本>`;平台用 `XM_INVOICE_CONSOLE_ASSERTION_KEY_REF` 指向它 |
| 公钥清单 | 两个仓库各一份 `contracts/auth/console-assertion-keyring.v1.json`(必须逐字节一致),生产上是开票主机文件 `/root/invoice-system/config/console-assertion-keyring.json`,只读挂进 api,**启动时读、坏了不启动** |

**轮换顺序不能颠倒**(清单里同时只有一把有效钥时,顺序错就会把管理员锁在门外):

1. 生成新钥 → 私钥种子写进本机固定文件,**绝不打印到终端**
2. 新**公钥**加进两个仓库的清单 + 开票主机文件 → 重启开票 api(此时两把都被信任,
   平台仍用旧钥签,不受影响)
3. 新**私钥**粘进平台凭据管理(这一步只能人来做:Action 要有操作者署名)
4. 平台 `XM_INVOICE_CONSOLE_ASSERTION_KEY_REF` 指向新引用 → 重新部署平台
5. 验证一次真实的步进登录成功
6. 给旧记录补 `revoked_at`(**不删记录**,历史通行证还要能验)+ 在凭据管理里吊销旧引用

**为什么不按规格的「valid_from 设在未来、重叠 ≥1 天」**:那是给计划内轮换用的,
为的是覆盖两侧发布的时间差。2026-09-06 那次是私钥出现在截图里的应急轮换,
多等一天就是多暴露一天,所以新钥即刻生效,不锁门改由上面的顺序保证。

### 2026-09-06 那次轮换的实录(下次照着做)

| 时刻(UTC) | 做了什么 | 结果 |
|---|---|---|
| 12:59 | 生成 `2026-09b`,私钥种子写进本机文件,**从未打印** | 公钥指纹 `25c0649f1507…` |
| 13:38 | 新公钥装进开票主机清单 + 重启 api | 两把并存,平台仍用旧钥,功能无变化 |
| 13:42 | 私钥粘进凭据管理「添加新的」 | 值指纹 `16770fae…`(= 种子去尾换行的 sha256) |
| 13:51 | 平台引用切到新钥 + 重新部署 | 日志 `console_assertion_signer_loaded key_id=2026-09b`,公钥与清单逐字相同 |
| 14:24 | 开票侧首次成功兑换 | `kid=2026-09b`,零报错 |
| 14:46 | 旧记录补 `revoked_at`,重启 api | 撤销后兑换照常成功 |

**两个坑,下次别再踩**:

1. **凭据页显示的指纹 ≠ 公钥清单里的指纹**。前者是 `sha256(私钥值)` 前 16 位,
   后者是公钥材料的指纹。要核对「粘对了没有」,拿本机种子文件算
   `printf '%s' "$(cat 种子文件)" | sha256sum` 的前 16 位去比。
2. **新引用不在「平台需要的凭据」那张固定清单里**,不会自动出现一行。
   要用下面「已登记引用」区块的「**添加新的**」表单,引用名手填。

---

## 绝不做的事

1. **绝不把密钥值打印到终端。** `2026-09` 这把就是这么泄漏的——生成工具的
   一次性输出进了截图。工具自己印着「never log again」。
2. **绝不把值写进文档、提交、聊天记录或截图。** 指纹可以,值不行。
3. **绝不直接改平台凭据库的文件**绕过凭据管理页——会绕过台账与审计。
4. **绝不用旧密钥的位置去猜新密钥的位置**——以这份文档为准,过期了就来改它。
