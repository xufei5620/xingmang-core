# XM-ERRCODE-NOTFOUND：Action handler 不该说「这个 Action 没注册」

- **status:** implemented，未上线。**含一处对外可见的状态码变化**（404 → 412）。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-ERRCODE-AUDIT 提交），
  基线 `21082d9`。
- **来源：** XM-ERRCODE-AUDIT 自己留的 follow_up：「统一 `ErrNotFound` 的映射」。

## 调查改变了结论

我原本以为这是一道「404 还是 412，二选一」的跨包约定题，可能要负责人拍板。
**扫完全仓之后不是。**

`CodeNotRegistered` 的对外字符串是 **`ACTION_NOT_REGISTERED`**，内核之外有
**14 处**在用它。逐处核过之后是一条干净的分界：

| 用在哪 | 处数 | 例子 | 判断 |
|---|---|---|---|
| **Query（读）路径** | 13 | `GET /actions/runs/{id}` 的「执行记录不存在」、`platformusers` 的「没有这条用户记录」、`requestlog`、`payments`、`platform_channels` | **对的**。GET 一个不存在的资源回 404 本来就是 HTTP 的标准答案 |
| **Action handler** | **1** | `finance/channel_binding_actions.go` 的「binding resource not found」 | **错的**，见下 |

所以不需要选约定——需要修的是那唯一一处。

## 为什么那一处是错的

Action 走的是 `POST /api/v1/actions/{id}/versions/{v}/execute`。**这个端点存在，
这个 Action 也注册着**，不存在的只是请求体里指名的那个对象。

回一个 `ACTION_NOT_REGISTERED`（404）等于告诉调用方「平台上没有这个操作」——
拿到它的人会去查部署、查路由、查 Action 是不是没注册上，而真正的原因只是
他给的 `service_id` 在库里没有。`ActionRun.ErrorCode` 也会跟着记错，事后按
错误码统计时把一次参数错误算成一次 Action 缺失。

改成 `PRECONDITION_FAILED`（412），与另外三个 Action handler 对「参数指名的
对象不存在」的既有做法一致（assurance 的声明、credentials 的 credential_ref、
alerts 的告警——最后那条是上一片刚补的）。

## 改了什么

- `finance/channel_binding_actions.go`：`ErrNotFound` → `CodePreconditionFailed`
  （文案不变，它本来就准确）。
- `action/errors.go`：给 `CodeNotRegistered` 写清契约——Query 路径可以用，
  **Action handler 不要用**，并说明为什么。这是这一片最可能防住下次重犯的
  那一行。
- 测试见下。

**对外可见的变化**：移除/设置渠道绑定时，若 `service_id` 或绑定不存在，
HTTP 从 **404 `ACTION_NOT_REGISTERED`** 变成 **412 `PRECONDITION_FAILED`**。
前端不受影响（不按这些码分支，只显示 message）。

## 测试

- **`finance/binding_error_mapping_test.go`（改 1 例）** —— 跟随 404→412。
- **`finance/channel_binding_notfound_integration_test.go`（新增 2 条）**
  - `TestRemoveUnknownServiceIsNotActionNotRegistered` —— 从 **Action 入口**
    经真内核打进来；断码、断「不是 ACTION_NOT_REGISTERED」、**断文案逐字**。
  - `TestBindingActionRejectsMissingPermission` —— 本包此前只在 definition
    层面断言过 `Permission` 字段的字符串，那证明的是「声明写对了」，不是
    「内核真的按它拒绝」。声明与执行是两段代码。

### 变异验证，以及一次被抓出来的假绿

| 变异 | 结果 |
|---|---|
| 退回 `CodeNotRegistered` | 红（入口测试 + 映射单测） |
| handler 六处 `bindingActionError` 退回裸返回 | 红（入口测试） |

**入口测试的第一版是假绿的，值得写下来。** 我最初用「一个真实的 service +
一个不存在的 channel_id」来构造，看着很合理。跑变异——**没红**。

诊断出来：`ChannelInventoryGate{}` 的零值（`Observations == nil`）会在读到
store 之前就返回 `ErrBindingPrecondition`，而它**也**映射成 412。于是那条
用例是靠「渠道清单没就绪」这个完全不同的原因绿的，把修复回退掉它照样过。

改成用一个库里没有的 `service_id`——`store.GetService` 在 gate **之前**被调用，
且走 `bindingActionError`——才真正打到那条分支。两个变异随即都红。

这是本 session 第四次撞上同一类：**正向断言要问一句「旧实现下它会不会照样绿」**。
前三次分别是 router.test.tsx 的标题断言、alerts 的映射单测、以及这一次。

门禁：`go test -p 1 -count=1 ./...` 全绿、`go vet ./...` 退出 0、
`gofmt` 干净、`scripts/check-governance.sh` 退出 0、
`gitleaks protect --staged` 无泄漏。前端未改。

## 顺带记录的一处设计（没改）

`channelBindingRemoveHandler` 对 `gate.Verify` 返回的 `ErrNotFound` 是
**刻意容忍**的（`if err := gate.Verify(...); err != nil && !errors.Is(err, ErrNotFound)`）：
一条已经从上游清单里消失的渠道，绑定仍然应该能被移除。这是对的，本片没有动它。

## risks

- `CodeNotRegistered` 的字符串对那 **13 处读路径也算不上贴切**——一个运维在
  日志里看到 `ACTION_NOT_REGISTERED` 而事由是「用户记录不存在」，同样会愣一下。
  本片**没有改**它：那是对外契约变更（错误码字符串出现在每个 404 的响应体里），
  14 个调用点，值得单独一片并确认没有消费方按这个字符串分支。
- 本片只改了 finance 这一处 Action handler。**没有**逐个复查其余三个包的
  Action handler 里是否还有别的「参数指名的对象不存在」路径走了别的码——
  上一片（XM-ERRCODE-AUDIT）已经过了它们的 `domainError` 表，但没有把每个
  handler 里的每一条 `return nil, err` 都跟到源头。

## follow_ups

- **`ACTION_NOT_REGISTERED` 这个字符串该不该改**（见 risks 第 1 条）。两个方向：
  ① 直接改成 `NOT_FOUND` 之类——一处改动，但改的是对外契约；
  ② 新增一个 `CodeResourceNotFound = "NOT_FOUND"` → 404，把 13 处读路径迁过去，
  `CodeNotRegistered` 只留给内核——更干净但改动面大。
  **建议 ②**，且先确认没有消费方（前端已确认不按码分支，但服务器端的日志
  告警规则没查过）。
- 其余三个包的 Action handler 里逐条跟 `return nil, err` 的来源（risks 第 2 条）。
- 上一片留的两条仍开着：`docs/modules/action/README.md` 的正文补充、
  alerts 的 Silence 相关 `return nil, err` 逐处核实。
