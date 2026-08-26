# 审计模块

规格 §4.4 的审计模型：完整字段事件、**防篡改哈希链**、Chain Root 签名与库外锚点。

## 为什么是哈希链

append-only 规则能挡住误操作，但挡不住拥有 DBA 权限的人——他可以先
`DISABLE RULE` 再改数据。哈希链解决的是另一个问题：**改了能被发现**。

每条事件的 `event_hash = SHA256(规范化字段 ‖ prev_hash)`。改动任何一条，
其哈希与存储值不符；删掉任何一条，序号出现缺口。`VerifyChain` 能精确定位
到第一个出问题的 `sequence`。

实测（`cmd/audit-verify`）：篡改 sequence=4 的 `reason` 字段后，
校验输出 `sequence=4 kind=hash_mismatch` 并以退出码 1 结束。

## 三类问题

| Kind | 含义 |
|---|---|
| `hash_mismatch` | 内容被改动 |
| `sequence_gap` | 记录被删除 |
| `broken_link` | `prev_hash` 与上一条的 `event_hash` 对不上 |

## Chain Root 与外部锚点

`ComputeAndSignRoot` **先全链校验再签名**——给一条已经断掉的链盖章，
等于用签名给篡改背书。签名覆盖 `from/to/root_hash/computed_at` 四项，
不只是根哈希：只签哈希的话，旧签名可以被挪到别的区间上冒充。

`ExportRoot` 把根写成 JSON（含公钥与被签载荷，**不含私钥**），供存放到
异故障域。文件可离线独立验签——有测试断言这一点。

签名密钥只经 `secrets.SecretProvider` 按 CredentialRef 解析（ADR-014），
`SignerFromSecret` 是本包唯一接触密钥材料的入口。

## 边界

- **`Canonical()` 的字段集合与顺序一旦上线即冻结**：改动会使全部历史链失效。
  需要新增字段时应发新版规范化函数并在 chain_root 标注版本，而不是就地修改。
- 脱敏责任在调用方：本包提供 `Redact`/`RedactDefault`，但不代替调用方判断
  什么是敏感的。
- 与 Action 内核的接线（每次执行都写审计事件）**不在本模块**，见 follow-up。

## 两个只有真实数据库能发现的坑（已修）

1. **时间精度**：PostgreSQL `timestamptz` 到微秒，Go `time` 是纳秒。
   用 `RFC3339Nano` 会让写入前算的哈希与读回后重算的对不上，链一读就断。
   现用固定微秒格式并在 `Normalize()` 截断。
2. **jsonb 数字类型**：读回后数字一律是 `float64`。调用方传 `int` 时表示不一致，
   大整数（>2^53）尤其明显。`Append` 前走 JSON 往返归一化。
