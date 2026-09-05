# XM-INV-NEGATIVE-DEFICIT：负余额带金额上报，可解释的透支不再进入待对平

> 状态：设计冻结（产品负责人 2026-09-06 拍板「B+C」）。实现按 `docs/handoffs/XM-INV-NEGATIVE-DEFICIT.md`。
> 依据：账号 12（`acdcdce9`）"反复进入待定"的机制分析（ACCEPTANCE-LOG 2026-09-06 条目）、
> `docs/handoffs/XM-INV-OVERAGE-CARRY-FORWARD.md`（透支结转到下一笔现金充值）、
> `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md` §3(A)/(B)。

## 0. 问题

两条源都是"边用边扣、允许透支"。用户把余额用到零后，最后一笔请求把上游余额扣成负数；下一笔充值先抵扣这笔负数。
`XM-INV-OVERAGE-CARRY-FORWARD` 之后，账本侧已经能把这笔透支（`ShortfallUnits`，落在
`source_account_eligibility_state.non_invoiceable_overage_units`）结转到下一笔现金充值，所以充值一到，
`ExpectedBalance` 就重新跟上上游。

问题出在**透支到充值之间的窗口**：

1. 桥接函数把负余额报成 `balance_service_units="0"` + `balance_negative=true`，**不带金额**
   （`contracts/*-economic-projection-grants.postgresql.sql` 的 `balances_v4('rows')`）。
2. 评估器一见 `balance_negative` 就无条件判 `negative_frozen` 并进入
   `not_invoiceable_pending_reconciliation`（`consumption.go` `evaluatePendingBalanceEvidenceTx` 的
   `if item.balanceNegative` 分支），哪怕投影当时算出的预期余额恰好就是 0 且透支额 `ShortfallUnits` 已知。
3. 退出待对平要连续两次 `matched` 的**新**证据；余额不变就不产生新检查点（快照去重），只有充值才解开。

于是小额用户的每个"用光→透支→充值"周期都要在待定态里走一遭。产品负责人的裁定：**B**（评估器把"投影
预期余额已到零、且透支额已知"时的上游负余额视为可解释，不冻结）**+ C**（桥接把负余额的具体金额带上来，
评估器按金额精确比对，而不是只看一个布尔）。

## 1. 目标与非目标

**目标**

- 上游负余额的金额（"透支额"）随每个余额检查点上报、落库、结转、参与评估。
- 当上报的透支额与投影在该时刻**全部**未能分配到任何池的用量（新增 `UnallocatedUnits`：结转中的现金债务 + 不可开票的短缺，全部求和；超额列 `ShortfallUnits` 仍只记最老一笔）**逐单位相等**时，该证据评为 `matched`，
  账户不进入待对平；已在待对平的账户以它计一次连续匹配。
- 金额不等（无论哪个方向）仍按今天的口径进入待对平；金额未知（升级前封存的证据）也按今天的口径处理。

**非目标**

- 不改分配算法、结转规则、正向抖动（blip）逻辑、退出阈值 N=2、任何金额阈值。
- 不改 Sub2API/NewAPI 源码；桥接函数属于我方安装在上游库里的 `invoice_bridge` schema，是我方契约。
- 不做历史评估记录的改写：`balance_checkpoint_evaluations` 是不可变事实。

## 2. 数据面（C）

### 2.1 桥接函数（两条源，函数名与版本号不变）

`invoice_bridge.sub2api_balances_v4('rows')` 的输出行新增一列：

```
deficit_service_units = ((GREATEST(-balance,0)*100000000)::numeric(78,0))::text
```

NewAPI 对称：`GREATEST(-quota,0)::numeric(78,0)::text`。保留原有两列不变。
`operation='contract'` 与 `'health'` 不动，因此 `projection_contract` 与 `configuration_hash` 不变——
`checkLiveEconomicContract` 不会判"漂移"，**不需要重做切换清单**。旧代理通过
`jsonb_to_record(...) AS projected(user_id bigint,balance_service_units text,balance_negative boolean)`
读取，多出的键被忽略，先装桥接后滚代理是安全的。

`check-db` 钉住函数体哈希（`agents/cmd/source-agent-prod/main.go` `expectedBridgeRoutineHash`：
`sha256(prosrc)`）。两条源的 `StreamBalances` 常量随本片更新；哈希的计算规则已验证：取 SQL 文件里
`AS $bridge$` 之后到 `$bridge$;` 之前的全部文本（含开头换行）做 sha256，与当前常量逐字节一致。本片新增
一个单元测试，从两份 contracts 文件重算五流哈希并与常量比对，之后任何人改桥接 SQL 都会当场红。

### 2.2 代理

- `BalanceSnapshotRow` 新增 `DeficitServiceUnits string \`json:"deficit_service_units,omitempty"\``。
  `balanceSnapshotID` 对整个快照 JSON 取 sha256；`omitempty` 保证升级前封存的基线快照（没有这个键）
  重新哈希得到同一个 `BaselineSnapshotID`，切换清单继续可验。
- 记录定义扩为 `user_id bigint,balance_service_units text,balance_negative boolean,deficit_service_units text`；
  两处扫描（`captureReconciliation`、切换基线捕获）读取第四列。列为 NULL（桥接尚未安装本片 SQL）
  → 视为"桥接未升级"，fail-closed 并点名 `install-economic`。
- 行校验：`deficit_service_units` 匹配 `serviceUnitsPattern`；不变量 `balance_negative == (deficit != "0")`，
  且 `balance_negative → balance_service_units == "0"`（沿用）。
- 增量判定 `changedBalanceRowsAndRetirements`：`prior.DeficitServiceUnits != row.DeficitServiceUnits` 也算变化，
  否则透支额从 −3 变到 −5 不会产生新检查点。退休规则不变（负余额行永不省略）。
- `BalanceCheckpointPayload` 新增同名字段（`omitempty`）；`balanceCheckpointProjection` 填入；
  `validateRecord` 的 `EntityBalanceCheckpoint` 分支加同一组不变量。
- `inspect-pending` 输出带上该字段。

### 2.3 API 与库

- `balanceCheckpointPayload.DeficitServiceUnits *string \`json:"deficit_service_units,omitempty"\``。
  两侧都是 `DisallowUnknownFields`，所以 **api 先于代理升级**（roll-forward 本就先重启 api 再重建代理）。
  字段缺席（升级前封存、按铁律逐字重放的待发批次）合法，落库为 NULL=未知。
- 迁移 `0026_balance_checkpoint_deficit.sql`：
  - `balance_reconciliation_checkpoints` 与 `balance_carry_forward_proofs` 各加
    `deficit_service_units NUMERIC(78,0)`，`CHECK (deficit_service_units IS NULL OR deficit_service_units>=0)`，
    `CHECK (deficit_service_units IS NULL OR (deficit_service_units>0)=balance_negative)`；原
    `CHECK (NOT balance_negative OR balance_service_units=0)` 保留。
  - `enforce_balance_carry_forward_proof_contract()` 重建：`prior_deficit IS DISTINCT FROM NEW.deficit_service_units`
    也视为"prior actual is invalid"。
- `BalanceCheckpointObservation` 新增 `DeficitServiceUnits *string`；`ObserveBalanceCheckpoint` 校验不变量并落库；
  结转证明候选（`carryCandidates`）与插入语句复制 prior 的透支额；评估项查询两个分支都选出该列。

## 3. 评估面（B）

`evaluatePendingBalanceEvidenceTx` 的 `if item.balanceNegative` 分支改为：

| 透支额 | 计算 | 结果 |
|---|---|---|
| 已知（非 NULL） | `difference = (−deficit) − (ExpectedBalance − UnallocatedUnits)`，其中两项来自 `buildEligibilityProjectionTx(item.asOf)` | `difference == 0` → **`matched`**（写评估、按既有闭包推进待对平退出计数）；`≠ 0` → `negative_frozen` + `enterPendingReconciliationTx(UNKNOWN_NEGATIVE_BALANCE)`，detail 写清"上报 −X，预期 −Y" |
| 未知（NULL） | 不比对 | 今天的口径：`negative_frozen` + 待对平；detail 注明"金额未知（升级前证据）" |

- 评估行的 `expected_service_units` 仍写 `ExpectedBalance`（非负，语义不变），`difference_service_units`
  写上表的有符号差值（该列本就无非负约束）。
- 正向抖动（`pending` blip）的确认条件保持 `!item.balanceNegative`，负余额证据不参与抖动确认。
- `difference > 0`（上游欠得比账本少：管理员在透支账户上手动加额度之类）**不**走正向分支合成
  `UNKNOWN_POSITIVE`：合成的非现金额度不会抵扣结转债务，差值会一直存在。它进入待对平，由人看 detail。
  这是刻意的保守选择，边界情况先可见再说。
- 退出规则不变。账号 12 类的循环因此在**进入**这一步就被消灭：透支检查点评 `matched`，账户保持 `active`；
  充值到账后结转抵扣，下一检查点同样 `matched`。

## 4. 文档与可见性

- `docs/SOURCE-SYNC-PROTOCOL.md`：负余额"报零 + 布尔"改为"报零 + 布尔 + 透支额"。
- `contracts/source-agent-batch.v3.schema.json`：`balance_checkpoint.payload` 增加可选 `deficit_service_units`。
- `docs/ELIGIBILITY-OPERATIONS.md`：`negative_frozen` 的三种来源（金额不符 / 金额未知 / 其它），以及
  "上报 −X 预期 −Y" 的读法。
- `docs/PRODUCTION-RUNBOOK.md`：本片的上线顺序（§5）。
- 管理端账本 detail 已展示评估 detail 字符串，本片不改前端。

## 5. 上线顺序与回滚

1. 负责人以集群超级用户运行维护包装器 `install-economic`（sub2api、newapi 各一次）。旧代理不受影响。
2. 常规 RC：迁移 0026 → api → 代理。评估器改动 → **发布前影子评估必做**（评估器改动纪律）。
3. 金丝雀：账号 12 的下一个余额检查点应评 `matched`；`negative_frozen` 新增行应为 0。
4. 回滚：上一 RC 目录（旧 api 拒收带新字段的批次？——不会：旧 api 也是 `DisallowUnknownFields`，
   **会拒收**。因此回滚代理必须与回滚 api 同时进行，即整个 RC 回滚；桥接函数多出的列对旧代理无害，不必回滚）。

## 6. 测试

- 代理：带/不带第四列的扫描；旧基线 JSON 重哈希不变；透支额变化触发增量；校验不变量；`inspect-pending`。
- API：载荷解码（缺席/存在/非法）；`ObserveBalanceCheckpoint` 不变量；迁移；结转证明复制透支额且触发器拒绝
  不一致；评估器四种结果（相等→matched；上游多欠→negative_frozen；上游少欠→negative_frozen；未知→negative_frozen）；
  **账号级隔离**：一个透支账户被解释为 matched 时，另一账户的评估逐字节不变。
- 哈希常量测试（§2.1）。
- 影子评估：修复前备份 + 差分（单次/分块）一致。
