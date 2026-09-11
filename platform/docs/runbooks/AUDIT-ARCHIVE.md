# AUD2 审计归档运行边界

## 当前状态

`release/v0.1-launch` 只提供对象存储、catalog/journal 与 MinIO qualification 的
契约基础，以及 `platform-worker` 的显式 manual-only seam。R2-10 集群租约和 DB
角色拆分尚未同时完成，因此不得注册 River 周期 scheduler，也不得把此片当作生产
归档激活证据。`XM_AUDIT_ARCHIVE_ENABLED` 默认 `false`；`scheduled` 模式和生产
环境配置会 fail closed。

## 手工 fixture

1. 在仓库外生成 operator-managed env 文件，填入 MinIO root/KMS 值；不要把值、DSN
   或签名材料写入仓库、日志或 Handoff。
2. 使用固定的独立项目、网络和卷启动 fixture：

   ```bash
   XM_ARCHIVE_CREDENTIAL_ENV_FILE=/path/to/operator-managed.env \
     docker compose -p xingmang-archive -f deploy/compose/archive.yaml \
     --profile archive-fixture up -d --wait
   ```

3. 只使用 `127.0.0.1` 的 Docker 分配端口；停止时按项目清理 fixture，不触碰
   `xingmang-launch`：

   ```bash
   XM_ARCHIVE_CREDENTIAL_ENV_FILE=/path/to/operator-managed.env \
     docker compose -p xingmang-archive -f deploy/compose/archive.yaml \
     --profile archive-fixture down --volumes --remove-orphans
   ```

镜像引用必须与 `VERSIONS.lock` 的 MinIO tag/digest 完全一致。bucket 的 versioning、
Object Lock `COMPLIANCE`、3650 天保留和无 lifecycle 删除规则仍需由 qualification/
后续运维证据证明；本 fixture 不构成生产异故障域证据。

## 手工触发边界

`jobs.AuditArchiveArgs` 只携带已批准 signed envelope 的 SHA-256 摘要。调用方必须
显式注入 `ArchiveRunner`，由 runner 完成 envelope、Kill Switch、source/role、
exact VersionID、RecoveryIndex CAS 和 append-only journal 验证。当前 `NewClient`
不注册该 worker，因而普通 worker 启动不会上传、读取或排队归档对象；没有 runner 时
手工触发返回 `audit archive manual runner unavailable`。

## CredentialRef 清单

| 用途 | CredentialRef | 当前片状态 |
|---|---|---|
| MinIO object-writer | `secret://archive/minio-runtime` | 后续受控部署注入，只存引用 |
| MinIO KMS secret | `secret://archive/minio-kms` | compose/策略引用，仅值由运维装配 |
| 一次性 qualification | `secret://archive/minio-qualification` | AUD2 disposable fixture |
| 独立 security sink | `secret://archive/security-sink` | AUD3 预留，本片不读取 |

MinIO object-writer 访问引用通过 `XM_AUDIT_ARCHIVE_CREDENTIAL_REF` 注入；本片只固定
引用名称，不在仓库写入真实值。任何 CredentialRef 只代表寻址，不是凭据本身。

## 后续解锁条件

- R2-10 集群级租约/重复探测/所有权证据合入；
- DB role split 的 source/catalog/receipt 最小权限在 disposable 与目标环境验证；
- AUD3 signed PLO、Kill Switch、受限读取及独立 security sink 完成审读；
- 生产 activation 另获 lifecycle approval。满足前不得新增 scheduler、启动共享栈
  归档或声明 RPO/RTO 达标。
