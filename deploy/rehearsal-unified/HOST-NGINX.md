# 主机 nginx 切换和回滚

P1-7 把主机 nginx 纳入实际 operator。统一容器内 nginx 保留；公网两域名由主机 nginx 分别转发到统一 web 的两个 loopback 端口。`/api/`、`/invoice-api/v1/`、`/readyz`、`/healthz` 和页面入口都经过同一选择，不能遗留旧 API 的 58088 覆盖规则。

## 负责人准备的公开输入

顶层增加必需的 `host_nginx`：

```json
{
  "main_config": "/www/server/nginx/conf/nginx.conf",
  "staged_main_config": "/www/server/nginx/conf/nginx-unified-reviewed.tmp",
  "vhosts": [{
    "live": "/www/server/panel/vhost/nginx/xingmang.conf",
    "candidate": "/srv/unified-reviewed/xingmang-candidate.conf",
    "candidate_sha256": "REPLACE_WITH_REVIEWED_SHA256"
  }],
  "execution": {
    "binary": "/www/server/nginx/sbin/nginx",
    "prefix": "/www/server/nginx",
    "unshare_binary": "/usr/bin/unshare"
  },
  "web_ports": {"admin": 8088, "user": 58090}
}
```

支持一个含两个站点的 vhost 文件，或两个独立文件。替换全部路径和 SHA，使用实际主配置/运行 prefix；不要照抄示例路径。origin 来自同轮 `smoke_config`，含真实域名；端口再与实际解析出的候选 Compose web 的 80/8081 发布绑定核对。

候选 vhost 只能修改 `proxy_pass` 目的地。SSL、真实 IP、访问控制及其他指令保持原样；不拷贝/读取证书私钥。每个受管 HTTPS server 的关键路由必须明确指向 `http://127.0.0.1:<对应web端口>`，保留原请求路径。路由审计器支持明确前缀/精确 location，以及 user 站点原有的两条区分大小写正则：`~ ^/api/v1/admin/invoice-requests/[^/]+/documents/upload$` 和 `~ ^/api/v1/user/invoice-requests/[^/]+/document$`。这两条的全部 handler 仍须指向同一已审 user web 端口，不能拦截根路径、readyz、healthz 或 session 探针；pattern、modifier、header 和其他指令不得变化。其它正则或 handler override 均拒绝，不能自动删掉安全配置以求通过。

校验覆盖所有更具体的 `/api/`、`/invoice-api/`、`/webhooks/`、`/finance/` 路由，以及每个带 `proxy_pass` 的页面 location；不能仅凭登录/列表抽样成功而保留审核、开具、附件等子路径的旧后端。嵌套 location、受管 server 内未展开的 include 也不能绕过检查。关键路径之外原有的明确拒绝规则（例如 `/private` 返回 403）保持不变。

准备一份完整的 staged main：只替换 include，使候选 vhost 替代原 vhost；其他站点和共享配置仍引用原文件。实际 `nginx -T` 同时做语法检查并输出加载文件清单；预检比较两套展开结果，确认旧 vhost 消失、候选进入、其他站点字节/指令未变。单独检查一个没有被主 nginx include 的模板不能通过。

主配置可在原 AST 位置明确 include 已审 vhost 绝对路径，或直接用一个绝对目录的 `*.conf` glob 加载它们。后一种情形只在 staged main 的原 include 位置，按实际 `nginx -T` 首次加载顺序展开完整冻结成员，替换其中已审 vhost；真实 main 字节不改，其他成员仍引用原文件。展开成员必须各自只被引用一次，显式/include glob 重叠、间接包含受管 vhost、glob 父目录含通配、未知成员、作用域/顺序变化均拒绝。`-T` 本身只给每个文件的原文和首次加载顺序；operator 另遍历完整 include 图校验引用次数、上下文、无环与成员，不把文件列表误当作已展开 AST。

首次 preflight 冻结全部已加载公开配置的 SHA、mode/owner，以及所有 glob 父目录的完整直接成员（包括不匹配 glob 的条目）；snapshot 不能重新接受其间产生的共享策略变化。文件上限 256、单文件 1 MiB、总计 8 MiB、引用上限 2048、深度 32；超出即拒绝。安装前后及 reload 前重复核对；受管 live 只允许本轮已审旧/新字节，安装后严格要求本次目标字节。原有共享安全头仅限 server/location 直接 literal include，最多 64 次引用、单片 64 KiB、总计 256 KiB，只展开 `add_header`、`proxy_set_header`、`proxy_hide_header`、`proxy_http_version` 供路由验证，仍绑定原文件 SHA；嵌套 include、block 或 handler 均拒绝。候选文件保留原 include 指令和全部策略原文。

只要原 include 图包含相对路径（例如 `mime.types`、`proxy.conf`），提供的 staged main 必须与真实 main 同目录；nginx 用 main 的目录解析相对 include。operator 后续临时 main 也使用这个目录的独占 `.tmp` 文件，候选临时 vhost 放独占子目录。staged main、候选 vhost 均不得放在任何已冻结 glob 的父目录中，避免自身成为新成员。准备时可调用 `staged_main(original_bytes, exact_pairs, frozen_layout['expanded_globs'])`；该函数只返回临时内容，再以 AST 比对证明替换位置和完整顺序。不要永久展开或改写真实 main。

所有 `-T` 均经 `unshare --net --` 在新网络命名空间执行，不能访问公网；缺少工具或权限直接失败。配置如依赖启动时 DNS/网络初始化，应由负责人明确处理离线输入，不能去掉断网边界。这里不自动修改主机权限、解析器或网络配置。

## 实际顺序

1. preflight 校验候选镜像、nginx 原/候选加载树、域名路由及实际端口；不改活跃配置。
2. 在独立副本运行新栈预检；原 nginx 和旧栈仍服务。
3. snapshot 再核输入，把原公开 vhost 原字节和权限复制到本次证据目录；快照先持久化，再停旧栈。
4. 原迁移和权限作业、新栈闩通过后，先读取并核对已审 SHA 的候选字节，在主配置同目录建立本次独占的临时公开配置目录。临时 main 只替换已审核的 include 目标，加载刚复制的候选字节；先对当前与临时布局运行断网 `nginx -T`，再检查展开配置、路由和输入未变。只有全部通过后，才原子安装这些已测试字节，保留原权限；安装后再次 `nginx -T`，通过再执行 `nginx -s reload`。
5. 对配置的真实 origin 运行只读冒烟。切换或 reload/冒烟失败进入原自动回滚。

回滚先停新写者、恢复原权限和完整旧 22 容器并核原 ready，再从主配置和原 vhost 备份建立临时恢复布局。断网 `nginx -T` 通过且输入复核未变后，才原子恢复快照中的 vhost，安装后语法检查、reload，实际读取两个域名的 `/readyz`。只有这些完成才记录 `ROLLED_BACK`。已损坏或丢失的候选 vhost、负责人准备的 staged main 都不妨碍使用原快照恢复；不覆盖切换之外产生的未知 live 文件改动。切换中断后用匹配配置和持久快照运行原 rollback 入口。

暂存检查失败时，operator 不安装任何 live 文件、不修改其权限、不 reload。主配置目录须允许 operator 创建和删除本次临时公开文件；不具备权限则拒绝，不能降级为先写 live 再测。临时目录在成功或失败后仅清理本次明确创建的文件，不递归删除未知内容。输入读取前后及外部 `nginx -T` 返回后均核对文件身份、元数据与字节，候选、主配置、staged main、live 或临时布局有变化即拒绝；即使同字节替换文件也拒绝。此检查不是针对其它管理员的全局文件系统锁，操作窗口仍应避免并发编辑 nginx；安装后的检查和原回滚防线继续保留。

回滚探针要求 HTTP 200、JSON 和原平台 `3800a8b`/开票 `277063c` 的明确 `status: "ready"` 成功响应；空对象、显式失败、错误字段、重复 JSON 键不能通过。原开票成功响应允许的 `degraded` 已受控故障列表仍保留，不把它误判为失败。仅有 JSON Content-Type 不足以证明旧入口恢复。

## 本地演练映射

`local-synthetic` 的 execution 为 `container,image_id,project,host_root,container_root`。只允许显式指定、镜像和 Compose project 标签匹配的现存任务 nginx 容器，所有公开输入均在 host_root 内，映射到 container_root。生产/server-rehearsal 不接受此替代。

本地 nginx 的 `-T` 通过镜像原有 `busybox unshare -n -- nginx` 执行；本地专用 helper 需要创建网络命名空间的能力。该能力仅给没有生产卷/凭据的受控测试 helper，不修改生产服务配置。旧/新入口及实际 reload 都在此真实 nginx 上运行，不能由 Python 路由切换伪装 nginx 测试。

最终 HEAD 的 D/E、实际失败回滚、断网验证和所有源文件/镜像绑定见 F 交接单；这些本地结果不能替代服务器的输入核对与负责人执行。
