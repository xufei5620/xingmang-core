# 开票系统待管理员审阅的冻结清单（2026-09-02，RC70 修复之后）

状态：只读盘点，供产品负责人在开票管理端「资格冻结」页逐条审阅。验收线不解冻这些记录：
它们不是 XM-INV-PREANCHOR-USAGE 修复工具的范围（该工具只处理 POLICY_ANCHOR 账号锚前用量/信用事实触发的 SOURCE_GAP 及其 EVENT_DEAD），
每一条都需要人工核对证据后走管理端的解冻流程（留证据、留说明）。

| 账号（external_account_id） | 冻结原因 | 触发对象 | 数量 | 首次打开（UTC） | 含义 | 建议 |
|---|---|---|---|---|---|---|
| 40bd883d-26fa-4938-b8c8-0f51c8b88686（sub2api 用户 34，鲸鱼账号） | USAGE_EXCEEDS_LEDGER | usage_event 6d12bbcb-7059-4063-90f6-bbbb966ea844；fc705a30-10ab-48df-9d08-d40731611fd3 | 2 | 2026-09-01 21:10；2026-09-02 01:05 | 用量事实超过账本可解释的余额：该账号 2026-08-31 17:20 按 POLICY_ANCHOR 重锚后，账本 = 锚点余额 + 之后的充值，重锚前的消费不计；这两条用量把账本推成负数 | 在管理端核对这两条用量的时间与金额是否落在重锚之前的消费窗口；若是重锚口径导致，按「锚前消费不可开票」解冻并写明；否则保留冻结等对账 |
| 98cce4c8-a03c-4b55-9049-61b650db2d0e（sub2api，2026-09-02 02:00 引导） | SOURCE_GAP | balance_checkpoint ×13、balance_carry_forward_proof ×3 | 16 | 2026-09-02 02:00～02:52 | 事故期间该账号的用量事实被拒绝投影，随后的余额检查点与 carry-forward 证明找不到区间起点，被判源缺口；这是同一事故的连带冻结，不在修复工具范围（工具只解锚前用量触发的那 101 条） | 修复后新的检查点已正常评估，但这 16 条旧冻结不会自动关闭；建议在管理端按「事故连带、已由 RC70 修复」批量解冻并引用本文件与 ACCEPTANCE-LOG 的 RC70-DEPLOYED 记录 |
| 98cce4c8-a03c-4b55-9049-61b650db2d0e | USAGE_EXCEEDS_LEDGER | usage_event | 1 | 2026-09-02 | 同上账号，事故后一条用量超过账本 | 与上一行一并审阅 |
| acdcdce9-c7f4-4cb4-9a02-ce527849a440 | UNKNOWN_NEGATIVE_BALANCE | balance_checkpoint | 1 | 2026-09-01 09:32 | 上游余额检查点出现负数且账本无法解释来源 | 到 Sub2API 后台核对该用户余额是否确为负（透支/退款）；确认后解冻并注明来源 |

说明：
- 冻结解除是开票管理端的 L2 写操作，需要开票管理员账号（Keycloak 退役前为 1187166666@qq.com）在管理端执行，验收线不代做。
- 鲸鱼账号的投影任务当前停在 BALANCE_PROOF_PENDING（检查点每分钟到达，但账号处于冻结），它是 readyz 仍为 503 的唯一原因；RC71（XM-INV-READY-PENDING）会让「等证明」不再误判为就绪失败，但账号本身要等上面的审阅结论才会恢复可开票。
- 本清单来自 2026-09-02 05:00Z 前后的只读查询（eligibility_freezes / source_account_eligibility_state / eligibility_projection_jobs）。
