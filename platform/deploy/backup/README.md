# 平台签名备份

这是获批 Platform Lifecycle Operation：冻结当前平台API/worker，导出平台库及两份迁移metadata，加密并签名，恢复原writer集合。没有任意SQL、命令hook、Compose重建、pull或上游写入。Python仅用标准库，支持3.10；哈希分块读取，不依赖 `hashlib.file_digest`。

```sh
bash platform/deploy/backup/backup.sh --config /reviewed/platform-backup.json --dry-run
bash platform/deploy/backup/backup.sh --config /reviewed/platform-backup.json
# 仅当SIGKILL/主机中断留下活动journal时，以同一原配置恢复原集合：
bash platform/deploy/backup/backup.sh --config /reviewed/platform-backup.json --recover
```

所有参数都在单一严格JSON中。以下容器值须取实际完整ID/image/project，示例占位不能执行：

```json
{
  "schema": "xingmang.platform-backup/v1",
  "mode": "production",
  "docker": {"binary": "/usr/bin/docker", "endpoint": "unix:///var/run/docker.sock", "config_dir": "/reviewed/empty-docker-config"},
  "tools": {"age": "/usr/bin/age", "ssh_keygen": "/usr/bin/ssh-keygen"},
  "database": {"container_id": "FULL_64_HEX_PG_ID", "image_id": "sha256:EXACT_PG_IMAGE_ID", "project": "xingmang-launch", "service": "postgres", "user": "xingmang", "database": "xingmang", "socket": "/var/run/postgresql", "port": 5432},
  "writers": {
    "platform-api": {"container_id": "FULL_64_HEX_API_ID", "image_id": "sha256:EXACT_API_IMAGE_ID"},
    "platform-worker": {"container_id": "FULL_64_HEX_WORKER_ID", "image_id": "sha256:EXACT_WORKER_IMAGE_ID"}
  },
  "backup_dir": "/root/xingmang-backup/backups/platform",
  "recipient_file": "/etc/xingmang-backup/platform/age-recipients.txt",
  "signing_key_file": "/root/xingmang-backup/keys/platform_backup_signing_ed25519",
  "allowed_signers_file": "/etc/xingmang-backup/platform/allowed_signers",
  "restore_identity_file": "/root/xingmang-backup/keys/platform_backup_age_identity_20260912",
  "quiesce_confirmed": true
}
```

配置本身只含公开身份和路径，不能放DSN/口令。production限root/Linux/本机Unix socket，私钥须root0400，backup_dir须已存在且root独占。独立信任锚必须只有 `platform-backup namespaces="solov-platform-backup-v1" ssh-ed25519 ...`。dry-run只核形状，不调用Docker/age或声称备份通过。Windows真实工具测试使用local-synthetic，仅允许 `xm-backup-test-` 项目，不能作为Linux权限检查证明。

冻结前验证PG/API/worker完整ID、实际image、项目/服务标签、唯一service成员、稳定健康/运行状态及实际Unix socket数据库身份；用公开challenge确认签名锚与recipient可用。活动journal在任何stop前持久化。只停止原先running的API/worker，原先停止者保持停止。任何导出/加密/签名失败均恢复原集合；原先healthy的writer还须回到healthy。PG和web保持原位。

`pg_dump --format=custom --no-owner --no-acl` 二进制直接管道给age，不落明文dump。生产密钥仅stat并交加密/签名工具消费路径，脚本不读取私钥/DSN正文。两份metadata为canonical `deploy/rehearsal-unified/restore.py` 的同名CSV：`schema-migrations.csv` 和 `river-migrations.csv`，COPY带CSV HEADER并按原顺序输出；dump前后逐字节比较，发生变化就拒绝发布。metadata tar只有这两份CSV。

最终文件为 `platform-YYYYMMDDTHHMMSSZ.postgres.dump.age`、`.metadata.tar.age`、`.sha256`、`.sha256.sig`。`.sha256`是仅含两组件的SHA256SUMS；namespace/principal固定如上。先验签和恢复writers，再将完整包发布到backup_dir；manifest最后发布，拒绝覆盖。`backup-descriptor.json` 精确提供canonical restore平台域所需八字段和两components。restore_identity_file只提供恢复descriptor路径，不证明解密成功；真实恢复演练仍是独立验收。

每次证据保存在 `.platform-backup-runs/<UTC>-<id>/`：原writer状态、逐命令UTC/native退出码/输出哈希与长度、双进程dump/age原退出码、结果及descriptor。stderr不原样泄漏。未发布的失败密文/临时签名保留在专属run目录，不冒充顶层完整包。发布先以同文件系统不覆盖link创建新目标，再仅unlink本次work原件，逐项记录publish-progress，最终文件必须单链接；manifest最后。异常留下部分发布记录，不删除既有目标或其它包。

OS锁文件常驻；正常退出关闭锁。若SIGKILL/掉电留下 `.platform-backup-active.json`，新backup会拒绝，使用原配置的 `--recover` 验证同一完整容器身份、state类型及独立writer-state-before原件的SHA/内容后恢复原writer集合，不再产备份。恢复证据写入新的recovery目录，首次失败日志不覆盖。恢复失败保留journal、返回非零。不要删除活动标记来跳过恢复；未创建任何通用“重启所有服务”路径。

生产POSIX路径在原before和active持久化后才stop；原子JSON替换后刷父目录，发布前刷文件、link后刷目标目录，再unlink本次源并刷run目录，active解除也刷目录。Windows合成验证不声称已经测过Linux掉电持久性。恢复阶段如日志盘已满，日志创建/写入失败不阻断原集合inspect/start；实际恢复继续尝试，证据失败仍返回非零，不产生backup PASS，也不凭无法记录的恢复绕过活动journal。
