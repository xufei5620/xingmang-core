# Runbook：reqlog-recorder 平滑切换（XM-REQLOG-MERGE）

> 目标：把生产上已经在跑的桌面端原型记录代理（`/root/reqlog/reqlogger`，
> systemd 服务）换成本仓库收编、可配置、有测试的 `cmd/reqlog-recorder`，
> 并把平台「请求详情」从 `off` 切到直读它落盘数据的 `file` 模式——不再规划
> 接入原来 `:9300` 的 HTTP 查看端。
>
> 背景与字段口径见 `contracts/connectors/reqlog.read.v1.md`；风险清单见
> `docs/handoffs/slices/XM-REQLOG-MERGE.md`。

## 前提

- 记录代理与平台是**两个独立部署**：记录代理以 `systemd` 服务跑在宿主机
  （`User=root`），平台以 Docker 容器跑（`platform-api` 只读挂载记录代理的
  输出目录）。切记录代理不需要重新部署平台栈，切平台读取模式也不需要重启
  记录代理——两步互相独立，可以分开验证。
- 落盘格式（`Record`/`FullRecord` 的字段与 JSON 标签、按天目录 + `index.jsonl`
  的布局）在收编前后**逐字段兼容**：新旧二进制写的数据可以被同一个读侧读到，
  中途切换不需要迁移历史数据。
- 宝塔 nginx 的 `0.reqlog-upstreams.conf` 配了 backup 直连降级：记录代理短暂
  停机时，nginx 会直接把请求转给 NewAPI/Sub2API，**用户侧无感知**，代价只是
  那个窗口内的请求不落盘、不可审计。第 3 步的重启窗口因此不需要挑维护窗口。

## 第 1 步：构建二进制

在有 Go 1.27 工具链的机器上（服务器本机，或任何能交叉编译 `linux/amd64` 的机器）：

```bash
cd /path/to/xingmang-platform
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o reqlog-recorder ./cmd/reqlog-recorder
```

`go build` 前建议先跑一遍本地门禁（见本仓库根 `CLAUDE.md`「常用命令」）：

```bash
go build ./cmd/reqlog-recorder/... && go vet ./cmd/reqlog-recorder/... && go test ./cmd/reqlog-recorder/...
```

## 第 2 步：放二进制

把编译产物传到服务器，放到 `/usr/local/bin/reqlog-recorder`（root 所有，可执行）：

```bash
sudo install -o root -g root -m 0755 reqlog-recorder /usr/local/bin/reqlog-recorder
```

此时旧的 `/root/reqlog/reqlogger` 单元还在跑，新二进制只是放到位，**还没生效**。

## 第 3 步：一次性放宽历史数据的属组权限

新版本新建的目录/文件默认是 `0750`/`0640` 且属组是 GID `10001`（星芒平台容器
的运行用户），但**已经存在**的历史数据是旧版本用 `0700`/`0600` 写的，只有
`root` 能读。补一次属组与权限位，否则平台切到 `file` 模式后读不到旧数据
（只能看见切换那一刻之后的新记录）：

```bash
sudo chgrp -R 10001 /root/reqlog/data
sudo chmod -R g+rX /root/reqlog/data
```

`g+rX`（大写 X）只给目录和已有执行位的文件加执行权限，不会把数据文件
变成可执行——这是标准的"批量放开同组读权限"写法。这一步只需要做一次；
`tokenmap.json` 的刷新使用 `os.WriteFile`，既有文件的 mode 会保留；后续改属组
不会自动补上组读权限。历史 `0600` 文件仍需核对可读性，不能把下次刷新视为修复。
确认文件已存在、读组仍是获批的 GID `10001`，并取得这次权限迁移批准后，
由操作员执行以下既有读组适配；任一步失败立即停止并排查，不吞掉错误：

```bash
sudo chgrp 10001 /root/reqlog/tokenmap.json &&
  sudo chmod g+r /root/reqlog/tokenmap.json
```

本步骤不授权自动修改真实文件，也不改变记录代理运行时权限策略；未完成批准或
文件尚未产出时，保留此项为待验证，不宣称容器已经可读。

## 第 4 步：替换 systemd 单元

备份旧单元，装入新单元（`deploy/reqlog/reqlog-recorder.service`），保留旧
`ExecStart` 指向的二进制不动（回滚用）：

```bash
sudo cp /etc/systemd/system/reqlogger.service /etc/systemd/system/reqlogger.service.bak-$(date +%Y%m%d) 2>/dev/null || true
sudo cp deploy/reqlog/reqlog-recorder.service /etc/systemd/system/reqlog-recorder.service
```

> 新单元用了一个新名字 `reqlog-recorder.service`（不是复用旧的 `reqlogger.service`
> 名字），这样旧单元文件仍在、可以在两者之间切换，不是"改写后回不去"。

## 第 5 步：切换服务

```bash
sudo systemctl daemon-reload
sudo systemctl disable --now reqlogger.service 2>/dev/null || true   # 停旧服务（如果服务名不同，按实际名字改）
sudo systemctl enable --now reqlog-recorder.service
sudo systemctl status reqlog-recorder.service --no-pager
```

验证监听与落盘（几秒内应该能看到新记录）：

```bash
sudo ss -ltnp | grep -E ':(9301|9302)\b'
sudo journalctl -u reqlog-recorder.service -n 50 --no-pager
ls -la /root/reqlog/data/$(date +%Y%m%d)/ | tail -5
```

`reqlog_recorder_starting` 与 `reqlog_recorder_listening`（两条，来源分别是
`newapi`/`sub2api`）应该出现在日志里；几分钟后应该有一条
`reqlog_recorder_tokenmap_refreshed`。

停机窗口内（`disable --now` 到 `enable --now` 之间，通常 1~2 秒）nginx 的
backup 直连降级会兜住用户请求，不会中断服务，但那几秒内发生的请求不会
被记录代理捕获。

## 第 6 步：平台切到 file 模式

编辑平台部署机器上的 `.env`（例如 `/srv/deploy/xingmang-platform/deploy/compose/.env`，
改前先 `cp -p .env .env.bak-reqlog-$(date +%Y%m%d)`）：

```dotenv
XM_REQLOG_MODE=file
# 下面两个默认值已经对应记录代理的默认落盘位置，通常不需要改：
# XM_REQLOG_HOST_DATA_DIR=/root/reqlog/data
# XM_REQLOG_HOST_TOKENMAP=/root/reqlog/tokenmap.json
```

`XM_REQLOG_DATA_DIR`/`XM_REQLOG_TOKENMAP`（容器内路径）不需要改，
`deploy/compose/server-prod.yaml` 已经把它们的默认值与只读挂载目标钉在一起。

> CR-0008 新增的 `tokenmap.v2.json`（上游用户 ID）也在这一步一起覆盖：
> `server-prod.yaml` 现在带 `XM_REQLOG_TOKENMAP_V2` 与对称的只读挂载，
> 宿主机默认路径就是记录代理的 `DefaultTokenMapV2Path`。同一条告诫适用：
> 部署这份覆盖前该文件必须已经存在。见下方
> "CR-0008：tokenmap.v2.json（上游用户 ID，稳定 UserRef 的数据前提）"。

## 第 7 步：用生产覆盖重新部署 platform-api

按本仓库当前的生产部署方式执行（见 `docs/runbooks/DEPLOY.md` §3 与
`docs/runbooks/GO-LIVE-CHECKLIST.md` 的实际记录，二选一，按服务器当前实际
采用的流程执行）：

```bash
# 方式一：受控 checkout 上的部署脚本（DEPLOY.md §3 描述的正式流程）
deploy/scripts/deploy.sh prod --confirm DEPLOY-PRODUCTION --reason "XM-REQLOG-MERGE cutover"

# 方式二：本机覆盖文件方式（GO-LIVE-CHECKLIST.md 记录的实际操作）
cd /srv/deploy/xingmang-platform && nice -n 10 bash deploy/scripts/deploy-local.sh \
  --override-file /srv/deploy/xingmang-platform/deploy/compose/server-prod.yaml
```

这一步只重启 `platform-api`（`.env` 改动不影响 `platform-worker`/`web` 的行为），
但两种部署脚本都会按 compose 定义整体核对/重建；具体哪些容器会重启以脚本
实际输出为准。

## 第 8 步：验证

1. **容器能读到挂载**：
   ```bash
   # 先设置为第 7 步实际部署的项目：方式一 xingmang-prod；方式二 xingmang-launch。
   case "${XM_DEPLOY_PROJECT:-}" in
     xingmang-prod|xingmang-launch) ;;
     *) echo "请先设置 XM_DEPLOY_PROJECT 为第 7 步已部署的项目" >&2; exit 1 ;;
   esac
   docker exec "${XM_DEPLOY_PROJECT}-platform-api-1" sh -c \
     'test -d /var/lib/xm/reqlog && test -r /var/lib/xm/reqlog-tokenmap.json'
   ```
   退出 0 表示指定容器能读取目录和映射文件；不输出映射内容。非零先核对项目、容器和挂载，不据此宣称 file 数据已验证。
2. **API 能列出真实请求**（需要一个持有 `request.read` scope 的身份）：
   ```
   GET /api/v1/platforms/sub2api/requests?limit=5
   GET /api/v1/platforms/newapi/requests?limit=5
   ```
   应返回真实用户的请求元数据（不是编造的样本），`Instance` 字段是
   `"reqlog-file"`（不是 `"reqlog-fake"`，前端据此不挂"演示数据"横幅）。
3. **详情页能读正文**（需要额外的 `request.content.read` scope；这是交接
   文档 §9.4 要求的高敏权限，`staff`/`admin` 默认都不给，见
   `contracts/connectors/reqlog.read.v1.md` §4 的 RoleScopeMap 裁定）：
   ```
   GET /api/v1/platforms/sub2api/requests/{id}
   ```
   应该能看到分角色对话与装配后的最终回复；同时应在审计（`audit.read`）里
   看到一条 `request.content.viewed` 事件。
4. **`/readyz`** 正常、启动日志里没有 `reqlog_file_mode_no_tokenmap`
   （除非确实没配 `XM_REQLOG_TOKENMAP`，那种情况下用户名会恒为空串，是
   已知的降级状态，不是故障）。
5. **旧查看端**（`127.0.0.1:9300`）不再是平台的数据来源——它是否继续跑
   是记录代理自己的事，与平台是否读得到数据无关；平台侧完全不连它。

## 回滚

- **平台侧**（低风险，随时可做）：把 `.env` 的 `XM_REQLOG_MODE` 改回
  `off`（或 `.env.bak-reqlog-*` 里的旧值），重新执行第 7 步的部署命令。
  只读挂载不产生任何副作用，回滚不需要额外清理。
- **记录代理侧**（谨慎，涉及生产数据面）：
  ```bash
  sudo systemctl disable --now reqlog-recorder.service
  sudo systemctl enable --now reqlogger.service   # 按实际旧服务名调整
  ```
  旧二进制、旧数据目录结构未被本次改动破坏（格式兼容），可以直接切回。
  切回旧版本后，新版本按新权限（0750/0640 + GID 10001）写的记录仍然
  可读（旧版本只是不再收紧权限，不影响读取）。

## CR-0008：tokenmap.v2.json（上游用户 ID，稳定 UserRef 的数据前提）

`refreshTokenMap`（`cmd/reqlog-recorder/tokenmap.go`）在写 `tokenmap.json`
（v1，格式逐字节不变）的同一次刷新里，额外写一份并行文件
`tokenmap.v2.json`（默认路径 `/root/reqlog/tokenmap.v2.json`，可用
`--tokenmap-v2-path` / `XM_REQLOG_RECORDER_TOKENMAP_V2` 覆盖，留空则不写），
携带两条导出 SQL 已经 `JOIN` 到、但 v1 从未选出的 `u.id`（上游数字/不透明
用户 ID）。`connectors/reqlog` 的文件后端（`FileConfig.TokenMapV2Path`）
可选读取它，解出 `RequestLogSummary.User`；文件缺失或解析失败一律按
"映射不到"处理（`User` 为 `nil`），与 v1 tokenmap 缺失时 `Username` 恒为
空串同一条容错纪律，不是新错误类别。详见
`docs/change-requests/CR-0008-reqlog-tokenmap-upstream-user-id.md` 与
`docs/adr/ADR-020-请求日志记录器的上游库只读接入.md`。

**容器化 `platform-api` 的接入现状**：`cmd/platform-api` 读环境变量
`XM_REQLOG_TOKENMAP_V2`（容器内路径）传给 `FileConfig.TokenMapV2Path`，与
既有 `XM_REQLOG_TOKENMAP` 同一条模式；`deploy/compose/server-prod.yaml`
现在也带上了这个变量与对称的只读绑定挂载（XM-REQLOG-TOKENMAP-V2-MOUNT）：

```yaml
XM_REQLOG_TOKENMAP_V2: ${XM_REQLOG_TOKENMAP_V2:-/var/lib/xm/reqlog-tokenmap-v2.json}
- ${XM_REQLOG_HOST_TOKENMAP_V2:-/root/reqlog/tokenmap.v2.json}:/var/lib/xm/reqlog-tokenmap-v2.json:ro
```

两个默认值分别对应记录代理的 `DefaultTokenMapV2Path` 与上面这个容器内
路径，所以**不需要**在 `.env` 里写任何东西；只有把文件放在别处时才用
`XM_REQLOG_HOST_TOKENMAP_V2` / `XM_REQLOG_TOKENMAP_V2` 覆盖。

同第 3 步的告诫：宿主机路径在部署这份覆盖前必须已经存在（记录代理已经
按本文档"第 1～5 步"升级并跑过至少一轮刷新），否则 Docker 会把它当空
目录创建，容器能起来但读不到任何 v2 数据（`User` 恒为 `nil`，不是报错，
排查时先看这里——与 v1 `tokenmap.json` 挂载的既有风险同一类）。这条
挂载本身缺席时的行为不变：读侧把"文件缺失/解析失败"与"映射不到"视作
同一件事，`User` 恒为 `nil`，不影响 `Username` 与其余字段。

### tokenmap 形状检查与真实读侧验收（本次未执行服务器验证）

CR-0008 原验收标准保持：前缀命中的记录中至少 99% 能解出非空 `User`。
下面的 jq **只检查 tokenmap 文件形状和条目中 user_id 的填充比例**，
分母是映射条目数，不是实际请求记录数；即使结果为 100%，读侧仍可能全部
返回 `User=nil`。因此它不能作为真实读侧 99% 通过证据。

完成第 1～5 步且记录代理刷新后，获批操作员可运行以下只读形状检查。
真正验收仍须保留目标版本、时间窗口、实际读侧结果及分子/分母的可复核证据；
采样范围和生产取证由负责人批准，本段不新增采样 API 或改变既有标准。

```bash
# 1) 确认 v2 文件已产出且形状合法
sudo test -s /root/reqlog/tokenmap.v2.json && \
  sudo jq -e '.schema_version == 2 and (.entries | type == "object")' /root/reqlog/tokenmap.v2.json

# 2) 映射条目填充率，仅作形状诊断，不证明请求记录已解析为 User。
sudo jq '
  (.entries | length) as $total |
  ([.entries[] | select(.user_id != null and .user_id != "")] | length) as $with_id |
  {evidence_kind: "tokenmap_shape_only", proves_user_resolution: false,
   total: $total, with_id: $with_id,
   pct: (if $total == 0 then 0 else ($with_id * 100.0 / $total) end)}
' /root/reqlog/tokenmap.v2.json
```

`pct` 仅表示映射条目填充率，不是验收通过标记；实际读侧证据缺失时必须记为未验证。
若填充率偏低，核对两条导出 SQL 是否确实带上了新增的
`u.id` 列（`docker exec postgres psql -c "..."`/`docker exec
sub2api-mig-postgres sh -c '...'`，SQL 原文见 `tokenmap.go` 的
`newapiTokenMapQuery`/`sub2apiTokenMapQuery` 常量）。形状合格后仍要核对挂载、权限及实际读侧结果。

## 已知限制（不在本次收编范围内）

- 令牌映射刷新仍然经 `docker exec postgres psql` / `docker exec
  sub2api-mig-postgres sh -c 'psql ...'` 只读导出，绕开了平台的
  Connector/CredentialRef 体系（ADR-014、ADR-018）。这是既有做法的原样
  保留，重新设计需要独立的 ADR/Change Request；ADR-020「请求日志记录器的
  上游库只读接入」把这条通道接受为一个有边界的例外（详见该 ADR"决策·一"）。
  **连接主体现状**（ADR-020 决策·一·2 要求记录在此，供审计者不必重新读
  源码确认）：这条通道**不是**专用只读数据库角色——NewAPI 侧 `docker
  exec` 显式声明 `-U root`，Sub2API 侧使用该容器已配置的
  `$POSTGRES_USER`；"只读"这一属性今天完全由查询文本承载（两条 SQL 逐字
  只有 `SELECT`），不是由数据库权限承载。**凭据落地现状**：这条通道不经
  `SecretProvider`/`CredentialRef`，"凭据"事实上是宿主机 `root` 身份本身
  （`deploy/reqlog/reqlog-recorder.service` 的 `User=root`）加上该身份对
  Docker socket 的访问权；数据库侧认证在容器内部完成，平台侧管理不到、
  也看不到这一层。若 NewAPI/Sub2API 未来提供专用只读数据库角色，导出
  连接应改用该角色（属运维配置变更，不强制同 PR 代码改动），切换后应回
  ADR-020 补记"决策·一·2"。
- 如果上游连接在**响应头返回之前**就失败，这次请求完全不会被记录（不是
  记一条 `status=0` 的记录）——`status=0` 只出现在响应头已经拿到、但读
  响应体过程中连接中断的场景。
- `connectors/reqlog` 的文件后端读取时不按 `Since`/`Until` 对目录名做剪枝
  （出于时区正确性考虑，见 `file_client.go` 的注释），意味着一次不带时间
  范围的查询会扫描全部保留期内的数据；在当前量级（约 13k 请求/日 × 30 天）
  下测得可接受，未来量级明显增长时可能需要补上按目录名的安全剪枝。
