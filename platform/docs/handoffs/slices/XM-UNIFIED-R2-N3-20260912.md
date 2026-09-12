# XM-UNIFIED-R2-N3 — 显式角色映射完整性

对应 CR-0010 统一进程切换 R2 的 N-3。基线为 `6f229b3576315db461014ca883d23265d3c32544`。

显式 `XM_AUTH_ROLE_SCOPES` 若遗漏任一平台默认角色或该角色默认 scope，预检立即拒绝并报告准确差项。管理员精确角色、`finance.read`、C1 逐行计数、全覆盖 TOTP 和至少一名可登录管理员的联合校验保留。额外自定义角色及显式额外 scope 保留；不自动合并默认表，不修改运行时授权函数、员工角色、数据库或依赖。

默认基线通过 `platform/cmd/export-role-scope-defaults` 直接调用 `rolepermissions.DefaultRoleScopeMap()` 导出为 `deploy/unified/audit/default_role_scopes.py`。该生成物带源文件行尾归一化 SHA256，并受 Go 测试和操作器制品清单绑定；部署预检无需 Go 或公网。再生成和公开 JSON 导出命令见 `deploy/unified/audit/README.md`。

本地证据在 `G:/xingmang/logs/unified-review-fixes-r2-20260912/N-3/`，每次命令保留 UTC 起止、原退出码及 stdout/stderr SHA256：

- `01-RED.json`：原实现接受不完整显式配置，新增失败用例实际 exit 1，原因为未抛出预检拒绝。
- `03-GREEN.json`：15 个角色预检测试通过；逐一缺失所有默认角色和所有默认 scope 均拒绝；自定义角色保留。
- `MUTATIONS.json`：6 个有效变异均 exit 1，分别覆盖移除完整性调用、移除角色键检查、移除 scope 检查、生成表删权限、真实默认函数增加角色但不再生成、错误拒绝自定义角色。修改后源码逐字节恢复。
- `13-RESTORED-REHEARSAL.json`：全部 171 个操作器测试 exit 0；`14-RESTORED-GO.json`：导出器及角色权限包 `go test -count=1` exit 0；`15-RESTORED-AUDIT.json`：全部 22 个审计测试 exit 0。

早期 `02-GREEN.json` 的 sibling 导入接线失败已保留，不计入通过；绝对路径加载修复后重跑如上。服务器 C1/C2、D/E、制品重建与完整发布门禁由主任务在集成后的 HEAD 执行，本切片不声称线上或真实切换验收。
