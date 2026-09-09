# Runbook：凭据管理（Foundation-A）

## 存放结构

- 加密源（入仓库）：`deploy/secrets/<env>.enc.yaml`（SOPS+age，两层 scope→name→值）
- 解密目标（不入仓库，`var/` 已 gitignore）：`var/secrets/<env>/<scope>/<name>`
- 运行注入：容器把 `var/secrets/<env>` 挂载为 FileProvider root，或经
  Docker Secret（目标文件名 `<scope>__<name>`）

## 密钥（ADR-014 备份铁律）

- age 私钥离线保存，不入仓库、不与数据库密文备份同位置
- `.sops.yaml` 中 recipient 为占位公钥，首次生成真实密钥对后替换
- 轮换：新增 recipient → `sops updatekeys` → 确认可解密 → 移除旧 recipient

## 操作

1. 编辑凭据：`sops deploy/secrets/<env>.enc.yaml`（自动加解密编辑）
2. 部署解密：`bash deploy/scripts/decrypt-secrets.sh <env>`
3. 平台装配（示例）：
   `NewAudited(NewRouter(map[string]SecretProvider{...}), NewSlogRecorder(l), env)`
4. 泄漏应急：吊销/轮换目标系统凭据 → 更新加密文件 → 重新解密部署 →
   审计日志核对 `secret_access` 异常访问

## 成本采集的动态 CredentialRef（XM-REAL0-a）

`finance` 登记簿里的账号和令牌可以在运行中新增，因此不能把每个 ref 预写成
Compose 的静态 secret。Worker 通过 `XM_FINANCE_COLLECT_SECRET_PROVIDER` 显式
选择单一来源，并用 `XM_FINANCE_COLLECT_SECRET_SCOPES` 做精确 scope 白名单：

- `file`：设置 `XM_FINANCE_COLLECT_SECRET_ROOT`（容器内默认
  `/run/xm/finance-secrets`），文件布局为 `<root>/<scope>/<name>`；宿主机以只读
  bind mount 提供 `var/secrets/<env>`。可复制
  `deploy/compose/finance-secrets.override.example.yaml`，在未入库的环境中设置
  `XM_FINANCE_COLLECT_SECRET_HOST_ROOT` 后与主 compose 文件一起启动。不存在/空
  文件均是可见的解析错误。
- `env`：按 `secret://sub2api/token-a` →
  `XM_FINANCE_SECRET_SUB2API__TOKEN_A` 的约定即时查找；scope/name 只允许
  CredentialRef 的小写字母、数字和连字符，连字符转下划线不会产生碰撞。

两种来源不自动互相回退，Provider 每次 Resolve 才读取值并由 Audited 装饰器记录
ref、用途和结果；日志不会记录明文。未配置 provider 时 real 成本采集保持
`not_supported`，不会阻断其它 Worker 任务。

## 禁止

- 明文凭据出现在仓库、日志、错误信息、前端响应或 AI 上下文（宪法 7 条）
- Connector 绕过 CredentialRef 直接读 env/文件（契约 credential-ref.v1）
