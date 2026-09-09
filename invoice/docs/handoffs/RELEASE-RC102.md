# 发版材料：RC102（开票线）

- **发布线** `ai/claude/XM-INV-AUTOLOGIN`，改名提交 `eb67959`
- **三片**，都是 2026-09-06 RC101 上线当天暴露出来的问题
- **带一个迁移 `0030`，可逆**

产品负责人 2026-09-07 00:50 指示：「发」。

---

## 一、先看这三件事

**1. 这一版修的是 RC101 上线当天踩的坑，其中一个会自我兑现。**

`XM-INV-ROLLFORWARD-PERMISSIONS` 让 `roll-forward.sh` 在迁移之后自动重放
运行时角色授权。而**本版自己就带一个新建列的迁移 0030**——但那是 `ALTER TABLE
ADD COLUMN`，不是 `CREATE TABLE`，`invoice_app` 对 `admin_settings` 的既有授权
自动覆盖新列，所以理论上不需要重放。**即便如此新的 `[0b/6]` 仍会跑**，这正是
想要的：不再依赖任何人判断「这次需不需要」。

**2. 测试邮件按钮从这一版起才真的可用。** RC101 之后它对所有人返回 422，因为
CR-0006 之后管理员经控制台登录、在开票侧没有邮箱，而那道「邮箱必须已验证」的
判断早在收件人被钉成固定地址时就失去了依据。

**3. 影子评估按规则跳过。** 三片都不碰评估器、投影 worker，也不碰
`eligibility_freezes` / `balance_checkpoint_evaluations` /
`balance_carry_forward_evaluations` / `eligibility_projection_jobs`。迁移 0030
动的是 `admin_settings`。`install-economic` 同样不需要——桥函数没变。

---

## 二、三片

| 片 | 一句话 | 用户可见 |
|---|---|---|
| XM-INV-ROLLFORWARD-PERMISSIONS | roll-forward 在迁移后自动重放授权；outbox 表收掉 DELETE | 否（但防止再出现 RC101 那 15 分钟） |
| XM-INV-SMTP-TEST-VESTIGIAL-GATE | 去掉「发送测试邮件」里失去依据的邮箱判断 | 是（按钮从此可用） |
| XM-INV-SMTP-TEST-RECIPIENT-SETTING | 测试收件邮箱搬进管理后台（**迁移 0030**） | 是（设置页多一个输入框） |

---

## 三、迁移

| 迁移 | 内容 | 可逆性 |
|---|---|---|
| `0030_smtp_test_recipient.sql` | `admin_settings` 加 `smtp_test_recipient`，两条 CHECK（形状、与发件人互斥） | **可逆**（`DROP COLUMN`，两条 CHECK 随之消失） |

回滚后运行时自动退回只认环境变量 `SMTP_TEST_RECIPIENT`——**回滚前先确认它还留着
一个可用地址**，否则按钮会变成 `TEST_EMAIL_NOT_CONNECTED`。

---

## 四、门禁

- 开票后端全量 `go test -p 1 -count=1 ./...`（带 `INVOICE_TEST_DATABASE_URL`）退出 0
- 前端 `tsc --noEmit` / `vitest run`（**203 条**）/ `npm run build` 全过
- 新增断言做过**变异验证**：把「设置优先」改回只认环境变量、把旧邮箱判断加回去，
  对应测试都立刻变红
- `gitleaks` 增量干净
- 源门禁 `verify.ps1`：**一次通过，退出 0**（含真库迁移、PG15/PG18 源契约验证、Sub2API/New API 四份快照核对）

**改名**：严格传输门禁钉死在单一候选版本上，RC101→RC102 共改 76 处（字面量 68、
正则转义 6、大小写夹具 2）。按行排除了三类**历史记述**：runbook 里讲 RC101 那次
permissions 事故的两段、测试里 RC100 恢复计划文档的断言、全部交接文档。

---

## 五、上线后要做的第一件事

**点一次「发送测试邮件」。** 这一版存在的理由就是它。这套 SMTP 凭据被证明过能
发信（2026-08-25 12:31 有一次 `outcome=success` 的审计，`email_outbox` 里也有一封
`sent` 的真实发票邮件），所以链路上不应有未知数。

顺序建议：先用环境变量里那个地址点一次，验链路；通了再去设置页把收件人改成想要
的，保存，再点一次。这样第二次若失败，立刻知道是新地址的问题而不是链路问题。

---

## 六、遗留债（不在本版范围）

- 门禁脚本的错误消息里仍写着 "RC100 tag"，而值已经跟着 RC 走。8 个测试钉着那些
  文字，**不在发版当天动**；应当单独一片把措辞改成与版本无关（"the exact signed
  release tag"），以后换 RC 只改值。
- `testEmail` 的失败分支不打日志。RC101 那次诊断之所以只能靠推断，就是因为 422
  这条路径静默；本版又多了「收件人没配」这一种失败，更值得补一行。
- 生产 `.env.production` 的 `SMTP_TEST_RECIPIENT` 在管理员存过一次之后就不再生效，
  可以在再下一版摘掉（摘之前确认库里那一列非空）。
