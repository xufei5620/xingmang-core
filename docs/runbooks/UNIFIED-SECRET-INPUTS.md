# 统一切换的凭据文件来源（P0-1）

新 `deploy/unified/compose.json` 的平台 PG secret 固定从 `${SECRETS_DIR}/database__postgres_password` 提供，只读挂至 `/run/secrets/database__postgres_password`。目标主机负责人应通过现有凭据保管工具把**现有平台数据库口令**准备到该绝对路径，按容器实际 UID/GID 配置宿主权限，并保留原受控环境输入。不要在 shell 参数、公开 JSON、报告或本仓库中粘贴口令；本操作器不生成、复制或读取该 secret 正文，不轮换数据库密码。

平台 API/worker/migrate 原有 `DATABASE_PASSWORD_REF` 和原 environment credential provider 不在此次替换范围。负责人须保证它们仍消费相同的现有口令；路径检查不证明内容相同，实际隔离恢复及数据库认证成功也不能冒充生产口令审计。

旧部署兼容仅限原 `previous` 的 `platform` 项目中 `platform-postgres` 角色：secret 名必须为 `database__postgres_password`，唯一来源必须是 `environment: DATABASE_PASSWORD`，目标固定为默认 `/run/secrets/database__postgres_password`，默认 root:root/0444，并由 `POSTGRES_PASSWORD_FILE` 消费。Docker Compose 对这种来源执行容器文件注入，不产生 host bind，因此本包将它记作 `compose-environment-injected`，绝不制造 bind-mount 证明。目标及父目录不得被任何 mount/tmpfs 遮蔽。

兼容记录绑定原 Compose 文件和 env 文件路径/字节 SHA，随 durable snapshot 保存；回滚启动前仍核原文件字节，恢复消费同一已冻结 env 输入。保持原环境文件和服务形式，不要求先变更生产 PG。本轮无法在不读取秘密的前提下证明既有容器中的注入内容相等，服务器负责人仍需核实其来源，不能把本地合成测试当成该事实。

此例外不适用于候选、其他角色、其他 secret/config、改名/改 target、inline content、external source 或混合来源。其他 secret/config 仍必须有可核对的文件只读 mount。若旧主机实际声明不符合这个唯一合同，preflight 会拒绝，不能加宽跳过项。

依据：[Docker secrets 来源](https://docs.docker.com/reference/compose-file/secrets/)及[Docker Compose 注入实现](https://github.com/docker/compose/blob/main/pkg/compose/secrets.go)。主机版本的实际配置和容器元数据仍需目标主机核对；本次仅本地源码与消费者测试，未启动 Docker。
