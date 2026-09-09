-- XM-INV-NEWAPI-AUTOVERIFY：New API 充值改为按证据自动核验。
--
-- **这条迁移移除一道财务控制**，务必读完再执行。
--
-- 产品负责人 2026-09-06 明确决定（在被告知本触发器的存在与含义之后，仍选择
-- "完全自动"）：New API 用户当前连提交开票申请都做不到——他看到的可开票金额
-- 恒为 0，因为每一笔充值都卡在双人复核队列里，而那个队列没有人在处理。
--
-- 决定的依据是三件已核实的事实（对着 New API 源码 K:/newapi-src 查的）：
--
--  1. **管理员送的免费额度进不到本系统。** 手动加额度走 IncreaseUserQuota，
--     它只改 users.quota，从不写 top_ups；而本系统的充值投影只读 top_ups。
--     这是结构隔离，不是操作纪律。
--  2. **"补单"（AdminCompleteTopUp）是真钱。** 它必须带 tradeNo，那是支付流程
--     创建的订单——钱到了、只是回调没回来。
--  3. **最终由人开票。** 用户提交的只是申请；真发票由管理员在税务平台手动
--     开具再上传。自动核验 ≠ 自动开票，人仍是最后一道关。
--
-- **被移除的是什么**：0006 建的 newapi_verified_dual_control_guard 触发器，
-- 它要求每一笔 verified 的 New API 资金批次都有一条 proposed_by <> approved_by
-- 的已批准决策。移除之后，**没有任何人需要对这笔钱签字**，唯一的关卡是开票
-- 那一刻的人工判断。
--
-- **没有被移除的**：payment_candidate_decisions / payment_candidate_reviews
-- 两张表与提议/批准端点都保留。它们仍然是例外处置的通道——尤其是下面这条。
--
-- **仍然存在的缺口（用流程补，不是用代码）**：New API 的 top_ups 状态只有
-- pending/success/failed/expired，**没有退款态**。一笔已退款的充值在这里仍是
-- success，仍然可开票。因此：**手动退款之后必须去管理端冻结该资金批次**
-- （freeze-payment 或 manual-cap-adjustment）。这条写进了 PRODUCTION-RUNBOOK。

DROP TRIGGER IF EXISTS newapi_verified_dual_control_guard ON funding_lots;
DROP FUNCTION IF EXISTS enforce_newapi_verified_dual_control();

-- 历史数据：把已经满足证据判据的待核验批次提上来。
--
-- 判据与处理器里那条逐字一致（source_processor.go 的 candidateVerification）：
-- 状态成功 + 有完成时刻 + 金额为正。source_status 存的是 "candidate:" + 上游
-- 状态，所以这里比 'candidate:success'。
--
-- current_cap_minor 设成 original_minor，与人工核验路径（funding.go 的
-- current_cap_minor=PaidMinor）同义；verified_cash_minor 同样设成它，与资格
-- 路径的推导（consumption.go：verified 时 verifiedCash = currentCap）一致，
-- 不是这里发明的数。
--
-- **不动 consumed_cash_minor**：那是消耗分配的结果，由资格投影按真实用量推
-- 导。因此本次回填之后，历史批次的"可开票额"仍取决于已消耗现金——想让历史
-- 消耗回填上去，需要另外跑一次资格重投影，见 handoff。
UPDATE funding_lots fl
SET verification_state='verified',
    current_cap_minor=fl.original_minor,
    verified_cash_minor=fl.original_minor,
    updated_at=now()
FROM source_instances si
WHERE si.id=fl.source_instance_id
  AND si.source_type='newapi'
  AND fl.verification_state='pending'
  AND fl.refund_frozen=false
  AND fl.source_status='candidate:success'
  AND fl.completed_at IS NOT NULL
  AND fl.original_minor > 0
  -- 已经分配出去的不能被这条回填改小：cap 只会等于 original，
  -- 但保险起见仍然要求它容得下已预留与已开票的部分。
  AND fl.original_minor >= fl.reserved_minor + fl.issued_minor;
