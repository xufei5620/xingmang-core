# ADR-015：Tailscale 负责网络层，命令信封负责动作鉴权

| 项 | 值 |
|---|---|
| 状态 | 已接受 |
| 日期 | 2026-08-26 |
| 来源 | 规格 v2.1 §3 ADR-015（本文件拆分后为唯一权威版本） |

## 背景

服务器动作需要网络可达性与动作级鉴权两层保障，二者职责不能互相替代。

## 决策

Tailscale 负责：设备组网；传输加密；网络可达性；ACL；Tagged Resource。

Server Agent 负责：动作白名单；参数 Schema；指令签名；防重放；审批验证；
幂等；本地审计；结果证明。

## 理由

网络层信任不等于动作授权；动作必须逐条验证。

## 边界与后果

### 命令信封

```text
request_id
action_id
action_version
target_service
target_environment
issued_at
expires_at
nonce
principal_id
approval_decision_id
parameters_hash
signature
```

### Agent 九项验证

1. 指令来源；2. 时间窗口；3. Nonce 防重放；4. 目标环境；5. Action 白名单；
6. 参数 Schema；7. 审批凭证；8. 是否已执行；9. 结果完整回传。

禁止任意 Shell、任意 Docker 命令、任意路径文件读写、页面拼接命令参数和
平台直挂 Docker Socket。

## 替代方案

Tailscale 可替换为 Headscale 或原生 WireGuard（保留替换路径，见规格 §0.5）。

## 重评条件

Tailscale Personal 限额或商务条件变化（证据 V7）。

## 相关契约

命令信封示例见规格附录 L。

## 证据编号

V7（Tailscale 官方 Pricing）。
