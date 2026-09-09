# XM-DBTEST-CLEANUP：让「共享测试表是干净的」重新成为不变量

- **status:** implemented，未上线。**只改测试**，一行生产代码都没动。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-ERRCODE-NOTFOUND 提交），
  基线 `90f79a3`。
- **来源：** `docs/handoffs/slices/XM-DBTEST-FIX0.md` 的 follow_up 第 1 条：
  「建议给 `internal/platform/credentials` 的 DB 集成测试补一条『结束后清理』
  的惯例」。

## 实测先于判断

follow_up 说的是「建议补」，没说今天是不是真的在漏。**先测了再改**：

```
跑 credentials 集成测试之前：connector_config=0
跑完之后：                    connector_config=1     ← 漏
跑 assurance 之后：           connector_config=0
```

所以精确的结论比 follow_up 的措辞更窄：**只有 `credentials` 漏，`assurance`
不漏**（它开跑前那条定向 DELETE 顺手把别人留的也清了，自己也不留）。

顺带核了另外两个：`jobs/connector_config_integration_test.go` 与
`jobs/assurance_probe_ui_compat_integration_test.go` **两头都清**，且后者的注释
写着它就是被这个坑咬过才改成两头清的。

| 文件 | 开跑前清 | 结束后清 |
|---|---|---|
| `jobs/connector_config_integration_test.go` | ✓ | ✓ |
| `jobs/assurance_probe_ui_compat_integration_test.go` | ✓ | ✓ |
| `assurance/store_integration_test.go` | ✓ | ✗（但实测不留行） |
| `credentials/store_integration_test.go` | ✓ | **✗ ← 本片修的** |

## 为什么值得修

每个包都在开跑前自己清一遍，所以今天**不会**红。但那把「共享表是干净的」
从一条**不变量**降成了「谁记得防谁就没事」——

`jobs/connector_config_integration_test.go` 的注释是现成的证词：

> 其他包（如 `internal/platform/credentials` 的 `TestConnectorConfigSetAndList`）
> 同样会写 `('sub2api','staging')`，且不保证在它之后清干净。所以本用例既要在
> 开跑前把自己要用的键位清空一遍，也要在结束时清理。

而 `assurance_probe_ui_compat` 那份注释记了实际的事故：单跑时蒙混过关，
`go test -p 1 ./...` 时读到别的包留下的 `mode=real` 行，把 fake 模式该绕过的
闸误判成 real 模式，`run@1` 被 `global_kill_switch_off` 拒绝。

**下一个写集成测试的人不该需要知道这段历史。** 补上收尾之后，
「表是空的」重新是可以依赖的前提。

## 改了什么

`credentials/store_integration_test.go` 的 `credentialPool`：把那条 TRUNCATE
抽成一个闭包，**开跑前调一次、`t.Cleanup` 再挂一次**。

用 `t.Cleanup` 而不是 `defer`：`credentialPool` 是被每个用例各自调用的，
`t.Cleanup` 挂在调用它的那个用例上，逐个用例收尾比整包收一次更干净。

收尾里的 `pool.Exec` 用独立的 10s 超时 context：用例自己的 ctx 这时候可能已经
取消了。失败用 `t.Errorf` 而不是 `t.Fatalf`——收尾阶段 `Fatalf` 会把失败归到
一个已经跑完的用例上，读起来莫名其妙。

## 验证（实测 + 变异）

| 步骤 | 结果 |
|---|---|
| 修前跑一遍 | `connector_config` 0 → **1** |
| 修后跑一遍 | 0 → **0** |
| 变异：删掉 `t.Cleanup(cleanup)` 那一行 | 0 → **1**（回到漏的状态） |

**没有为它加一条常驻测试**，这是刻意的：要断言「本包跑完之后表是空的」，
测试就得排在包内所有用例之后，那要么靠字母序（脆）、要么靠 `TestMain`
（把一条卫生检查提升成包级基础设施）。这件事用一次实测 + 一次变异确认更诚实，
结论写在这里。真正的回归防线是那两个 `jobs` 测试——它们仍然两头设防，
真漏了它们会红。

门禁：`go test -p 1 -count=1 ./...` 全绿、`go vet ./...` 退出 0、
`gofmt` 干净、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 无泄漏。前端未改。

## 顺带核过、没有改的

`internal/platform/httpapi/finance_test.go` 的 gofmt 问题（XM-DBTEST-FIX0 与
XM-USERS-V2-REAL 两个切片都点名「留给下一个碰它的人」）——**今天已经是干净的**，
`gofmt -l` 无输出。大概是在中途某一片里被顺手修掉了。两处 follow_up 可以划掉。

## follow_ups

- `assurance/store_integration_test.go` 实测不留行，但它**没有**收尾；哪天它的
  用例开始写 `core.connector_config` 就会变成第二个 credentials。补一条对称的
  收尾是廉价的，但本片没有动它——**没有证据的改动也是改动**，留给下一个碰
  那个文件的人。
- XM-DBTEST-FIX0 还有一条开着：「未迁移库上的 `connector_config_integration_test.go`
  兜底建表路径值得单独跑一次验证」，需要一个**没跑过迁移**的库，本片没有造。
