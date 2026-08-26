# ADR-014：SecretProvider 和 CredentialRef 从第一天启用

| 项 | 值 |
|---|---|
| 状态 | 已接受 |
| 日期 | 2026-08-26 |
| 来源 | 规格 v2.1 §3 ADR-014（本文件拆分后为唯一权威版本） |

## 背景

凭据一旦以明文/固定环境变量方式扩散，事后治理成本极高。

## 决策

### Foundation-A 必须具备

- `SecretProvider` 接口；`CredentialRef` 类型；SOPS + age；
  Docker Secret / 受限文件注入；可选受限环境变量适配器；凭据读取审计；
  禁止明文返回前端；Connector 只接受 CredentialRef。

示例：

```text
secret://sub2api-prod/read-only-admin
secret://newapi-prod/read-only-db
secret://alerting/telegram-primary
```

NewAPI Connector 不直接读取 `NEWAPI_DB_URL` 等固定变量。环境变量只可以是
SecretProvider 的内部适配实现，不属于 Connector 契约。

### Foundation-B 补齐

- 数据库 Envelope Encryption；每凭据独立 DEK；KEK 版本管理；
  凭据创建、轮换、吊销；批量 Rewrap；凭据管理 Action 和页面。

## 理由

从第一天用间接引用，才能保证审计与轮换可行。

## 边界与后果

### 备份铁律

数据库密文备份与 KEK 不得存放在同一位置。SOPS/age 恢复私钥独立离线保存。

## 替代方案

先用环境变量后期治理：因扩散不可逆否决。

## 重评条件

Foundation-B 时可插拔 Infisical/KMS 评估。

## 相关契约

`internal/platform/secrets/` 接口定义（规格 §4.5）。

## 证据编号

无。
