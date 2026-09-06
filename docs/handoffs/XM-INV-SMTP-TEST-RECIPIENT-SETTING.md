# XM-INV-SMTP-TEST-RECIPIENT-SETTING —— 测试收件邮箱搬进管理后台

- status: ready-for-review（未合入发布线；与 XM-INV-ROLLFORWARD-PERMISSIONS、
  XM-INV-SMTP-TEST-VESTIGIAL-GATE 一起进下一版 RC）
- branch: `ai/claude/XM-INV-SMTP-TEST-RECIPIENT-SETTING`
- base: `dedefca`（`ai/claude/XM-INV-SMTP-TEST-VESTIGIAL-GATE`）

## summary

产品负责人 2026-09-06：「为什么这个接收测试邮箱我不能自己在后台设置？」

原先「发送测试邮件」的收件人钉在宿主机环境变量 `SMTP_TEST_RECIPIENT` 里，换一个
地址要 SSH 上去改 `.env` 再重建容器。按本仓库既有的归属纪律（运营会调的东西进
后台、环境变量只留「会不会花真钱」那一类），它该在设置页里。

**钉在环境变量里原本的理由是防止开放中继**——收件人能随便填，这个按钮就等于
「用公司邮箱给任意地址发信」。搬进后台之后这条风险由三样东西接住，一样都不能少：

1. **单地址严格校验**：恰好一个裸地址，不接受 `显示名 <a@b>`、逗号分号分隔的
   多个收件人、任何空白或控制字符（CR/LF 进收件人就是邮件头注入），且只允许
   ASCII 可打印字符——`mail.ParseAddress` 会放行 `测试@example.com`，而本系统的
   投递链路没做过 SMTPUTF8 验证，放进来只会在真正发信时才炸。
2. **不得与发件人相同**：服务层给出专属错误码，**库里另有 CHECK 兜底**。
3. **改地址本身落审计**：走既有的 `admin_settings.smtp.update.*` 审计路径，与
   改发件人、改授权码同一条记录线。

外加原有的守卫一个没动：admin 角色闸、控制台断言步进、每位管理员每分钟一次、
请求体只接受空对象（`{"recipient":"…"}` 仍被 400 拒绝——**发信时永远不能指定
收件人**，能改的只有「常驻配置」）。

## 改动

**为什么加一列到 `admin_settings`，而不是像 0028 那样新建一张表**：0028 的通知
地址与 SMTP 口令生命周期无关（清口令走 `DELETE`，会连带抹掉地址），所以必须
分开。这里正相反——收件人**必须与 `smtp_from` 不同**，两者要在同一次保存里一起
校验、一起提交、共用 `admin_settings` 那把 revision 乐观锁。拆两张表就没法用一个
CHECK 表达这条互斥，也会出现「发件人已改成 A、收件人还停在 A」的中间状态。

| 层 | 改了什么 |
|---|---|
| 迁移 `0030` | `admin_settings` 加 `smtp_test_recipient TEXT NOT NULL DEFAULT ''`，两条 CHECK（形状、与发件人互斥） |
| `adminsettings` | `Settings`/`UpdateInput` 各加一个字段；新增 `NormalizeTestRecipient`/`MaskTestRecipient`（规则从 httpapi 搬来，**只留一份**）；`normalizeInput` 里做归一化与互斥校验；新增 `ErrInvalidTestRecipient`/`ErrTestRecipientConflict` |
| `postgresstore`(adminsettings/postgres.go) | **8 条语句、11 处列清单**全部同步 + `scanSettings` 加一个扫描目标 |
| `httpapi` | `effectiveTestRecipient()`；`testEmail` 用它；`updateSMTP` 收 `test_recipient`；设置响应加 `test_recipient_managed`；构造期那条硬性要求挪到端点 |
| 前端 | `types.ts`/`http-api.ts`/`mock-api.ts`/`App.tsx`：设置页 SMTP 卡片多一个输入框 |

**过渡期回退（不是「默认值」）**：迁移只能把新列建成空串——它读不到环境变量。
所以运行时是「库里那一列非空就用它，否则回退到 `SMTP_TEST_RECIPIENT`」。**任何
时刻只有一个来源生效**，管理员在页面上存一次之后环境变量就再也不参与；这和
「一个常量同时当默认值和下限」那种一物两职不是一回事（那个坑见
`XM-INV-SETTABLE-INVOICE-MINIMUM`）。环境变量可以在下一版之后单独摘掉。

**两处刻意保持不变**：

- **设置响应绝不回显收件人明文**，只回遮蔽值。这是仓库既有的不变量（有测试
  `TestTypedSMTPSettingsSecretIsWriteOnlyAndTestMailFailsClosed` 明确钉着），
  我第一版为了「编辑方便」把明文加了进去，**被这条既有测试当场拦下**。改成与
  SMTP 授权码、企业微信地址同一手法：输入框留空=保持不变，填了才覆盖。
- **构造期不再强制环境变量非空**：真正生效的值可能来自库，而它在建 Server 的
  时刻还没读。判断挪到端点——两个来源都空时 `testEmail` 返回
  `TEST_EMAIL_NOT_CONNECTED`，按钮禁用并提示去配置。这不是放宽：以前是整个
  进程起不来，现在是那一个按钮明确报错、其余功能照常，而且管理员自己就能修好。

## files_changed

- `backend/migrations/0030_smtp_test_recipient.sql`（新增）
- `backend/internal/adminsettings/{types,service,postgres,memory}.go`
- `backend/internal/adminsettings/test_recipient_test.go`（新增）
- `backend/internal/httpapi/server.go`
- `backend/internal/httpapi/smtp_test_recipient_test.go`（新增）
- `web/src/{types.ts,App.tsx}`、`web/src/lib/{http-api.ts,mock-api.ts}`
- `web/src/lib/http-api.smtp-test-recipient.test.ts`（新增）
- `docs/handoffs/XM-INV-SMTP-TEST-RECIPIENT-SETTING.md`（本文）

## tests_run

后端：

- `TestNormalizeTestRecipientRejectsEverythingButOneBareAddress`：九种畸形输入
  逐个拒绝（含 `测试@example.com` 这条——**它是既有测试先抓到我的**，第一版我把
  规则从「ASCII 可打印」换成「非空白非控制字符」，把国际化地址放了进去）。
- `TestUpdateRejectsTestRecipientEqualToSender`：大小写不同也算同一个；空串
  （未配置）不触发互斥。
- `TestSettingsTestRecipientWinsOverEnvironment`：两个地址都非空且不同，所以
  **不会恒真**。已做变异验证：把「设置优先」改回「只认环境变量」，它与
  `TestSettingsResponseNeverReturnsTheRawTestRecipient` 立刻变红。
- `TestEnvironmentRecipientStillUsedBeforeFirstSave`：过渡期兜底。
- `TestNoRecipientAnywhereFailsAtTheEndpointNotAtBoot`：两处都空时端点报错、
  一封信都不发。
- `TestSettingsResponseNeverReturnsTheRawTestRecipient`：遍历 smtp 块每个字符串
  字段，确认没有一处包含明文。
- 开票后端全量 `go test -p 1 -count=1 ./...`（带 `INVOICE_TEST_DATABASE_URL`）。

前端：`tsc --noEmit`、`vitest run`（**203 条**，新增 7 条）、`npm run build` 全过。
新增那 7 条钉的是「不传=保持不变、传空串=清空」这个区分——真值判断会把两者混成
一个，于是每保存一次 SMTP 主机就顺手把收件人抹掉。

## not_run

- 未部署。迁移 0030 要随下一版 RC 走完整两段门禁。
- 未在真实 SMTP 上端到端发过信（那要等上线后由产品负责人点一次）。

## risks

- **迁移 0030 改的是 `admin_settings`**，而 `postgres.go` 有 8 条语句列着它的列。
  漏改任何一条都会造成「库与内存不一致」（正是
  `XM-INV-SETTABLE-INVOICE-MINIMUM` 那次的形态）。本片的做法：先把 11 处列清单
  全部数出来，再用带出现次数校验的脚本一次改完，任何一处对不上就整体不写盘；
  写侧的新占位符一律**追加在末尾**，绝不重排既有编号。
- 回滚：0030 可逆（`DROP COLUMN` + 两条 CHECK 随之消失），回滚后运行时自动退回
  只认环境变量——但**回滚前要确认环境变量里还留着一个可用地址**，否则按钮会变成
  `TEST_EMAIL_NOT_CONNECTED`。

## follow_ups

- 生产 `.env.production` 里的 `SMTP_TEST_RECIPIENT` 在管理员存过一次之后就不再
  生效，可以在再下一版摘掉。摘之前先确认库里那一列非空。
- `testEmail` 的失败分支仍然不打日志（`XM-INV-SMTP-TEST-VESTIGIAL-GATE` 已登记）。
  这次多了「收件人没配」这一种失败，更值得补一行。
