# Runbook：审计链

## 日常校验

```bash
go run ./cmd/audit-verify -database "$XM_DATABASE_URL"
```

退出码：`0` 链完好；`1` 发现断链（输出断点 sequence 与类型）；`2` 参数或连接错误。

只校验某段：`-from 100 -to 200`。

## AUD1 本地归档格式核验

AUD1 只允许本地/离线格式演练，不是对象存储、备份、恢复或 production 归档：

```bash
go run ./cmd/audit-archive plan --from 1 --to 108
go run ./cmd/audit-archive export-local --to 108 --root-id '<trusted-root-uuid>'
go run ./cmd/audit-archive verify-local --manifest '<path-below-approved-local-root>'
```

运行配置通过环境和 CredentialRef/SecretProvider 注入；不要把 DSN、seed、私钥或 payload
写进命令行、日志或证据。`export-local` 拒绝 production、拒绝 `--from`，并要求现有可信
Chain Root 精确覆盖导出终点。`verify-local` 从 terminal manifest 的 exact previous key/hash
逆向走到唯一 Genesis，不扫描目录、不猜 latest。

本地 `LOCAL-ONLY/NONE` descriptor 只说明它是开发 fixture；它不满足 versioning、
SSE-KMS、Object Lock、异故障域或 provider qualification，绝不能作为 AUD2/生产证据。

## AUD2 运行时接线（manual-only）

当前只允许显式手工 seam，详见 [`docs/runbooks/AUDIT-ARCHIVE.md`](../../runbooks/AUDIT-ARCHIVE.md)。
`platform-worker` 默认不注册归档 worker/periodic；R2-10 与 DB 角色拆分未同时证明前，
任何 `scheduled` 或 production 配置都会 fail closed。MinIO fixture 必须使用独立
`xingmang-archive` Compose 项目、锁定 `VERSIONS.lock` digest、loopback 临时端口和
仓库外 CredentialRef 装配文件；不与 `xingmang-launch` 共用网络、卷或凭据。

## 旧流程的信任变更

历史导出 JSON 即使带 `public_key`，该字段也只是 untrusted hint，不能自证。验证必须从独立
trusted keyring 取得 `chain_root_signing` / `audit-chain-root/v1` 公钥并核对 fingerprint 与
有效期。AUD1 起不再回写或读取 `export_target/exported_at`，它们不能表示“已归档”。

## 恢复演练（规格 §4.4 要求）

1. 从备份恢复数据库到演练环境；
2. 跑 `audit-verify` 全链校验——**必须 exit 0**；
3. 取最近一次库外 `ChainRootRefV1`，用独立 trusted keyring 的精确 purpose/protocol 公钥验签；
4. 核对链根的 `root_hash` 与恢复后链尖的 `event_hash` 是否一致；
5. 把演练结果记入 `docs/evidence/`。

第 3 步的 leaf 与 keyring 必须都位于原 PostgreSQL 之外，且 keyring 不能来自 leaf 或
archive bucket；artifact 自带 key 的自认证不证明任何事。

## 发现断链时

**这是事故，不是 bug。**

1. **先保全现场**：不要改动数据库、不要重启、不要跑任何写操作；
2. 记录 `audit-verify` 的完整输出（断点 sequence 与类型）；
3. 用最近一次**库外**链根定位篡改窗口：根覆盖到 `to_sequence`，
   若断点在其之前，说明连已签名的历史都被动过；
4. 查 `action.action_run` 与访问日志，找同期的可疑操作；
5. 检查数据库账号权限：谁有能力 `DISABLE RULE`？
6. 事后必须补：应用账号 `REVOKE DELETE, UPDATE`，并缩小 DBA 权限范围。

## audit_write_failed：审计缺口

日志里出现 `error_code=audit_write_failed`（来自 `module=action`）意味着：
**一次业务变更已经生效，但它的审计事件没写进链**。这是事故，不是告警噪音。

内核不会因此把动作报成失败——业务写已经提交，回滚不了，报失败只会让调用方
重试从而制造重复变更（见 `docs/modules/action/README.md`「已知缺口」）。

处置：

1. 从日志里取 `action_run_id` 与 `request_id`，在 `action.action_run` 里查到
   这次执行的完整记录——ActionRun 与审计事件是两次独立写入，前者通常还在：

   ```sql
   SELECT * FROM action.action_run WHERE id = '<action_run_id>';
   ```

2. 判断审计库为什么写不进去（连接耗尽？磁盘满？append-only 规则被改动？）。
   先恢复审计库可用性，再继续第 3 步。
3. **不要手工补插审计事件。** `audit_event` 是 append-only 的，补插会让
   `sequence` 与真实时序不符，且新事件的 `prev_hash` 已经指向了后来的链尾——
   补插只会制造一条看起来完整、实则时序错乱的链。正确做法是把这次缺口
   记进事故记录，并在下一次链根签名的说明里标注缺失区间。
4. 缺口窗口内的变更用 `action.action_run` 重建审计视图，作为事故报告附件。

## 密钥轮换

1. 生成新的 32 字节种子，经 SOPS 存入 `deploy/secrets/<env>.enc.yaml`；
2. 用新 `key_id` 生成一次链根（旧根仍可用旧公钥验证——`key_id` 就是为此存在）；
3. 把新公钥随导出文件分发给核验方；
4. 轮换记录独立保存（规格 §4.4：审计签名密钥轮换记录独立保存）。

**不要**用新密钥重签旧根：那会让「谁在什么时候签的」变得不可考。

## 常见问题

| 现象 | 处置 |
|---|---|
| `审计链为空，无需校验` | 还没有任何审计事件；不是错误 |
| `拒绝为已损坏的链签名` | 先按上面的事故流程处理，修好前不要再签 |
| `自上个链根以来没有新事件` | 正常：不需要重复签同一段 |
| 从备份恢复后校验失败 | 检查恢复是否完整；若断点恰在备份边界，可能是备份本身不完整 |
