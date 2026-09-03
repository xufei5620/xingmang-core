# XM-REQLOG-TOKENMAP-V2-MOUNT：把 CR-0008 的 tokenmap.v2.json 接进生产 compose

- **状态**：已实现并在服务器上完成 CR-0008 验收标准第 1 条的只读核对；compose
  改动尚未随平台部署生效（下一次 deploy-local.sh 生效）。
- **分支**：`release/v0.1-launch`。
- **前置**：`XM-REQLOG-TOKENMAP-V2`（记录代理写 v2 文件）与 CR-0008 已合入并
  在生产运行；宿主机 `/root/reqlog/tokenmap.v2.json` 已由记录代理产出。

## 缺口

CR-0008 的"变更范围"只覆盖到记录代理二进制，明确把部署配置留给后续切片：
`cmd/platform-api` 早就读 `XM_REQLOG_TOKENMAP_V2`，但 `deploy/compose/
server-prod.yaml` 既没有这个变量的默认值，也没有对应的只读绑定挂载。于是
记录代理每轮刷新都在宿主机写出 3599 条带上游 `user_id` 的映射，容器里的
`platform-api` 一条也读不到，`RequestLogSummary.User` 恒为 `nil`。读侧把
"文件缺失"与"映射不到"当作同一件事，所以这不是报错，而是一条静默缺席的
数据链路——`docs/runbooks/REQLOG-RECORDER.md` 里原本用一整段说明它"需要
单独补"。

## 改动

`deploy/compose/server-prod.yaml` 的 `platform-api`：

```yaml
XM_REQLOG_TOKENMAP_V2: ${XM_REQLOG_TOKENMAP_V2:-/var/lib/xm/reqlog-tokenmap-v2.json}
- ${XM_REQLOG_HOST_TOKENMAP_V2:-/root/reqlog/tokenmap.v2.json}:/var/lib/xm/reqlog-tokenmap-v2.json:ro
```

两个默认值分别对应记录代理的 `DefaultTokenMapV2Path` 与容器内路径，写法与
既有 `XM_REQLOG_TOKENMAP` 一条对称，因此**不需要**往 `.env` 里加任何东西；
`.env.example` 只把两个变量作为注释登记，说明何时才需要覆盖。运维手册里
"尚未新增挂载"的整段替换成已接入的现状，保留"宿主机路径必须先存在，否则
Docker 会造空目录"这条与 v1 同源的告诫。

沿用短语法（而非 cpa-snapshot 那种 `type: bind` 强制失败）是刻意的：读侧
对缺席是容错的，一台还没升级记录代理的机器不应该因此拒绝启动。

## 验收（CR-0008 标准第 1 条，2026-09-03 在生产只读执行）

- 文件形状：`schema_version == 2`，`entries` 为对象，3599 条。
- 条目自身：3599 / 3599 携带非空 `user_id`（100%），来源分布
  sub2api 3347 / newapi 252。
- 前缀命中（真实请求日志，取最近两天 index.jsonl 共 6116 条带
  `token_prefix` 的记录）：命中 v2 映射 6116 条（100%），其中解出非空
  `user_id` 6116 条（占命中数 100%）。标准要求 ≥99%。

## 生效方式

下一次 `deploy/scripts/deploy-local.sh --override-file .../server-prod.yaml`
带上这份覆盖即可；容器重建后 `platform-api` 启动时就会读到该文件。核对
方式：请求详情列表里出现非空的用户引用，而不是只有用户名。
