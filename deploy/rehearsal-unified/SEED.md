# 隔离 D 播种与生产 E 公开验收

负责人本轮明确选择：D 使用冻结副本内的合成员工/客户，不提供真人密码或 TOTP；真实员工和客户的生产登录由负责人切换后亲自验收。合成身份、资金与资料不是生产业务金额或真人身份的验证结果。

## D 的公开配置

`rehearsal.seed` 固定五个公开字段，例如：

```json
{"network_cidr":"11.240.254.0/28","provider_ip":"11.240.254.10","api_ip":"11.240.254.11","port":18080,"admin_role":"admin"}
```

网络必须按实际 Docker 网段选择无重叠值，两个精确地址分别给 fixture provider 和 D API。该段沿用既有 local-synthetic 的独立 internal 演练网络方式，使 HTTPS 客户端和生产 SSRF 规则原样运行；不增加生产私网例外。名称由 D 的 32hex owner 派生，网络真实 `Internal`、容器真实 ID/image/owner/network 和卷真实 owner 都重新核验。不会把公网路由、host networking、通用代理或真实第三方账号接成认证夹具。

D 原 `smoke_config` 仅包含 `schema:"xingmang.unified.public-smoke/v1"`、实际 D mode、两个真实入口 `origins`、独立 loopback `connect_to` 和可选公开 `ca_file`。不含 credentials 或 expected。引擎在播种成功后取返回的 UUID/资料 ID，生成本次十二步写预检的配置。服务器 D 缺少 seed 会拒绝；本轮新的本地 D 也须启用此功能，不得以旧身份夹具的通过结果替代。

步骤保持为：验签/恢复并逐字节核原 metadata → 十源原 state 检查 → 原迁移和权限 → 在已核实的两份冻结数据库内播种 → 启动新运行服务并在同一个 300 秒预算内观察原 11 闩全绿 → 十二步写预检，包括提交、审核、开具、真实 ClamAV/qpdf 上传及双方下载 SHA。原 C1/C2 是生产只读事实，保留在播种之前，不能拿新合成员工去覆盖它们。

播种前分别实查 PostgreSQL 容器 ID、镜像、Compose project/service、owner 标签、实际数据卷、内网与卷 owner；两个目标都通过后才写任一库。实际 `pg_database.oid`、库名、socket 地址/端口及本次随机 owner/nonce marker 进入 SQL 开头的事务断言。只有 INSERT 新合成行，不修改旧用户、旧资金、原来源或原迁移账本。生产 mode、旧项目、原卷、不完整网络、错误 owner/数据库均拒绝。

恢复前和播种前均保护 `/var/run/postgresql`、其 `/run/postgresql` 别名、祖先与具体 socket：普通只读 bind 也不能覆盖这些位置。固定 Unix socket 调用清除 PGHOSTADDR/PGSERVICE 等 libpq 重定向环境，实查地址/端口必须都是 NULL。恢复与播种固定到已核定的完整容器 ID，不用一个被覆盖的 socket 或 TCP 返回值自证目标。

员工使用当前 ADMIN_ROLE、随机密码与已登记 TOTP，TOTP 文件按真实 reader 的 `staff-totp/UUID` 嵌套布局存储。两客户使用 `.invalid` 合成邮件和随机数字 subject；同一 invoice-tools 内的 HTTPS fixture provider 只提供两种登录/资料协议，不操作真实上游。合成客户在既有 source ID 下获得明确标记的 D-only wallet 消费资格、邮箱和抬头，写预检使用播种返回的资料 ID。

## 私密材料的生命周期

额外辅助容器和辅助私密卷不冒充十八个生产形状 runtime，也不冒充八个冻结数据卷。它们独立标记本次 owner，并在结果中单列。

随机密码、TOTP、fixture TLS 私钥与 SQL 输入只在 Docker local driver 的 **真实 tmpfs 卷** 中。Linux helper 实测 `statfs=tmpfs` 和目录 `uid=0/mode=0700`；服务器另以 `findmnt` 核该卷实际 mountpoint。Windows 主控只通过已核 helper ID 在内存中读取指定凭据；不在 Windows 盘写私密文件。公开 fixture CA 可以进入公开日志目录，仅挂给 D 的 `SSL_CERT_FILE`，生产认证代码与生产信任根均不改。

主 D 容器/网络清理或其他步骤失败，仍进入独立私密材料清理。helper 的 PID 1 必须仍持有原 tmpfs：先在该 mount 内执行三遍随机覆写、一遍零写、每遍 Sync、逐文件 unlink；最后才移除 helper，清除 provider 进程的内存凭据并释放最后的 tmpfs 挂载。不得先卸载再对一个新空卷宣称 shred。未知文件、符号/硬链接、失去 owner 或 holder、擦除/移除失败均记为失败，不能形成 D PASS。

日志盘满或日志写入失败不会先截断实际归属核查和擦除；清理通道尽力记录，但日志错误仍禁止 D PASS。若四遍擦除失败，保留原活 helper、guard、tmpfs 与网络供受控重试，不释放最后 holder。只有擦除成功或明确从未生成私密材料，才允许释放该挂载。

备份解密身份是另一条生命周期：原 canonical D 会清理它自己的两份暂存副本，保留 `backups.identity_file`。若该原输入是负责人临时上传的 `/dev/shm/rbk/invoice.age-identity`，外层流程须在 D 结束后另行 shred；E 会重新做预检，所以 E 前须重新从原离线身份以不展示内容的方式暂存，不能保留 D 的临时文件凑过 E。

## E 的五项验收

E 的 `smoke_config` 使用相同 public-smoke schema，mode 为实际 production/local-synthetic，不接受 credentials 等扩展键。引擎执行：

1. 两个入口 `/readyz` 的三模块及原 invoice report 全绿。
2. 客户页面和管理员页面 shell 可达，不称作已认证业务访问。
3. 六个旧认证入口返回原允许的 404/405/410。
4. 实际只读 SQL：`source_ingest_events.processing_status='dead'` 与 `eligibility_projection_jobs.status='dead'` 均为 0。后者的 dead 状态来自原迁移 0023；不清队列、不豁免已有 dead。
5. 实际只读 SQL 记录平台 schema_migrations 最高 version/dirty、开票 schema_migrations 最高 name；同时原全量 migration ledger SHA 必须与切换前 snapshot 相同。原 migration digest、permissions 校验继续执行。

每项保存 UTC、退出码及实际公开响应/查询结果。结果明确 `human_login_verified:false`，由负责人完成真人登录验收。任何失败继续触发原 E 自动回滚，不能以公开页面通过冒充真人登录通过。
