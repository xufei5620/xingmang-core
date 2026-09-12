# 统一进程 D/E 操作包

这是 06 的可执行操作包。单元测试、合成 Docker 演练、负责人在服务器上的演练是三类独立证据；本目录存在不表示后两项已经通过。代码不调用 SSH、不拉取镜像、不读取 age 私钥正文、不运行生产备份。镜像、两个签名备份与已审配置由负责人预先 stage。

从 monorepo 根运行测试：

```sh
python3 -m unittest discover -s deploy/rehearsal-unified/tests -p 'test_*.py' -v
```

三个入口均支持 `--dry-run`。干跑只校验公开输入形状和路径，打印步骤，不运行 Docker、不生成 PASS。实际运行用独占锁、逐步 UTC/退出码与 stdout/stderr 的 SHA/字节数记录，避免把运行日志中的凭据扩散到证据。

```sh
bash deploy/rehearsal-unified/rehearse.sh --config /reviewed/rehearsal.json --dry-run
bash deploy/unified/cutover.sh FULL_COMMIT_SHA --config /reviewed/production.json --dry-run
bash deploy/unified/rollback.sh --config /reviewed/production.json --dry-run
```

公开配置契约为 `xingmang.unified.operator/v1`，顶层字段严格为 `schema,mode,state_root,docker,candidate,previous,backups,approvals,smoke_config,rehearsal,host_preflight,host_nginx`。实际配置含路径、公开 ID、镜像 SHA，不能嵌入密码、私钥、TOTP 或客户邮箱。所有路径绝对且不可经符号链接/父目录跳转。

| 对象 | 字段和要求 |
|---|---|
| `mode` | `local-synthetic`、`server-rehearsal`、`production`；合成结果不能升级成服务器证明 |
| `docker` | `binary,context,config_dir`；只接受本机 Unix socket 或 Docker Desktop named pipe，不读取 credential store |
| `candidate` | `head,manifest,manifest_sha256,migration_digest,projects,jobs,ready_url,databases`；完整 clean HEAD，所有 runtime imageId 必须匹配 manifest |
| `previous` | `projects,migration_digest,ready_urls,permission_jobs,databases,input_snapshot`；原位四项目，两个 ledger 与候选声明一致，旧 readiness 分 platform/invoice；保全快照为 `{path,sha256}` |
| 每个 project | `kind,name,compose_files,env_file,services`；service 值为 `{role,image_id}`；`candidate` 仅 unified/sources，旧为 platform/invoice/sources/idp。仅 previous project 可额外声明 `service_inputs`：精确覆盖其运行服务，每项为完整有序 `compose_files` 和同首文件父目录的 `working_dir`；见[旧输入保全](../../docs/runbooks/UNIFIED-OLD-INPUTS.md) |
| 每域 database | `{project,service,database,owner}`；仅作本机容器内 pg_restore/只读元数据查询，不接受任意 SQL |
| lifecycle job | `{project,service}`；新顺序固定 migrate、invoice-migrate、invoice-permissions；旧只重放已审 invoice permissions，不引入平台 DBR1 |
| `approvals` | `rehearsal_record,mfa_query_record,max_age_hours,minimum_free_bytes`；时限 1–24h，D/C1 结果有时区、未过期、非未来 |
| C1 文件 | 由 audit/capture_metadata.py 生成 v2：绑定只读平台查询 SHA、有效 ADMIN_ROLE 和完整 XM_AUTH_ROLE_SCOPES；原角色与 finance.read 两闩交集、TOTP 及至少一名 login-ready 员工同时核对。生产结果不能由本地合成查询替代；详情见 audit/README.md |
| D 文件 | `status:"PASS",mode,source_head,manifest_sha256,backups,cleanup_complete:true,end_utc`；只有实际恢复、冒烟、清理完整成功才写入 |

旧 `invoice` 六角色为 invoice-api、invoice-web、invoice-postgres、pdf-scanner、clamav、ingest-proxy；`sources` 为 SUB/NEW × payments/identities/usage/credits/balances 共十个；`idp` 为 keycloak、keycloak-postgres。另 `platform` 为 api、worker、web、postgres 四角色。检查的是 18 + 4 个实际运行容器、实际 imageId、健康状态、挂载和端口，不是镜像角色数量或 `compose up` 返回值。

`backups` 仅有 platform/invoice；每项为 `manifest,signature,allowed_signers,identity_file,age_binary,ssh_keygen_binary,components`。component 项是绝对文件路径：platform 必须 database/metadata，invoice 必须 database/documents/source_state/metadata，可额外包含原 Keycloak dump。文件名沿用 `invoice-YYYYMMDDTHHMMSSZ.postgres.dump.age`、`.documents.tar.age`、`.source-state.tar.age`、`.metadata.tar.age`；platform 用对应 platform 前缀。签名 SHA256SUMS 必须覆盖精确组件集合，同域同时间戳，不能混包或加未校验文件。

固定签名锚：invoice-backup / solov-invoice-backup-v1；platform-backup / solov-platform-backup-v1。allowed_signers 仅允许对应 namespace-bound Ed25519 公钥。平台锚是本次交付的新约定，负责人必须明确授权签名公钥，不能冒认原平台已有此签名链。验证先验签，再校验所有 ciphertext SHA，最后 age 解密；身份仅由 age 和下述显式暂存工具消费路径，不输出正文。

`rehearsal` 字段为 `owner_id,projects,jobs,ready_url,smoke_config,volumes,tools_image,databases,verification_jobs,shred_binary,temporary_identity_paths,archive_tmpfs_bytes`。owner 是 32 位 UUID hex；项目和卷均以 `xm-rehearsal-` 开头，卷首次必须不存在。六必需卷：platform_database、invoice_database、documents、source_state、invoice_metadata、platform_metadata；可另加 scanner_socket、clamav_database。所有主机输入只读，生产卷不能成为冻结副本。archive 在有界 tmpfs 中经原 invoice-archive-verify 验证，再解包；memory 至少 tmpfs 上限 + 4GiB。

两库恢复后、迁移前，逐字节核签名 metadata：platform 的 schema-migrations.csv/river-migrations.csv；invoice 的 schema-migrations.csv/invoice-documents.csv/source-receiver-state.csv/invoice-eligibility-policy.csv。invoice 另要求专属 `verify-invoice-restore` job 调用原 invoice-backup-verify 做文档解密/完整性检查。它应由本包的演练 override 使用当前 invoice-tools 定义，不能换成 echo 或空命令。

ClamAV 的原入口需要可写数据库目录。完整 D 应声明新的 `volumes.clamav_database`，并在 `readonly_input_volumes` 保留一个 `kind: scanner_signatures` 的原卷及其已核归属。程序在 finally 清理已武装后，以原卷只读挂载复制 main、daily、bytecode 三类公开签名（每类恰一个 `.cvd` 或 `.cld`），逐文件比较 SHA、UID/GID、权限、mtime 和长度。ClamAV 仅将新副本可写挂到 `/var/lib/clamav`，保留原镜像入口及 48 小时签名健康检查；不能直接把只读原卷给原入口，也不能改入口跳过检查。副本纳入同一 owner journal 和清理；配置含 scanner_socket 时，完整 D 清理八个新卷。原签名卷保留，E 继续沿用原可写卷。

清理先验证本次 owner label/原始 journal，停止本次项目，确认容器消失，再移除明确命名冻结卷。原离线 identity 不删除。需要演练暂存身份时，`temporary_identity_paths` 必须恰为 `state_root/tmpfs/<owner_id>/invoice.age-identity` 和 `platform.age-identity`，并提供绝对路径 `identity_copy_binary`（原 GNU cp）及 `shred_binary`（原 GNU shred）。`backups.identity_file` 始终保留原件路径；创建冻结卷、武装 finally 后，程序独占创建暂存目录并先记归属，再由 cp 消费路径复制，只有 trial 的 age 调用改用副本。每个副本以三次覆写、补零及 unlink 清理；资源清理失败也会继续清理本次已登记副本。空目录仅非递归删除，未知条目保留并报失败。程序不读或哈希身份正文；暂存目录名不代表 Windows 上实际挂载了 tmpfs，执行记录应如实说明介质。未启用暂存时记录数量 0；要求覆盖身份销毁的完整演练必须提供两份暂存配置和真实删除回执。任何清理失败都不能生成 D PASS。

`host_preflight` 的完整字段与合成边界见 [HOST-PREFLIGHT.md](HOST-PREFLIGHT.md)，主机 nginx 实际切换/回滚输入见 [HOST-NGINX.md](HOST-NGINX.md)。本轮新增的 D 隔离播种、真实 tmpfs 凭据清理与 E 五项公开验收见 [SEED.md](SEED.md)；D 十二步写链仍由 [SMOKE.md](SMOKE.md) 定义。E 的 `rehearsal.host_preflight` 必须描述独立预检副本，停旧前重跑实际冻结副本预检；不切流量、不共用可写数据。四项主机守卫与原 11 道开票闩继续保留，真人登录验收由负责人执行。
