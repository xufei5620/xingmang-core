# 统一操作器重入与恢复

## 重复切换（P0-3）

同一 `state_root` 已有 `COMMITTED`、`PREPARED`、`ROLLING_BACK`、`ROLLBACK_FAILED` 或未知状态时，再次执行 `cutover.sh` 会在任何外部操作前返回非零；不会停服务，也不会覆盖 `deployment-record.json`。成功切换不是可重复执行的 no-op，包装脚本不得把拒绝退出码当作成功。

需要恢复时，使用原配置调用 `rollback.sh --config <原配置绝对路径>`。原 source HEAD、manifest、配置绑定及 snapshot 校验继续生效。只有完整回滚写成 `ROLLED_BACK` 后，才允许重新执行完整切换。未知或损坏的恢复记录保持拒绝，不能靠清空 state_root 重新开始。

切换前的失败和重复拒绝只写本次 `cutover-<时间戳>/history/`；它们不再替换持久恢复记录。首次 preflight 失败可能没有 `deployment-record.json`，应查本次 history，而不是推断已有可用回滚快照。旧版留下且不含 snapshot 的 `PREFLIGHT_FAILED` 可以重新执行完整 preflight。已包含 snapshot 的记录仍须按恢复状态处理。

## 崩溃残锁与互斥（P1-4）

操作器在同一受控、本机 `state_root` 中保持两个不同职责的文件：

- `operator.guard` 是永久保留的内核互斥载体。Linux 使用 `flock(LOCK_EX|LOCK_NB)`，Windows 使用 `msvcrt.locking(LK_NBLCK)`。锁句柄不传给子进程，进程退出或主机重启会由 OS 释放；**不要 unlink、替换或复制正在使用的 guard 文件**，否则会破坏同一 inode 上的互斥。不要把 state_root 放到共享/NFS/SMB 目录或多机共用。
- `operator.lock` 只保存公开的 host、boot、PID namespace、PID、进程创建身份和随机 token。它不是凭据。正常结束时只有相同 owner 字节才能移除；释放无法核实时退出非零并保留记录。

切换、回滚、D 和 cleanup 都先取得同一内核锁。未取得即拒绝，不检查或删除别人的 owner 文件。取得之后，只有同主机的已变化 boot，或同 boot/namespace 下明确退出、PID 创建身份已不同，才自动接管；旧 owner 原字节另存为 `operator.lock.stale.<随机值>.json`。活跃进程、PID 重用后的新进程和重启前旧 PID 由创建身份与 boot 一起区分，不按文件年龄猜测。

Linux 使用 `/etc/machine-id`、内核 boot_id、PID namespace 和 `/proc/<pid>/stat` 启动 tick；缺失 proc 行仍需 signal 0 证明确实不存在，不能把 hidepid/权限拒绝当作进程死亡。Windows 在本机固定 CIM 查询中读取 CSName/LastBootUpTime，并通过进程 handle、GetProcessTimes 创建 FILETIME 和进程终止信号判断。

安全未知会拒绝：旧版只有 PID 的记录、写入中断造成的空/损坏 JSON、读权限不足、身份查询失败、跨主机/无法核对的 namespace、symlink/junction/hardlink。此时不要盲删锁或重建 state_root；先由目标主机负责人核对该主机/namespace 上的操作进程与其外部作业是否已结束，保存原锁、guard 与 deployment-record 的证据，再安排受控恢复。仅确认 PID 当前不存在，不能证明旧 PID-only 文件的 boot/创建身份。此实现不自动覆盖这些未知输入，也不把锁已释放等同于服务回滚完成。

本地回归使用真实 Windows 子进程竞争与 kill，原 CLI/快照/回滚消费链实际执行；Docker 和 HTTP 是测试边界，没有服务器或容器恢复实证。Linux boot/PID namespace 与权限分支为合成 OS 输入测试，不冒称本轮重启过服务器。

接口依据：[Python msvcrt](https://docs.python.org/3.14/library/msvcrt.html)、[Python fcntl](https://docs.python.org/3.14/library/fcntl.html)、[Windows GetProcessTimes](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getprocesstimes)、[Win32_OperatingSystem](https://learn.microsoft.com/en-us/windows/win32/cimwin32prov/win32-operatingsystem)。
