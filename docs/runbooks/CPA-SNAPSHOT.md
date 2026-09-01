# Runbook：CPA 一致性快照

## 目的

平台不再把 cpa-manager-plus 的活动 WAL 目录挂进 API/worker。宿主机上的
`cpa-snapshot` 用 SQLite online backup 生成独立、原子发布的只读副本；平台
只消费该副本。该工具是版本化 Platform Lifecycle Operation，不进入用户请求
路径，也不读取 CPA `auths` 或 `config.yaml`。

固定路径：

```text
source    /root/cpa-stack/cpam-data/usage.sqlite
binary    /opt/xingmang/cpa-snapshot/current/cpa-snapshot
published /var/lib/xingmang/cpa-snapshot/published/usage.sqlite
history   /var/lib/xingmang/cpa-snapshot/history/<generation>.sqlite（最多 2 代）
units     xingmang-cpa-snapshot.service / .timer
```

## 发布纪律

1. 本机完整门禁和 gitleaks 均 exit 0 后才 push。
2. 服务器只运行既有 `deploy-local.sh`；Go 二进制在 Docker builder 中构建，
   从精确 migrate 容器提取，服务器不安装开发工具。
3. `production + XM_CPA_MODE=file` 时部署脚本在启动 API/worker 前：
   - 验证二进制内嵌 commit 等于目标 SHA；
   - 安装版本化 binary 和受控 systemd units；
   - 同步运行一次 oneshot，要求首代快照成功；
   - 用同一 binary `verify` 复核 metadata、DELETE journal、必需 schema 与
     `quick_check`；
   - API/worker mount 与四条同代观测验证通过后才启用 timer。
4. 任一步失败即 `DEPLOY LOCAL FAIL`，API/worker 不启动。

## 一致性与权限

- 源连接固定 `mode=ro` + `query_only=ON`。宿主进程允许 SQLite 创建/更新 SHM
  协调文件，但不得对源数据库执行 DML、DDL、checkpoint 或 journal-mode 修改。
- online backup 使用显式 `BackupInit`、`Step`、`Close`，两个退出状态都必须
  成功。
- 临时库写入唯一 generation 和 captured time，转换为 DELETE journal，要求
  `quick_check=ok`、必需 CPA 表列齐全且没有 WAL/SHM/journal。
- 临时文件与 current 位于同一目录；替换前把已验证 current 按 generation
  硬链接进 root-only history 并 fsync；新文件 fsync 后 `rename`，再 fsync
  目录。rename 前失败不会覆盖 current 或更老 history；成功后只保留最近 2 代。
- published 目录 `root:10001 0750`，数据库 `root:10001 0640`；API/worker 以
  UID/GID 10001 从只读 bind 读取。

## 状态核验

```bash
systemctl status xingmang-cpa-snapshot.service --no-pager
systemctl status xingmang-cpa-snapshot.timer --no-pager
systemctl list-timers xingmang-cpa-snapshot.timer --no-pager

/opt/xingmang/cpa-snapshot/current/cpa-snapshot version
/opt/xingmang/cpa-snapshot/current/cpa-snapshot verify

stat -c '%U:%G %a %s %n' \
  /var/lib/xingmang/cpa-snapshot/published \
  /var/lib/xingmang/cpa-snapshot/published/usage.sqlite

docker inspect xingmang-launch-platform-api-1 \
  --format '{{range .Mounts}}{{println .Source "->" .Destination "RW=" .RW}}{{end}}'
docker inspect xingmang-launch-platform-worker-1 \
  --format '{{range .Mounts}}{{println .Source "->" .Destination "RW=" .RW}}{{end}}'
```

两个容器必须只看到 `published -> /var/lib/xm/cpa RW=false`；输出中不得出现
`/root/cpa-stack/cpam-data`、`auths` 或 `config.yaml`。

只读核验内嵌水位，不输出模型、key hash、账号或用量明细：

```bash
docker exec xingmang-launch-platform-worker-1 sh -c \
  'test -r /var/lib/xm/cpa/usage.sqlite && test ! -e /var/lib/xm/cpa/usage.sqlite-wal && test ! -e /var/lib/xm/cpa/usage.sqlite-shm'
```

最终验收还必须证明四条 `cpa.*` 观测同一非空 watermark、`sync_status=ok`，
`cpa.accounts.health.run_at` 非空且 `/platforms/cpa` 页面显示真实时间。

## 故障与回滚

- timer 某轮失败：保留上一代；不要删除 current，不要复制 DB/WAL/SHM
  兜底。观测会按快照 capture time 自然变 stale。
- 需紧急停用：把 `.env` 的 `XM_CPA_MODE=off` 且
  `XM_CPA_SYNC_ENABLED=false`，重新运行生产 `deploy-local.sh`，随后
  `systemctl disable --now xingmang-cpa-snapshot.timer`。
- 禁止回滚到活动 WAL 直挂，禁止 `immutable=1`，禁止放宽 API/worker 对源目录
  的权限。
