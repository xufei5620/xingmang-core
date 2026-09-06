# XM-SUB2API-0_2-MATRIX：兼容矩阵声明 Sub2API 0.2

- **status:** implemented，未上线。**纯代码改动**：一个常量、一个新测试文件、
  一处清单记录。不改生产、不改上游、不动数据库。
- **branch:** `ai/claude/XM-SUB2API-0_2-MATRIX`（自 `release/v0.1-launch`）。
- **前一片：** `XM-UPSTREAM-VERSION-ALERT`（R6 `upstream.version.changed`）。
  R6 让这件事变得显眼，这一片把它修好。

## 为什么现在必须改

`connectors/sub2api` 的兼容矩阵一直是 `["0.1"]`。生产在 **2026-09-05 17:45 CST**
就地升级到了 **0.2.1**——Sub2API 后台的"点击更新"是替换容器里的 `/app/sub2api`
二进制，镜像标签不变，所以从外面看不出来。

矩阵停在 `"0.1"` 的后果不是一句提示：`Supported` 是**采集链路的判据**。连接器
对真实上游一律判假，而判假的原因（"我们没核对过这个版本"）与真正的不兼容长得
一模一样。上一片刚落地的 R6 会在升级后第一次评估时把它喊出来——喊的是实话，
但喊的是我们自己的矩阵陈旧，不是上游坏了。

## 先核对，再声明

矩阵自己的注释要求「加条目之前必须逐字段核对源码」。照做了，对着只读参考克隆
`K:/sub2api-src @ v0.2.1`，把本连接器**实际调用**的七条路由与它们读的每个字段
过了一遍：

| 端点 | 连接器读什么 | v0.2.1 |
| --- | --- | --- |
| `/admin/system/version` | `version` | `gin.H{"version"}` 未变 |
| `/admin/dashboard/stats` | `total_users` / `active_users` / `stats_stale` / `stats_updated_at` | 四个都在 |
| `/admin/dashboard/trend` | `trend[].date` / `.cost` | `TrendDataPoint` 未变 |
| `/admin/payment/dashboard` | `daily_series[].date` / `.amount` / `.count` | `DailyStats{Date, Amount CurrencyAmounts, Count}`——**仍是 0.1.183 起的币种 map**，本包 `currencyBuckets` 早已同时吃 map 与标量 |
| `/admin/payment/orders` | 11 个字段 | 逐个仍在（含 `user_email`、`refund_amount`） |
| `/admin/accounts` | 22 个字段 | 逐个仍在 |
| `/admin/accounts/:id` 今日 | `requests` / `cost` | `AccountStats` 未变 |

响应信封 `{code, message, data}` 也未变（`backend/internal/pkg/response`）。
一处未变都没有——0.1.183 → 0.2.1 对**我们读的这个面**是纯加法。

## 改了什么

`connectors/sub2api/client.go`：`SupportedUpstreamVersions` 从 `["0.1"]` 改成
`["0.1", "0.2"]`，注释里把上面这张核对表逐条写进去（将来谁想加 0.3 就知道
"逐字段核对"具体是核对哪七条）。**保留 0.1**：回滚到旧二进制时采集不能跟着判假。

**矩阵条目写到 major.minor，不写补丁位。** 写成 `"0.2.1"` 会让 0.2.2 判假——
上游打个补丁我们就停止采集，而补丁版之间这些字段没变。前缀匹配本来就覆盖
0.2.x 全线。

`docs/inventory/managed-systems.yaml`：`client_verified_against` 记上 0.2.1；
另加 `observed_binary_version`。**没有动 `detected_version`**——它的定义是"只填
探测出来的值"，而 0.2.1 是在部署主机上看容器内文件得到的，不是 `Version()` 跑
出来的。够格决定矩阵，不够格填进探测字段。这两件事分开记，是为了不让一次代码
阅读伪装成一次观测。

## 测试

新增 `connectors/sub2api/version_matrix_test.go` 三条。**它们钉的是矩阵这个值，
不是 `versionSupported` 的算法**——`amount_test.go` 那张表用的是局部矩阵，改导出
的常量不会让它变红，于是矩阵陈旧一直没有测试会说话：

- `CoversProduction`：`0.2.1` / `v0.2.1` / `0.2.0` / `0.2.9-rc.1` / `0.2` 必须判真；
  `0.1.179` / `0.1.183` / `0.1.133` 也必须判真（回滚不能失去采集）。
- `RejectsUnverifiedLines`：`0.3.0` / `1.0.0` / `10.1.0` / `""` 必须判假——矩阵是
  白名单，不是"大于等于就行"。
- `AreMinorLevelEntries`：每个条目恰好一个点，防止将来有人写成补丁位。

**做过变异验证**（[[absence-assertions-must-be-mutation-checked]] 的教训）：把矩阵
改回 `["0.1"]` 跑一次，`CoversProduction` 如期变红并指名 `"0.2.1"`；改回来再跑，
绿。这条断言不是恒真的。

门禁：`gofmt`（无输出）、`go vet ./connectors/...`、
`go test -p 1 -count=1 ./...` 全绿、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 无泄漏。前端未触及，未跑前端三条。

## 上线

**没有单独的上线动作**，随下一个平台 RC 一起发。发之后第一次连接器探测就会把
`supported` 从 false 翻成 true。

## follow_ups

- **注册表里那份 `supported_upstream_versions` 是死数据。** `registry.connector.create`
  Action 收它、`db/migrations/000001` 存它，但全仓库没有一处**读**它做判断
  （只有 store 的读写两处）。今天真正生效的是 Go 常量。生产那行大概率还写着
  `["0.1"]`，改它不影响任何行为——所以这一片没改；但"库里一个值看起来是判据、
  其实不是"本身值得收敛：要么让探测去比它，要么把这一列删掉。
- `detected_version` 仍是「未探测」。平台侧至今没有 Sub2API 生产凭据，
  `Version()` 一次都没跑过。凭据到位后第一件事就是跑它并回填。
- 工作树里有个 `mock-invoice-console.log`（798 字节，早前一次 node 跑失败留下的
  报错日志，脚本已不存在），未跟踪、未被 gitignore。不属于这一片，没有动它。
