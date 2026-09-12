# 统一部署补充密钥台账

本表只记录路径与用途，不包含密钥值。其它平台/开票运行凭据仍见[原平台总台账](../../platform/docs/runbooks/SECRETS-INVENTORY.md)。

| 路径 | 用途 | 主机归属与权限 |
|---|---|---|
| `/root/xingmang-backup/keys/platform_backup_signing_ed25519` | 平台备份 Ed25519 签名；principal `platform-backup`、namespace `solov-platform-backup-v1` | root，0400；只作为ssh-keygen路径参数 |
| `/root/xingmang-backup/keys/platform_backup_age_identity_20260912` | 平台备份 age 解密身份 | root，0400；备份仅stat/记录路径，恢复时由age消费 |
| `/etc/xingmang-backup/platform/allowed_signers` | 独立批准的namespace-bound平台备份公钥信任锚 | root管理的公开配置，不随备份包自授信任 |
| `/etc/xingmang-backup/platform/age-recipients.txt` | 平台备份公开age recipients | root管理的公开配置 |

密文备份输出根建议 `/root/xingmang-backup/backups/platform`，root独占0700。两个私钥不得放在该输出根或其子目录，不复制进备份包；不要通过cat、日志、环境转储或终端回显查看正文。独立离线保管原私钥仍由负责人负责。

本地代码交付不证明服务器文件已创建或权限已核。实际元数据检查、密钥创建与服务器备份记录由本次获授权的主任务保存。
