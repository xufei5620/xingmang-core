# XM-INV-SMTP-TEST-VESTIGIAL-GATE —— 「发送测试邮件」的邮箱判断已失去依据

- status: ready-for-review（未合入发布线；与 XM-INV-ROLLFORWARD-PERMISSIONS 一起进下一版 RC）
- branch: `ai/claude/XM-INV-SMTP-TEST-VESTIGIAL-GATE`
- base: `558fe5f`（`ai/claude/XM-INV-AUTOLOGIN`，RC101 = `f2f67e1` 之后的发布线）

## summary

RC101 上线当天，产品负责人在管理端点「发送测试邮件」，稳定收到
`current administrator email must be verified`（HTTP 422
`ADMIN_EMAIL_NOT_VERIFIED`）。**SMTP 本身是好的**：主机 smtp.qq.com:587、
发件人已配、STARTTLS 开、授权码密文 08-25 就存进库了、字段密钥版本仍是
`2026-08` 可解密、`SMTP_TEST_RECIPIENT` 已设且与发件人不同。

卡住的是一道判断，而它的依据早就没了：

1. **RC2（`3c93c04`）**：这封测试邮件发给管理员自己——`Recipient: user.Email`。
   往一个未验证的地址发信，等于把发信通道借给一个没人证明存在的收件人。
   所以「邮箱必须已验证」在当时是必要的。
2. **`6f723c4`（isolate SMTP test）**：收件人换成环境变量钉死的
   `SMTP_TEST_RECIPIENT`，并要求它与发件人不同。**那一刻起管理员的邮箱
   再没参与过这封信**——收件人、主题、正文都与它无关。判断的理由消失了，
   判断本身留了下来。
3. **CR-0006**：从「多余」变成「有害」。经平台控制台登录的管理员在这一侧
   根本没有邮箱（控制台只替操作者担保身份，不交出地址），`invoice_users`
   那行的 `email_ciphertext` 长度为 0、`email_verified` 恒为 `false`，而且
   每次登录的 `verified_email.oidc_synchronized` 都会把它再同步成 false。
   生产 `OIDC_ADMIN_LOGIN_ENABLED=false` 之后只剩这条登录路径，于是这个
   按钮对所有人永久失败。

**诊断为什么费劲（值得记）**：这条分支既不写审计也不打日志。生产 api 日志
里一行 SMTP 相关的记录都没有，`audit_events` 里今天也没有 `smtp_test` 行
（最近一条是 08-25）。只能靠「哪些分支会静默返回」逐个排除，再用库里的
`invoice_users.email_verified=f` 与 `oidc_issuer=https://console.solov.cc`
坐实。

## 改动

`backend/internal/httpapi/server.go` 的 `testEmail`：

```go
// 之前
user, err := s.productionAuth.LoadUser(r.Context(), adminID)
if err != nil || !user.EmailVerified || strings.TrimSpace(user.Email) == "" {
    writeError(w, 422, "ADMIN_EMAIL_NOT_VERIFIED", "current administrator email must be verified")

// 之后
if _, err := s.productionAuth.LoadUser(r.Context(), adminID); err != nil {
    writeError(w, 422, "ADMIN_NOT_RESOLVABLE", "current administrator could not be resolved")
```

保留「当前管理员仍能解析成一条真实用户」，去掉邮箱那两个条件，并把上面这段
代码考古写进注释——这道判断被移除的理由必须留在原地，否则以后有人看到
「测试邮件不校验管理员」会顺手加回去。

**没有动的东西**（这条路由的守卫仍然是四层）：`s.require("admin", …)` 的
角色闸、控制台断言的步进验证、每位管理员每分钟一次的限流、以及
rate_limited/failure/success 三种结局各落一条审计。收件人依旧由
`SMTP_TEST_RECIPIENT` 钉死、经 `normalizeSMTPTestRecipient` 校验成单个地址、
且必须与发件人不同——请求体只接受空对象，`{"recipient":"…"}` 会被 400 拒绝。

`ADMIN_EMAIL_NOT_VERIFIED` 这个错误码全仓库只有那一处引用，前端没有映射，
删除它不影响任何调用方。

## files_changed

- `backend/internal/httpapi/server.go`
- `backend/internal/httpapi/server_test.go`
- `docs/handoffs/XM-INV-SMTP-TEST-VESTIGIAL-GATE.md`（本文）

## tests_run

- 新增 `TestSMTPTestAllowsConsoleAdminWithoutEmail`：控制台登录形状的管理员
  （`SessionUser{}`，无邮箱、未验证）应当拿到 200，且确实投递了一封、收件人
  是那个钉死的地址。**做过变异验证**：把旧判断加回去，这个测试立刻变红并
  复现出生产上那个 422（`ADMIN_NOT_RESOLVABLE` 的响应体），证明它不是恒真。
- 新增 `TestSMTPTestStillRefusesUnresolvableAdmin`：`LoadUser` 报错时仍然
  422，且一封信都不发——钉住保留下来的那一层。
- 既有四个 SMTP 测试全部照旧通过（它们都显式设了 `EmailVerified: true`，
  本次改动不会让它们静默失效）。
- 开票后端全量 `go test -p 1 -count=1 ./...`（带 `INVOICE_TEST_DATABASE_URL`）。

## not_run

- 前端未改，未跑 `tsc`/`vitest`/`build`。
- 生产未部署：这是代码改动，要随下一版 RC 走完整的两段门禁。

## risks

- 判断从「四个条件」缩到「一个条件」，看上去像放宽鉴权。**它不是**：被去掉的
  两个条件保护的是「不要往未验证的地址发信」，而这个接口早就不往调用者的
  地址发信了。真正的鉴权面（谁能调这条路由）一个字节没动。
- 若将来有人把收件人改回「管理员自己的邮箱」，必须**同时**把邮箱校验加回来。
  `TestSMTPTestAllowsConsoleAdminWithoutEmail` 里那句
  `capture.message.Recipient == "test-recipient@example.com"` 就是为这件事
  设的绊线：改了收件人而不改校验，它会红。

## follow_ups

- **测试收件人搬进管理后台**（产品负责人 2026-09-06 当场提的：「为什么这个
  接收测试邮箱我不能自己在后台设置？」）。现在钉在环境变量里的理由是防止
  这个按钮变成开放中继——能填任意收件人就等于「用公司邮箱给任何地址发信」。
  搬进后台是合理的，但必须带齐三样：复用 `normalizeSMTPTestRecipient` 的
  单地址严格校验、保留「不得与发件人相同」、以及**改地址这个动作本身也落
  审计**。单独一片，未开工。
- 这条路由的失败分支应当至少打一行日志。这次生产诊断之所以只能靠推断，就是
  因为 422 这条路径静默。
