# 旧输入保全：stage 之前完成

新树的 `platform/deploy/compose/launch.yaml` 是退役空模板，不能用于旧平台回滚，也不能作为旧输入快照。负责人必须在旧 release 原件仍然存在时保留其整个目录，并给新源码选择另外的全新目录。旧 Compose、依赖的相对配置/挂载文件及 env 原路径保持不变；保留期内禁止原目录被 checkout、解包、同步或清理覆盖。若已经覆盖，停止准备，由负责人从可信原发布包和原配置恢复，核对实际旧容器后再开始；本工具不重建旧拓扑、不生成口令。

从已审操作包执行下面的**元数据捕获**，在服务器 stage 前完成。命令不调用 Docker、不复制 env/key、不解析 env 值，不产生部署或演练 PASS。公开 Compose 会检查退役/空基底模板；env 仅按原字节计算 SHA256。输出目录须已存在，输出文件须全新。

```sh
python3 deploy/rehearsal-unified/capture-old-inputs.py \
  --config /srv/operator/plan-before-stage.json \
  --retained-root /srv/releases/old-platform \
  --retained-root /srv/releases/old-invoice \
  --retained-root /srv/operator/old-env \
  --new-source-root /srv/releases/FINAL_HEAD \
  --output /srv/operator/old-inputs-FINAL_HEAD.json
```

大写占位和路径由负责人据真实主机填写。`plan-before-stage.json` 是路径/公开镜像身份配置，至少包含 `mode,state_root,candidate.projects,previous.projects`；project 保留原五字段，只有 previous project 可按下节增加严格的 `service_inputs`。输出只含路径、SHA、原四项目角色/镜像和捕获时间。把 stdout 的 `input_snapshot` 原样写入最终配置的 `previous.input_snapshot`，例如 `{"path":"/srv/operator/old-inputs-FINAL_HEAD.json","sha256":"实际64位SHA"}`。D、E、回滚均保留同一描述；不要从新空模板补做一个“旧”快照。`new_source_root` 必须与实际执行该操作包的源码根完全相同，不能给一个别名路径规避目录分离。

保留根必须覆盖所有旧 Compose 和 env；它们不得与新源码根、候选 Compose 或操作状态/快照输出相交。候选只读复用原 env 路径是允许的，不迁移或复制其内容。现有 E5 stage 仍要求目标目录全新，禁止 stage 到旧 release。捕获允许新目录已经存在以支持本地工作树，但这不证明已经执行过正确的服务器 stage；实际准备顺序及原件保护由负责人记录。

真正 preflight 在 Docker 消费前检查快照 SHA、完整项目描述和原件 SHA，并固定读取每个旧容器的 `com.docker.compose.project.config_files` 与 `com.docker.compose.project.working_dir`。未声明 `service_inputs` 时，文件列表仍必须按顺序等于 `previous.projects[].compose_files`；声明后必须精确等于对应服务的完整列表。两种形式的工作目录都必须等于该服务首个 Compose 文件父目录，保持原相对路径基底；服务专用调用还显式传相同的 `--project-directory`。缺标签、其它工作目录或路径不匹配即拒绝，不能靠前缀、子集、同名文件或新模板推断原件。原版 resolved image/全挂载核对继续执行；元数据捕获本身不证明文件对应当前容器。若原部署使用不同于首个 Compose 父目录的额外 `--project-directory` 或已搬迁目录，此合同不会静默改写，负责人须先核实原调用和完整恢复方案。

## 同项目中不同服务使用不同原 Compose 列表

旧容器可能分批部署：同一 `xingmang-launch` 的 postgres 标签只有 `launch.yaml`，API/worker/web 使用 `launch.yaml,server-prod.yaml`。不要修改生产容器标签，也不要将文件集合合并后套给所有服务。为旧项目增加以下可选字段（完整路径以实际标签为准）：

```json
"service_inputs": {
  "postgres": {
    "compose_files": ["/srv/deploy/xingmang-platform/deploy/compose/launch.yaml"],
    "working_dir": "/srv/deploy/xingmang-platform/deploy/compose"
  },
  "platform-api": {
    "compose_files": ["/srv/deploy/xingmang-platform/deploy/compose/launch.yaml", "/srv/deploy/xingmang-platform/deploy/compose/server-prod.yaml"],
    "working_dir": "/srv/deploy/xingmang-platform/deploy/compose"
  },
  "platform-worker": {
    "compose_files": ["/srv/deploy/xingmang-platform/deploy/compose/launch.yaml", "/srv/deploy/xingmang-platform/deploy/compose/server-prod.yaml"],
    "working_dir": "/srv/deploy/xingmang-platform/deploy/compose"
  },
  "web": {
    "compose_files": ["/srv/deploy/xingmang-platform/deploy/compose/launch.yaml", "/srv/deploy/xingmang-platform/deploy/compose/server-prod.yaml"],
    "working_dir": "/srv/deploy/xingmang-platform/deploy/compose"
  }
}
```

一旦提供，键集合必须恰好覆盖该项目 `services` 中的运行服务，不自动补齐，不接受未知或重复服务；每项仅允许完整、有序、不重复的 `compose_files` 与 `working_dir`。原项目的 `env_file` 继续共用且保持原位。候选项目不接受这个字段。各专用文件仍经过原 absolute/non-link/non-hardlink、公开 Compose、非空基底、保留目录与新树分离检查。

`capture-old-inputs.py` 将项目默认文件和所有服务专用文件逐一记 SHA，保留完整服务映射。PREPARED 的 `project_files` 再记录每个服务的完整有序文件 SHA/工作目录；恢复时与已绑定原件重算比对，改服务、顺序、目录、文件字节或快照本身均拒绝。不能在事故中重做快照来给漂移输入重新背书。

库存仍先核**整个旧项目**恰有预期运行服务，不能用按服务筛选掩盖额外运行容器。逐服务容器定位、镜像、挂载、PG ledger/权限读取都消费对应原文件组。停止与恢复只按相同完整来源分组，每次明确列出本组运行服务，`up` 保留 `--no-deps --pull never`，不让 API overlay 重建仅由 base 声明的 PG。停止后仍检查整个项目没有运行写者。

`permissions` 等明确列在 `previous.permission_jobs` 的非运行 profile job 不属于旧 22 个运行容器；不用将一个已停止的 migrate/job 容器伪装为 runtime 服务。非运行 job 继续使用显式的 project 默认 `compose_files` 和 env；若任务明确指向某个 runtime 服务，则同样使用该服务的专用来源。任务侧别和服务显式列表的原校验不变，不推断一个最近运行服务的来源。

PREPARED 快照收录公开输入绑定与真实 Compose 标签。在库存读取期间原件发生变化也不能重设基准；停旧前再次核对捕获时 SHA。回滚在第一次旧数据库 `up` 前核同一个保全快照和原 Compose/env；启动旧写者前再核一次，最终原 22 容器、标签、挂载、端口、ledger、readyz 判据保留。文件缺失或漂移时失败并保留证据，不从候选包复制旧文件、不从日志拼凭据。

这些 SHA 是保留原件的一致性证据，不是机密内容相等或生产已验证的声明。P0-1 唯一旧平台 PG environment secret 仍使用同一原 env 输入；工具不读取/展示注入口令，也不声称比较了容器内部字节。旧目录和所有机密输入的备份/权限仍沿负责人已有流程，不打入公开元数据文件。
