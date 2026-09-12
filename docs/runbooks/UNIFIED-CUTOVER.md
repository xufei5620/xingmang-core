# 统一进程切换与回滚（06 / CR-0010）

本手册由负责人在服务器执行；GPT 本轮只做本地合成验证。最终验收与实际 UTC/退出码列入 UNIFIED-CUTOVER-READINESS.md。未提供服务器 D、生产 C1 与负责人选择前，不把本地 PASS 当作上线批准。

## 负责人最终三条命令

将完整、已审的无凭据正文 JSON 配置分别放在命令指定路径；`FULL_COMMIT_SHA` 替换为最终完整 40 位提交号。工作目录为已 stage 的 monorepo release 根。

```sh
bash deploy/rehearsal-unified/rehearse.sh --config /etc/xingmang-unified/rehearsal.json
bash deploy/unified/cutover.sh FULL_COMMIT_SHA --config /etc/xingmang-unified/production.json
bash deploy/unified/rollback.sh --config /etc/xingmang-unified/production.json
```

演练成功为 exit 0 / PASS，切换成功为 exit 0 / COMMITTED，显式回滚成功为 exit 0 / ROLLED_BACK。切换故障即使自动回滚成功也返回非零（ROLLED_BACK / exit 1）；回滚自身失败为 ROLLBACK_FAILED，保持新写者停止、保留现场，不重复启动两套写者。三条命令加 `--dry-run` 只生成 DRY_RUN，不产生上述合格状态。

## 切换前置

- 负责人已在同一服务器运行 D：对应最终 clean HEAD、镜像 manifest SHA、两个已验签备份和完整冒烟，且冻结副本清理成功。Windows 本地合成结果明确保留“需服务器”事项。
- 以 C1 v2 只读 SQL 同时核对有效 `ADMIN_ROLE` 的原始角色名与完整 `XM_AUTH_ROLE_SCOPES` 中的 `finance.read`；记录两者交集、TOTP 与可登录人数、原查询 SHA 和 UTC。`preflight` 必须与实际 platform-api Compose 的两项配置一致，且至少一名交集员工可以登录；不能使用默认宽泛 admin 权限或只核 scope 的旧结果。没有 TOTP 的员工会被新门禁锁在外面；先完成注册，或记录负责人接受临时锁定及恢复联系人。不能生成虚假的全覆盖证明。
- 两域签名包在 24h 以内或更短已审时限，组件、capture 时间与签名锚完整。invoice 保留原备份目录布局；平台使用明确授权的新 platform-backup 签名锚。age 原 identity 只传路径。字段密钥、source spool/cutover 密钥由原规范只读挂载，不查看正文。
- D 的 `readonly_input_volumes` 仅允许逐卷指定 `name`、实际 `owner_id` 与 `kind`：`source_credentials` 只挂 `/fixture`，`staff_credentials` 只挂 `/run/xm/secrets` 或 `/run/secrets`，`scanner_signatures` 只挂 `/var/lib/clamav` 或 `/clamav-db`。所有挂载必须只读、卷的实际 owner label 必须相同，且不得与新建数据卷重合；这些输入不写入清理名单。原签名库仍执行原 48h 新鲜度检查。归档先通过原 archive-verify，再在无网络、只挂新建恢复卷的容器中保留数字 UID/权限，避免把 `65532/0700` 来源状态变成 root 所有；容器只增恢复所需 CHOWN/FOWNER/DAC_OVERRIDE 能力。
- 保留原适用前置：四个来源容器、精确 SUB binary/NEW image tag、带审核人/有效期/原文 SHA 的 Cloudflare 离线 CIDR 材料与主机 real-IP（preflight 不发公网请求，不声称当前在线复核）、主机根盘及 DockerRootDir 使用率小于 80%、NTP、所有网络/精确代理 /32、env 必需字段及本地不可拉取镜像。
- D 用全新端口。E 可以接管被精确核对的旧容器的同一个 127.0.0.1 端口；其它占用拒绝。停旧后重新检查端口已释放。
- 旧平台项目和原开票三个项目、18 + 4 个容器的镜像/Compose/env/挂载/数据库卷均原位保存。stage 前按 [旧输入保全](UNIFIED-OLD-INPUTS.md) 捕获公开 SHA 绑定，填写 `previous.input_snapshot`；新树的旧平台 `launch.yaml` 已退役清空，不能用它回滚或覆盖旧 release。主机有配置要求的余量。不得 `down -v` 清理旧栈、覆盖旧 env 或删除 source state。

## 冻结、切换和验证顺序

1. 本地 Docker endpoint、manifest/镜像、主机守卫、D/C1/签名包及 [主机 nginx 预检](../../deploy/rehearsal-unified/HOST-NGINX.md) 全部成功后，对新栈独立冻结副本再跑一次实际预检，保持旧入口和旧栈服务。预检完整清理成功，才写含原 nginx vhost 的持久 PREPARED 快照并进入停写。
2. 依次停止旧 source agents、旧 invoice、旧 platform、旧 idp，确认各项目没有仍在运行的容器并确认接管端口空闲。保留旧容器/卷/文件。
3. 启动新两个数据库并等待健康；按顺序执行平台 migrate（业务 + River）、invoice-migrate、invoice-permissions。平台沿用原角色/授权，目录只读快照必须前后一致；没有把 migrate 叫作权限重放，也不在本次实施 DBR1。
4. 比较两库实际迁移 ledger 与冻结时的 SHA，再启动统一 API/独立 worker、扫描器及十个新 source agents。
5. 要求 `/readyz` 的 modules.platform、invoice_sources、invoice_projection 都 ready=true/status=ready，并且 invoice_ready=true。统一外部 `/readyz` 只有平台和开票原 11 闩同时通过才返回 HTTP 200；任意开票失败返回 503，容器 healthcheck 消费同一端点。not_evaluated 必须视为未就绪。
6. 将已审主机 vhost 原子改为新 web 端口，断网命名空间中 `nginx -T` 成功后 reload。按实际配置域名执行[固定十步只读冒烟](../../deploy/rehearsal-unified/SMOKE.md)：真实登录/TOTP、列表/来源隔离/越权拒绝、原生管理页 HTTP、旧入口拒绝和会话撤销；绝不创建、审核、开具或上传发票。全部通过才写 COMMITTED。

停机窗口由“停旧”到“模块和冒烟通过”构成。先以本次本地实测给量级，服务器 D 再给主机量级；数据量、镜像已缓存、来源追数和扫描签名年龄都会影响耗时。没有服务器实测时不承诺固定分钟数，负责人应明确低峰窗口和超时回滚阈值。

## 回滚及 0032

回滚读取原持久快照并要求 HEAD/manifest/config/mode 均匹配。先停新 source 和统一项目，证明无新写者；恢复旧数据库，重放**原 invoice permissions**，核平台原授权目录和两库 ledger。再按 idp、平台、开票、source 顺序启动旧项目。实际 18 + 4 容器、镜像、挂载、端口、原 Compose/env SHA 和两个旧 readyz 全部匹配才成功。

旧栈就绪后，必须再恢复本次快照中的主机 nginx vhost 原字节，语法检查、reload，并实际读取两个域名的 `/readyz`。这几步是 rollback 程序必经路径；仅容器恢复不能记录回滚成功。未知的 nginx 文件改动保留并停止，不用候选模板覆盖原恢复快照。

本次 CR-0010 保持 schema。0032 两端同一 migration/checksum 时原样保留，不删 ledger，不降索引，不向旧二进制挂新 SQL 欺骗校验。若实际 ledger 不相同，自动原位镜像回滚明确失败并保持写者停止；负责人按原 PRODUCTION-RUNBOOK 12.1 对精确单项差异裁决，或从完整匹配签名备份恢复两库/文档/source state，不能无条件 DELETE 迁移记录。

## Keycloak 7 天回滚窗口

切换时两个旧 IdP 容器停止，新统一配置和路由不再引用 Keycloak 或 console assertion；旧数据和相关 secret 只用于回滚，默认保留 7 天。旧环境只随回滚恢复，不能把旧 OIDC 配置塞回统一进程。

第 7 天由负责人核部署记录仍为 COMMITTED、没有回滚需求后，使用记录中的**确切旧项目/Compose/env/卷和 secret 路径**执行以下模板（先替换所有大写占位，先核标签，不运行通配删除）：

```sh
docker compose --project-name OLD_IDP_PROJECT --env-file OLD_IDP_ENV -f OLD_IDP_COMPOSE down
docker volume inspect EXACT_OLD_KEYCLOAK_VOLUME
docker volume rm EXACT_OLD_KEYCLOAK_VOLUME
shred --iterations=3 --zero --remove=unlink -- EXACT_RETIRED_CONSOLE_ASSERTION_KEYRING
```

同时从 `docs/runbooks/SECRETS-INVENTORY.md` 划掉经核对的旧条目；只删已退役、没有其它消费者的 exact secret，原备份解密身份不在清理列表。当天删除须负责人另行选择；此时回滚需要重装同版本 Keycloak 并恢复签名数据库备份，额外耗时取决于已备镜像/数据量，本次没有生产实测，不编造恢复承诺。

## 旧链退役、新链与风险

切换成功后负责人禁用旧平台 hook/旧开票独立发布入口，保留只读历史材料。统一产物仅用最终源码构建清单、完整镜像档案和已审 stage 文件；签名、传输、校验、load 由原 artifact 流程完成，切换脚本不在线构建、不 pull、不 fetch、不 push。旧入口已返回明确退役非零，不应另留一个可写生产的旧真相源。

API 为单进程，两端 HTTP/会话/后台开票任务共享进程故障面；API 崩溃影响平台 API 与开票 API。平台 worker、十个采集器、PDF 扫描器、ClamAV 与数据库仍独立。平台业务路由在开票未就绪时仍可访问，但统一 readiness 与容器健康必须变为失败；开票申请/发票流程继续 fail-closed。切换判据同时检查全部模块与原 invoice_ready，避免掩盖源/投影/扫描器失败。统一生产进程没有关闭开票却报告整体健康的模式；缺少模块拒绝构造，未配置 readiness 或停止中的模块均不可报告 ready。

基础镜像 CVE 原样进入 F 的待拍板；本脚本不引入“漏洞清零”附加门，也不忽略或谎报现有风险。服务器 D、生产 MFA/历史身份映射、主机真实网络/来源、停机窗口与 Keycloak 删除时间仍需负责人完成。完整旧 18 容器回滚的本地结果必须是本轮真跑，旧三二进制结果不算替代证据。

# E2 / E4 / E5：签名交付与维护窗口

2026-09-12；依据 `06-统一进程-可部署终点.md`、当前 canonical `scripts/unified-service.py` / `deploy/unified/images.json`、旧 `invoice/docs/PRODUCTION-RUNBOOK.md` 的 OpenSSH 签名方式编写。以下网络、签名、服务器 stage / load 命令**本轮均未执行**；不改旧 manifest，不签 tag，不恢复旧发布脚本。示例必须替换最终 clean HEAD 和最终 manifest，不能沿用某次 r2 的临时提交。

## E5：旧链退役与统一交付

旧平台 hook 与旧开票独立发布入口在切换成功后由负责人停用；保留原镜像、Compose、env 和已签备份用于 7 天回滚。它们不再发布新版本。旧 `release-image-gate.ps1` / `verify-release-image-artifacts.ps1` 的九镜像契约不适用于统一十一镜像，不能改名或借旧 RC/tag 绕过。新链为：最终源码的 B 门禁 → canonical 11 镜像 build / 本机构建证据 verify → 下列完整包签名 → 传输到新目录 → 原始字节验签和离线逐镜像验证 → 本机 Docker load → 服务器 D → E cutover。签名或 load 成功均不等于 D 通过、生产批准或自动切换。

沿用已审 OpenSSH Ed25519 release 身份 `invoice-release@solov.cc` 与 namespace `solov-invoice-release-v1`。这是统一包的完整性签名，不沿用旧九镜像门禁的放行结论。allowed-signers 必须来自负责人独立保留的已审副本，不能信任传输包自带的公钥。私钥始终只作为 `ssh-keygen -f` 的路径参数；不显示、不复制到服务器、不装入包。

统一 manifest 使用 `xingmang.unified.local-build/v1`：`source.gitHead`、`source.inventorySha256`、`contextSha256`、`definitionSha256` 及 11 个 image record 原样保留；每个 record 的 `imageId`、`configDigest`、`archiveSha256`、`baseImages` 均受签名约束，九个构建镜像的 BuildKit JSON 也纳入包。两个 PostgreSQL 保留 `images.json` 中原官方 digest，不构造 PG 补丁包。`productionReady` 必须仍为 `false`，不能通过手改 manifest 变为放行。

### 1. 构建机：验证最终输出并形成只含公开产物的新交付目录

构建结束后，在**同一构建机 / 同一 Docker context**执行（PowerShell 7；使用已安装的 Python / Git / Docker / OpenSSH）。修改四个路径和最终 SHA。交付目录必须不存在，放在仓库外；`$builtArtifacts` 必须是最终构建输出原目录。

```powershell
$ErrorActionPreference = 'Stop'
$sourceRoot = 'G:/xingmang/09-wt/unified-deploy-endpoint-20260912'
$builtArtifacts = 'G:/xingmang/logs/unified-deploy-endpoint-20260912/FINAL_BUILD_DIRECTORY'
$delivery = 'G:/xingmang/logs/unified-deploy-endpoint-20260912/FINAL_DELIVERY_DIRECTORY'
$expectedHead = 'REPLACE_WITH_FINAL_40_HEX_SHA'
$localDockerContext = 'qualification-local'
python "$sourceRoot/scripts/unified-service.py" verify --manifest "$builtArtifacts/manifest.json" --docker-context $localDockerContext
if ($LASTEXITCODE -ne 0) { throw 'canonical local build verification failed' }

@'
import hashlib, importlib.util, json, pathlib, re, shutil, sys
if sys.platform == 'win32' and not hasattr(pathlib.Path('.'), 'is_junction'):
    raise SystemExit('use the existing Python 3.12+ runtime with Windows junction detection')
def lexical_path(value):
    path = pathlib.Path(value)
    if not path.is_absolute() or '..' in path.parts: raise SystemExit('path must be absolute without parent traversal')
    for part in [path, *path.parents]:
        if part.is_symlink() or (getattr(part, 'is_junction', lambda: False)()):
            raise SystemExit('symlink/junction path rejected before resolution')
    return path.resolve()
root, original, destination = (lexical_path(x) for x in sys.argv[1:4])
head = sys.argv[4]
if not re.fullmatch(r'[0-9a-f]{40}', head): raise SystemExit('replace final HEAD')
if destination.exists() or destination.is_relative_to(root): raise SystemExit('delivery must be new and outside checkout')
spec = importlib.util.spec_from_file_location('unified_gate', root/'scripts/unified-service.py')
gate = importlib.util.module_from_spec(spec); spec.loader.exec_module(gate)
doc = json.loads((original/'manifest.json').read_text('utf-8'))
gate.assert_manifest_kind(doc)
if doc['source']['gitHead'] != head: raise SystemExit('wrong final HEAD')
gate.assert_source_matches(doc['source'], gate.source_provenance(root))
items = gate.definitions(root)
gate.assert_image_inventory(doc['images'], items)
gate.assert_file_hash(root/'deploy/unified/images.json', doc['definitionSha256'])
roles = {x['name']: x for x in items}
files = {'manifest.json': gate.file_hash(original/'manifest.json')}
for record in doc['images']:
    name = record['name']; archive = 'images/'+name+'.tar'
    if record['archive'] != archive: raise SystemExit('unexpected archive path')
    files[archive] = record['archiveSha256']
    if roles[name]['kind'] == 'built':
        metadata = record['buildkitMetadata']; wanted = 'logs/'+name+'.buildkit.json'
        if metadata['path'] != wanted: raise SystemExit('unexpected metadata path')
        files[wanted] = metadata['sha256']
    elif record['buildkitMetadata'] is not None: raise SystemExit('pinned image has unexpected metadata')
for relative, expected in files.items():
    path = lexical_path(original/relative)
    gate.assert_file_hash(path, expected)
destination.mkdir(parents=True)
for relative in sorted(files):
    target = destination/relative; target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(original/relative, target)
    gate.assert_file_hash(target, files[relative])
# Recreate the canonical frozen context from the same clean source, not an arbitrary directory tar.
if gate.create_context(root, doc['source'], destination/'source.tar') != doc['contextSha256']:
    raise SystemExit('source context changed; leave failed delivery for diagnosis, do not sign it')
gate.assert_source_matches(doc['source'], gate.source_provenance(root))
files['source.tar'] = doc['contextSha256']
with (destination/'SHA256SUMS').open('x', encoding='ascii', newline='\n') as stream:
    for relative in sorted(files): stream.write(files[relative]+'  '+relative+'\n')
print(json.dumps({'status':'PACKED_UNSIGNED', 'sourceHead':head,
    'manifestSha256':files['manifest.json'], 'images':len(doc['images']),
    'signedFiles':len(files), 'productionReady':False}, indent=2))
'@ | python - $sourceRoot $builtArtifacts $delivery $expectedHead
if ($LASTEXITCODE -ne 0) { throw 'delivery construction failed; do not sign or transfer' }
```

期望 exit 0、`PACKED_UNSIGNED`、11 个镜像、22 个待签文件：manifest + source.tar + 11 archive + 9 BuildKit JSON。保留原构建日志和 B 门禁证据；这里不扫描或复制 `.env`、私钥、凭据目录、数据库、source state、备份解密身份。`source.tar` 由原 canonical 过滤器生成，包含本次脚本、Compose、两端构建源码；不是服务器配置备份。根目录 `docs/` 不在该 context 中，本手册和负责人填写的配置另行保留，不声称它们已被此 source tar 打包。

### 2. 构建机：签署 SHA256SUMS，并按原始字节验签

```powershell
$releaseSigningKey = 'REPLACE_WITH_OFFLINE_ED25519_PRIVATE_KEY_PATH'
$releaseAllowedSigners = 'REPLACE_WITH_INDEPENDENT_REVIEWED_ALLOWED_SIGNERS_PATH'
$checksumManifest = Join-Path $delivery 'SHA256SUMS'
$releaseSignature = "$checksumManifest.sig"
if (Test-Path -LiteralPath $releaseSignature) { throw 'refuse to overwrite an existing signature' }
& ssh-keygen -Y sign -q -f $releaseSigningKey -n solov-invoice-release-v1 $checksumManifest
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $releaseSignature -PathType Leaf)) { throw 'artifact signing failed' }
$verifyStart = [Diagnostics.ProcessStartInfo]::new()
$verifyStart.FileName = (Get-Command ssh-keygen -ErrorAction Stop).Source
$verifyStart.UseShellExecute = $false
$verifyStart.RedirectStandardInput = $true
foreach ($argument in @('-Y','verify','-f',$releaseAllowedSigners,'-I','invoice-release@solov.cc','-n','solov-invoice-release-v1','-s',$releaseSignature)) { $verifyStart.ArgumentList.Add($argument) }
$manifestInput = [IO.File]::OpenRead($checksumManifest)
try {
  $verifier = [Diagnostics.Process]::Start($verifyStart)
  try {
    $manifestInput.CopyTo($verifier.StandardInput.BaseStream)
    $verifier.StandardInput.Close()
    $verifier.WaitForExit()
    if ($verifier.ExitCode -ne 0) { throw 'artifact signature verification failed' }
  } finally { $verifier.Dispose() }
} finally { $manifestInput.Dispose() }
```

期望 sign / verify 均 exit 0。不要用 `Get-Content -Raw | ssh-keygen`，PowerShell 文本管道可能改变被签字节。不得创建或移动任何签名 tag。负责人另行记录 pack 输出的完整 HEAD、manifest SHA256 与交付目录；服务器参数从这份已审记录填写，不从未经校验的远端文件自取。

### 3. 传输：只传新交付目录，不覆盖已发布目录

以下是负责人将来执行的命令模板，本轮不联网执行。`REVIEWED_HOST_ALIAS` 须已通过原主机指纹核对；不使用 `StrictHostKeyChecking=no`。`FINAL_TRANSFER_ID` 只用英数、点、短横线，选择此前不存在的值，不能填 `current`。服务器接收根先由负责人准备为不允许其他用户写入的目录。

```powershell
# 先在服务器确认此 exact 子目录不存在；若已经存在就停止，不用覆盖/--delete。
ssh -o StrictHostKeyChecking=yes REVIEWED_HOST_ALIAS 'umask 077; test -d /srv/xingmang-incoming && test ! -e /srv/xingmang-incoming/FINAL_TRANSFER_ID'
if ($LASTEXITCODE -ne 0) { throw 'remote target is unavailable or already exists' }
scp -o StrictHostKeyChecking=yes -r -- $delivery REVIEWED_HOST_ALIAS:/srv/xingmang-incoming/FINAL_TRANSFER_ID
if ($LASTEXITCODE -ne 0) { throw 'transfer incomplete; do not stage/load' }
```

传输仅是复制文件，不运行远端解包或 Docker。中断留下的目录保留为失败现场；换一个全新 transfer ID 重新传输，不能把不完整目录当作已验包。私钥和 allowed-signers 不随包发送。

### 4. 服务器：先独立验签，再核完整包/源码/所有 archive，最后 load

在负责人已审的目标服务器 Bash 中执行。`SOURCE_ROOT` 为全新目录（不是旧 checkout、`current`、旧生产路径），`ARTIFACTS` 为刚传的包。此阶段只 stage 文件和加载本机镜像，不启动服务、不迁移、不重启、不改旧项目。所有失败均停止，不执行 E。

```sh
set -euo pipefail
umask 077
ARTIFACTS=/srv/xingmang-incoming/FINAL_TRANSFER_ID
SOURCE_ROOT=/srv/xingmang-releases/FINAL_TRANSFER_ID
EXPECTED_HEAD=REPLACE_WITH_FINAL_40_HEX_SHA
EXPECTED_MANIFEST_SHA256=REPLACE_WITH_REVIEWED_64_HEX_MANIFEST_SHA
ALLOWED_SIGNERS=/etc/xingmang-release/allowed_signers
test -d "$ARTIFACTS"
test ! -L "$ARTIFACTS"
test ! -e "$SOURCE_ROOT"
test -f "$ALLOWED_SIGNERS"
python3 - "$ARTIFACTS" <<'PY'
import pathlib, sys
root = pathlib.Path(sys.argv[1]).absolute()
for path in (root, root/'SHA256SUMS', root/'SHA256SUMS.sig'):
    if any(p.is_symlink() for p in [path, *path.parents]): raise SystemExit('symlink signature path rejected')
for name in ('SHA256SUMS','SHA256SUMS.sig'):
    path = root/name
    if not path.is_file() or not 0 < path.stat().st_size <= 65536: raise SystemExit('invalid signature/checksum file')
PY
ssh-keygen -Y verify -f "$ALLOWED_SIGNERS" -I invoice-release@solov.cc \
  -n solov-invoice-release-v1 -s "$ARTIFACTS/SHA256SUMS.sig" < "$ARTIFACTS/SHA256SUMS"
# 不把构建机 verify --docker-context 原封不动拿来服务器执行：它检查的是同一个本机构建器身份。
# 下段复用原 archive / BuildKit 校验函数，独立检查服务器本地 Docker endpoint，绝不 build/pull。
python3 - "$ARTIFACTS" "$SOURCE_ROOT" "$EXPECTED_HEAD" "$EXPECTED_MANIFEST_SHA256" <<'PY'
import hashlib, importlib.util, io, json, pathlib, re, shutil, sys, tarfile
bundle, staged = [pathlib.Path(x).absolute() for x in sys.argv[1:3]]
head, manifest_sha = sys.argv[3:5]
def require(ok, message):
    if not ok: raise SystemExit(message)
def digest(path):
    with path.open('rb') as stream: return hashlib.file_digest(stream, 'sha256').hexdigest()
def plain(path):
    require(not any(p.is_symlink() for p in [path, *path.parents]), 'symlink path rejected')
def canonical(value): return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()
require(re.fullmatch(r'[0-9a-f]{40}', head) and re.fullmatch(r'[0-9a-f]{64}', manifest_sha), 'fill reviewed source/manifest pins')
plain(bundle); plain(staged); require(not staged.exists(), 'stage must be new')
require(not staged.is_relative_to(bundle) and not bundle.is_relative_to(staged), 'source and artifacts must be separate')
roles = {'platform-api','platform-worker','platform-migrate','web','invoice-tools','pdf-scanner','source-agent','invoice-postgres','clamav','ingest-proxy','platform-postgres'}
built = roles - {'invoice-postgres','platform-postgres'}
expected = {'manifest.json','source.tar'} | {'images/'+n+'.tar' for n in roles} | {'logs/'+n+'.buildkit.json' for n in built}
rows = {}
plain(bundle/'SHA256SUMS')
for line in (bundle/'SHA256SUMS').read_text('ascii').splitlines():
    match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9./_-]+)', line)
    require(match is not None, 'malformed signed checksum line')
    sha, name = match.groups()
    require(name in expected and name not in rows, 'unexpected or duplicate signed path')
    rows[name] = sha
require(set(rows) == expected, 'incomplete signed package')
# Enumerate only names first. Never traverse a symlink or open an unexpected/secret file.
def walk(directory, prefix=''):
    result = set()
    for entry in directory.iterdir():
        plain(entry); name = prefix+entry.name
        if entry.is_dir():
            require(name in {'images','logs'}, 'unexpected directory')
            result |= walk(entry, name+'/')
        else:
            require(entry.is_file(), 'non-regular package member')
            result.add(name)
    return result
require(walk(bundle) == expected | {'SHA256SUMS','SHA256SUMS.sig'}, 'extra or missing package file')
for name, sha in rows.items(): require(digest(bundle/name) == sha, 'signed file hash mismatch: '+name)
require(rows['manifest.json'] == manifest_sha, 'wrong reviewed manifest')
doc = json.loads((bundle/'manifest.json').read_text('utf-8'))
require(doc.get('schema') == 'xingmang.unified.local-build/v1' and doc.get('productionReady') is False, 'wrong manifest kind')
source = doc['source']
require(source['gitHead'] == head and source['gitDirty'] is False, 'wrong or dirty source')
require(hashlib.sha256(canonical(source['entries'])).hexdigest() == source['inventorySha256'], 'source inventory mismatch')
require(rows['source.tar'] == doc['contextSha256'], 'wrong frozen build context')
entries = {}
for entry in source['entries']:
    name = entry['path']; parts = pathlib.PurePosixPath(name).parts
    require(name not in entries and parts and not name.startswith('/') and '..' not in parts and '\\' not in name, 'unsafe/duplicate source inventory')
    entries[name] = entry
# source.tar is already signed and hash-bound. Verify every file against the signed Git blob inventory before importing code.
with tarfile.open(bundle/'source.tar', 'r:') as archive:
    members = archive.getmembers(); names = set()
    for member in members:
        name = member.name; entry = entries.get(name)
        require(member.isfile() and not member.issym() and not member.islnk() and entry is not None and name not in names, 'unsafe source tar member')
        require(entry['mode'] in {'100644','100755'} and member.mode == int(entry['mode'][-3:],8), 'wrong source file mode')
        body = archive.extractfile(member).read()
        require(any(hashlib.sha1(b'blob '+str(len(data)).encode()+b'\0'+data).hexdigest() == entry['blob'] for data in (body, body.replace(b'\r\n',b'\n'))), 'source blob mismatch')
        names.add(name)
    require({'scripts/unified-service.py','deploy/unified/images.json'} <= names, 'missing canonical verifier')
    staged.mkdir(parents=True)
    for member in members:
        target = staged/member.name; target.parent.mkdir(parents=True,exist_ok=True)
        with target.open('xb') as output, archive.extractfile(member) as data: shutil.copyfileobj(data,output)
        target.chmod(member.mode)
spec = importlib.util.spec_from_file_location('unified_gate', staged/'scripts/unified-service.py')
gate = importlib.util.module_from_spec(spec); spec.loader.exec_module(gate)
gate.assert_manifest_kind(doc)
require(names == {e['path'] for e in source['entries'] if gate.context_path_allowed(e['path'])}, 'source context inventory incomplete')
items = gate.definitions(staged); gate.assert_image_inventory(doc['images'],items)
gate.assert_file_hash(staged/'deploy/unified/images.json',doc['definitionSha256'])
by_name = {item['name']:item for item in items}
# Validate all 11 archives, exact references, configs and base provenance before the first Docker load.
for record in doc['images']:
    name = record['name']; item = by_name[name]; relative = 'images/'+name+'.tar'
    reference = item['reference'] if item['kind']=='pinned' else item['repository']+':'+doc['tag']
    require(record['archive']==relative and record['archiveSha256']==rows[relative] and record['reference']==reference,'image record mismatch')
    proof = gate.assert_archive_image(bundle/relative,reference,record['imageId'])
    require(proof['configDigest']==record['configDigest'],'wrong config digest')
    if item['kind']=='built':
        metadata=record['buildkitMetadata']; path='logs/'+name+'.buildkit.json'
        require(metadata['path']==path and metadata['sha256']==rows[path],'wrong BuildKit metadata')
        value=json.loads((bundle/path).read_text('utf-8'))
        gate.assert_buildkit_output(value,proof,reference)
        require(record['baseImages']==gate.base_image_evidence(value),'wrong base provenance')
    else:
        require(record['buildkitMetadata'] is None and record['baseImages']==[{'uri':reference,'digest':'sha256:'+reference.rsplit('@sha256:',1)[1]}],'wrong pinned base')
docker = gate.docker_command('default')  # accepts only the actual local Unix socket, no remote daemon
for record in doc['images']:
    gate.run(docker+['image','load','--input',str(bundle/record['archive'])])
    actual=gate.run(docker+['image','inspect','--format','{{.Id}}',record['reference']]).decode().strip()
    require(actual==record['imageId'],'loaded image ID differs; do not retag/alter manifest to hide it')
    if record['name'] in built:
        labels=json.loads(gate.run(docker+['image','inspect','--format','{{json .Config.Labels}}',record['reference']]).decode()) or {}
        require(labels.get('org.opencontainers.image.revision')==head and labels.get('xingmang.source.inventory')==source['inventorySha256'] and labels.get('xingmang.source.context')==doc['contextSha256'],'loaded source labels mismatch')
print(json.dumps({'status':'STAGED_AND_LOADED','sourceHead':head,'manifestSha256':manifest_sha,'images':len(doc['images']),'productionReady':False}))
PY
```

期望验签 exit 0；Python exit 0 / `STAGED_AND_LOADED` / 11。所有 archive 在第一次 load 之前完成静态校验；load 不运行镜像。若中途 load 失败，仅留下已导入的候选镜像和新的 stage 目录，旧服务仍未动；保留错误，不执行 cutover，不把部分成功当作 PASS。Docker 版本/存储后端若不能原样加载所交付 archive，或 `imageId` 语义不能一致，视为服务器 D 需解决的实际兼容失败；不能把 configDigest 改填 imageId 或改签 manifest 来绕过。

stage 后保持 `ARTIFACTS` 原位；负责人配置 `candidate.manifest` 指向其中原 `manifest.json`，`candidate.manifest_sha256` 填上述已核 SHA，`candidate.head` 填最终 HEAD；各 runtime/job image ID 从相同 manifest 的准确角色取值，十个采集流都使用 `source-agent` 角色的同一个 image ID。源 stage 是受签名 source.tar 还原的只读源码目录，并非伪造的 Git checkout；服务器不需要也不执行 fetch、pull 或 build。按手册准备完整配置后，从该 SOURCE_ROOT 执行既定三条 rehearse / cutover / rollback 命令。

每个独立命令组在负责人执行时记录 UTC 开始、结束和实际退出码。任何非零停止该链；上述“期望 exit 0”不是本轮实测。整个 stage 过程不读或打印 private key、env、backup identity 的内容。

## E2：停机窗口

以下是**排期估算，尚非服务器实测**，以镜像已 stage/load、签名备份与服务器 D 通过、无新增 schema、来源正常、磁盘满足前置为前提。预留一次切换约 **10–20 分钟**，再预留一次原位回滚约 **10–20 分钟**，负责人安排约 **30–45 分钟的低峰维护窗口**。区间不能替代脚本的失败判定：停止旧写者约 1–3 分钟，两库健康/幂等迁移与权限核对约 1–4 分钟，统一 API/worker/扫描器/采集器就绪和十步真实冒烟约 5–10 分钟，现场命令及日志复核约 1–3 分钟。阶段可能重叠，也可能受来源追数、ClamAV 初始加载、主机 IO 和源网络延长，不把分项简单相加包装成测量值。

当前脚本某次 `compose up --wait` 上限 300 秒、单次 ready 检查上限 120 秒，是每次调用的技术超时，不是整条切换的保证时长。服务器 D 后记录各步骤真实 UTC，按较慢的成功路径和实际故障回滚路径更新窗口；若超过负责人接受的窗口，在停旧前取消，不临时放宽超时/来源闩。停旧后的异常由脚本自动回滚且非零。0032 ledger/权限不一致不能靠延长等待解决；按手册保留写者停止并升级负责人裁决。

## E4：当天删除的恢复耗时

默认仍为停用当日保留旧 Keycloak 容器、数据库卷、镜像和独立相关 secret 7 天；原位回滚只重启并核验，按上节预留。负责人若明确选择当天删除容器与数据卷，回滚增加“用保留的精确旧 Compose/env/镜像重建 IdP → 用对应已验签历史备份恢复 Keycloak 数据 → 恢复原 client/issuer/secret 路径引用 → 原会话/旧登录及旧 22 容器核验”这段，不能仅 `compose up` 宣称恢复完成。

在镜像和完整加密备份均已本机备妥、所需回滚 secret 仍可由原保管方式恢复的条件下，额外先预留 **20–40 分钟**；加上普通回滚，维护窗口应按 **45–90 分钟**安排并由负责人接受。这是容量/操作排期估算，未在生产计时；服务器 D 的实际恢复时间才用于最终调整。大库、慢盘、重新获取镜像、备份/密钥不可用可能超过此范围；**若当天连唯一所需 secret 也不可恢复地销毁，回滚无法给出有限时间保证**，不能声称“重装即可”。不要求本轮新做生产备份或读取密钥；只在负责人决策项中明确现有备份和原保管资料是否足以恢复。

第 7 天清理仍沿主手册 exact project / volume / secret 路径命令，不改成通配删除，不清理原 age 解密身份，也不创建新的身份提供商。该估算只解释两种已授权选择的差别，不新增上线验收清单。
