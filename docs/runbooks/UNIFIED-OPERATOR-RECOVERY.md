# 统一操作器重入与恢复

## 重复切换（P0-3）

同一 `state_root` 已有 `COMMITTED`、`PREPARED`、`ROLLING_BACK`、`ROLLBACK_FAILED` 或未知状态时，再次执行 `cutover.sh` 会在任何外部操作前返回非零；不会停服务，也不会覆盖 `deployment-record.json`。成功切换不是可重复执行的 no-op，包装脚本不得把拒绝退出码当作成功。

需要恢复时，使用原配置调用 `rollback.sh --config <原配置绝对路径>`。原 source HEAD、manifest、配置绑定及 snapshot 校验继续生效。只有完整回滚写成 `ROLLED_BACK` 后，才允许重新执行完整切换。未知或损坏的恢复记录保持拒绝，不能靠清空 state_root 重新开始。

切换前的失败和重复拒绝只写本次 `cutover-<时间戳>/history/`；它们不再替换持久恢复记录。首次 preflight 失败可能没有 `deployment-record.json`，应查本次 history，而不是推断已有可用回滚快照。旧版留下且不含 snapshot 的 `PREFLIGHT_FAILED` 可以重新执行完整 preflight。已包含 snapshot 的记录仍须按恢复状态处理。
