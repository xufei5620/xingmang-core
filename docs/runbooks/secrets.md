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

## 禁止

- 明文凭据出现在仓库、日志、错误信息、前端响应或 AI 上下文（宪法 7 条）
- Connector 绕过 CredentialRef 直接读 env/文件（契约 credential-ref.v1）
