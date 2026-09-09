# CredentialRef 契约 v1

状态：冻结（Foundation-A 起生效）。实现：`internal/platform/secrets`。

## 格式

`secret://<scope>/<name>`，scope 与 name 均匹配 `^[a-z0-9][a-z0-9-]{0,63}$`。

## 规则（ADR-014）

1. Connector 连接配置中的凭据字段类型必须是 CredentialRef，禁止内联明文、
   固定环境变量名或文件路径。
2. 解析只经 `SecretProvider.Resolve(ctx, ref, purpose)`；purpose 必填并进入审计。
3. 环境变量与文件仅是 Provider 内部实现：env 需显式登记映射；文件布局为
   嵌套 `<root>/<scope>/<name>` 或 Docker Secret 扁平 `<scope>__<name>`。
4. 未知 scope / 未登记映射 / 空值一律报错，禁止静默回退（规格 §18.1-5）。
5. scope 命名约定：目标系统实例（`sub2api-prod`、`newapi-prod`）或平台域
   （`alerting`、`ops`）。新 scope 在部署配置登记后方可使用。

## 破坏性变更

本契约的格式或规则变化必须发布 v2 并保留 v1 解析兼容期。
