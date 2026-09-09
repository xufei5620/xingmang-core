# Platform User Read Contract v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** 建立稳定的 platform-scoped UserRef 与精确 GetUser，并以 capability 分片逐步接入每日消费、Key 元数据和 reqlog 用户请求，同时保持 payment/invoice 独立和诚实不可用。

**Architecture:** platformusers v1 ListUsers 保持兼容；v2 在同一 Go 包中增加 UserDetailReader、DailyUsageReader、KeyMetadataReader 三个可选只读接口。HTTP 页面并列调用 platformusers 与 reqlog 等领域 Query，各自保留 scope、分页、retention、freshness 和 coverage；不建跨域聚合 Connector，不默认建本地 link 表。

**Tech Stack:** Go 1.27 support line、Chi HTTP、React 19、TypeScript 5.9、TanStack Query 5、Vitest 4、现有 Connector/Action error/Registry capability 框架。

**Spec:** docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md

## Global Constraints

- CORE_APPROVAL 只授权 Task 1~3；批准 core/Fake 绝不授权任何 real client。
- 本计划本身不授权 real client、真实实例访问、新 scope、角色映射、迁移、Action、推送或部署。
- 开始前必须确认 XM-B001 commit d1998e7 与 XM-C002 commit 855557f 已合入目标 release，并从当时 release 新建/重建任务 worktree。
- 读取走 Query；不得修改 Sub2API、NewAPI、reqlog 或 invoice 原始业务表。
- UserRef 唯一性域是 environment + platform + opaque ID；禁止 username、email、masked email、token prefix join。
- 金额只用整数最小单位；HTTP 金额用十进制字符串；未知与零必须分离。
- 每个结果独立携带 source、observed_at、watermark、is_partial、retention/coverage。
- platform.users.read 扩大字段面必须显式审批；platform.user_keys.read 是新 scope，未批不得注册。
- request.read/request.content.read 保持分离；正文成功披露的审计必须 fail closed。
- CR-0002/CR-0003 未冻结前不接 invoice；M3 未批准前不接 payment/recharge。
- Sub2API /api/v1/admin/users/:id/usage 是已证实的 mock 路由，任何 real 代码必须机械拒绝。
- capability 只能在对应 Reader 已实现并通过 contracttest 的同一 PR 中由该 client 声明；禁止预告 capability。
- 默认不建本地 link 表；需要时另立迁移、Action、证据与回滚设计。
- 实现代码使用 go fmt，不使用裸 gofmt；不升级依赖。

---

## Worktree / PR Dependency DAG

每片开始时运行 git fetch origin，并从表中“基线”对应的已合入
origin/release/v0.1-launch 新建独立 worktree。前序只存在未合并分支时不得开工。

| Task | 分支 / PR | 基线 | 必须存在的独立授权 | 阻断隔离 |
|---|---|---|---|---|
| 1 | ai/codex/XM-C-USER1-userref-codec | B001/C002 已合入的 release | CORE_APPROVAL | 可独立于所有 real 门 |
| 2 | ai/codex/XM-C-USER2-user-v2-fake | Task 1 已合入的 release | CORE_APPROVAL | 可独立于所有 real 门 |
| 3 | ai/codex/XM-C-USER3-get-user-query | Task 2 已合入的 release | CORE_APPROVAL | 可只交付 Fake/core |
| 4 | ai/codex/XM-C-USER4-sub2-user-real | Task 3 已合入的 release | SUB2_REAL_APPROVAL + 被引用的 Sub2 证据哈希 | 门未过只阻断 Task 4 |
| 5 | ai/codex/XM-C-USER5-newapi-user-real | Task 3 已合入的 release | NEWAPI_REAL_APPROVAL + 被引用的 NewAPI 证据哈希 | 门未过只阻断 Task 5 |
| 6 | ai/codex/XM-C-USER6-daily-usage | Task 3 已合入的 release | DAILY_USAGE_APPROVAL | 只做 Fake/core，不依赖 Task 4/5 文件 |
| 7 | ai/codex/XM-C-USER7-key-metadata | Task 3 已合入的 release | KEY_SCOPE_APPROVAL | 只做 Fake/core，不依赖 Task 4/5 文件 |
| 8 | ai/codex/XM-C-USER8-reqlog-userref | Task 3 与 C002 已合入的 release | REQLOG_USERREF_APPROVAL + 被引用的 reqlog 证据哈希 | 门未过只阻断 Task 8 |

Task 4 与 Task 5 彼此无基线依赖；Task 6/7 不等待 Task 4/5。任何 real、Daily、
Key 或 reqlog 批准事件都必须在对应 Task 的 Issue/PR 中逐字引用，不得用
CORE_APPROVAL、spec merge 或真实样本存在来推定。

### Task 1: UserRef、canonical codec 与 v2 核心类型

**Worktree / PR:** ai/codex/XM-C-USER1-userref-codec；基线为包含 B001/C002 的
最新 release；仅需 CORE_APPROVAL。

**Files:**
- Create: contracts/connectors/platformusers.read.v2.md
- Create: contracts/testdata/platform-user-ref-v1.json
- Create: connectors/platformusers/userref.go
- Create: connectors/platformusers/userref_test.go
- Create: connectors/platformusers/exact_lookup.go
- Create: connectors/platformusers/exact_lookup_test.go
- Create: connectors/platformusers/contract_v2.go
- Modify: connectors/platformusers/contract_test.go
- Modify: web/apps/admin-web/src/api/users.ts
- Modify: web/apps/admin-web/src/api/users.test.ts

**Interfaces:**
- Consumes: platformusers.SourceSub2API / SourceNewAPI、Amount、CountValue、Period、User、registry.Capability。
- Produces: UserRef、EncodeUserIDSegment、DecodeUserIDSegment、EvidenceSnapshot、GetUserQuery、UserDetail、UserDetailReader、ErrLookupIncomplete、ScanExactUser。Task 1 不向任何 client capability 列表加条目。

- [ ] **Step 1: 写 TS/Go 共用 golden fixture**

~~~json
[
  {"id":"u_10241","segment":"u-755f3130323431"},
  {"id":".","segment":"u-2e"},
  {"id":"..","segment":"u-2e2e"},
  {"id":"tenant/a?slot=#1% ready","segment":"u-74656e616e742f613f736c6f743d233125207265616479"},
  {"id":"中文","segment":"u-e4b8ade69687"}
]
~~~

- [ ] **Step 2: 写失败的 Go codec 与 UserRef 测试**

~~~go
type goldenCase struct {
    ID      string "json:\"id\""
    Segment string "json:\"segment\""
}

func loadGolden(t *testing.T) []goldenCase {
    t.Helper()
    body, err := os.ReadFile(filepath.Join(
        "..", "..", "contracts", "testdata", "platform-user-ref-v1.json"))
    if err != nil {
        t.Fatal(err)
    }
    var out []goldenCase
    if err := json.Unmarshal(body, &out); err != nil {
        t.Fatal(err)
    }
    return out
}

func TestUserIDSegmentGoldenRoundTrip(t *testing.T) {
    cases := loadGolden(t)
    for _, tc := range cases {
        got, err := platformusers.EncodeUserIDSegment(tc.ID)
        if err != nil || got != tc.Segment {
            t.Fatalf("Encode(%q) = %q, %v; want %q", tc.ID, got, err, tc.Segment)
        }
        id, err := platformusers.DecodeUserIDSegment(tc.Segment)
        if err != nil || id != tc.ID {
            t.Fatalf("Decode(%q) = %q, %v; want %q", tc.Segment, id, err, tc.ID)
        }
    }
}

func TestUserRefRejectsCrossPlatformAndEmptyID(t *testing.T) {
    for _, ref := range []platformusers.UserRef{
        {},
        {Platform: "cpa", ID: "1"},
        {Platform: "sub2api", ID: ""},
    } {
        if err := ref.Validate(); err == nil {
            t.Fatalf("UserRef %+v should fail", ref)
        }
    }
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/platformusers -run "TestUserIDSegment|TestUserRef" -v

Expected: FAIL because UserRef/codec symbols and golden loader do not exist.

- [ ] **Step 4: 实现最小 codec 与核心类型**

~~~go
const maxUserIDBytes = 512

type UserRef struct {
    Platform string
    ID       string
}

func (r UserRef) Validate() error {
    source, err := ParseSource(r.Platform)
    if err != nil || source != r.Platform || r.ID == "" || len([]byte(r.ID)) > maxUserIDBytes {
        return fmt.Errorf("invalid platform user ref")
    }
    return nil
}

type UserDetailReader interface {
    GetUser(context.Context, GetUserQuery) (UserDetail, error)
}

var ErrLookupIncomplete = errors.New("platformusers: exact lookup incomplete")
~~~

EncodeUserIDSegment 必须使用 UTF-8 字节小写 hex；Decode 必须严格校验 u- 前缀、
偶数 hex、512-byte 上限、合法 UTF-8 和 decode→encode canonical equality。

- [ ] **Step 5: 补非法 codec 测试并运行 GREEN**

非法集合固定为：空串、u-、raw-id、u-0、u-gg、u-C2A0、u-c0af、解码后超过
512 bytes。运行：

Run: go test ./connectors/platformusers -run "TestUserIDSegment|TestUserRef" -v

Expected: PASS。

- [ ] **Step 6: 写并实现可执行的 exact scan 边界测试**

~~~go
type ExactPageFetcher func(
    context.Context, string,
) (UserPage, error)

func ScanExactUser(
    ctx context.Context,
    userID string,
    maxPages int,
    fetch ExactPageFetcher,
) (User, error)

func scanFixture(
    t *testing.T,
    ctx context.Context,
    userID string,
    maxPages int,
    pages []UserPage,
) (User, error) {
    t.Helper()
    index := 0
    expectedCursor := ""
    return ScanExactUser(ctx, userID, maxPages,
        func(_ context.Context, cursor string) (UserPage, error) {
            if cursor != expectedCursor || index >= len(pages) {
                t.Fatalf("cursor=%q expected=%q index=%d", cursor, expectedCursor, index)
            }
            page := pages[index]
            index++
            expectedCursor = page.NextCursor
            return page, nil
        })
}

func TestScanExactUserBoundaries(t *testing.T) {
    t.Run("later page exact hit", func(t *testing.T) {
        pages := []UserPage{
            {Users: []User{{ID: "u_1-copy"}}, NextCursor: "c2"},
            {Users: []User{{ID: "u_1"}}},
        }
        got, err := scanFixture(t, context.Background(), "u_1", 5, pages)
        if err != nil || got.ID != "u_1" {
            t.Fatalf("got=%+v err=%v", got, err)
        }
    })
    t.Run("exhausted means not found", func(t *testing.T) {
        _, err := scanFixture(t, context.Background(), "u_1", 5,
            []UserPage{{Users: []User{{ID: "u_1-copy"}}}})
        if !errors.Is(err, ErrNotFound) {
            t.Fatalf("err=%v", err)
        }
    })
    t.Run("page limit and cursor cycle are incomplete", func(t *testing.T) {
        for _, pages := range [][]UserPage{
            {
                {NextCursor: "c2"}, {NextCursor: "c3"}, {NextCursor: "c4"},
                {NextCursor: "c5"}, {NextCursor: "c6"},
            },
            {{NextCursor: "same"}, {NextCursor: "same"}},
        } {
            _, err := scanFixture(t, context.Background(), "u_1", 5, pages)
            if !errors.Is(err, ErrLookupIncomplete) {
                t.Fatalf("err=%v", err)
            }
        }
    })
    t.Run("cancel propagates", func(t *testing.T) {
        ctx, cancel := context.WithCancel(context.Background())
        cancel()
        _, err := ScanExactUser(ctx, "u_1", 5,
            func(ctx context.Context, _ string) (UserPage, error) {
                return UserPage{}, ctx.Err()
            })
        if !errors.Is(err, context.Canceled) {
            t.Fatalf("err=%v", err)
        }
    })
}
~~~

scanFixture 在 exact_lookup_test.go 内按 pages 顺序返回，并断言调用方原样传回
上一页 NextCursor。ScanExactUser 必须在每页先检查 ID 全等，再判断 exhaustion、
上限或循环；maxPages<=0 当场返回 ErrLookupIncomplete。

Run: go test ./connectors/platformusers -run TestScanExactUserBoundaries -v

Expected: RED 后实现最小 helper，再运行得到 PASS。

- [ ] **Step 7: 让 TypeScript codec 读取同一份 golden**

~~~ts
import goldenText from "../../../../../contracts/testdata/platform-user-ref-v1.json?raw";

const golden = JSON.parse(goldenText) as Array<{ id: string; segment: string }>;

it.each(golden)("round-trips canonical user IDs", ({ id, segment }) => {
  expect(encodePlatformUserIdSegment(id)).toBe(segment);
  expect(decodePlatformUserIdSegment(segment)).toBe(id);
});
~~~

TypeScript 保留 B001 的严格 canonical 判定，并增加与 Go 相同的 512-byte 上限。

Run: pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/api/users.test.ts

Expected: PASS。

- [ ] **Step 8: 同步 v2 contract 文档并格式化**

contracts/connectors/platformusers.read.v2.md 必须逐字冻结 UserRef、codec、
GetUser 404/incomplete 判据、能力清单、scope 门和跨域非目标。

Run: go fmt ./connectors/platformusers

- [ ] **Step 9: 提交 Task 1**

~~~bash
git add contracts/connectors/platformusers.read.v2.md contracts/testdata/platform-user-ref-v1.json connectors/platformusers/userref.go connectors/platformusers/userref_test.go connectors/platformusers/exact_lookup.go connectors/platformusers/exact_lookup_test.go connectors/platformusers/contract_v2.go connectors/platformusers/contract_test.go web/apps/admin-web/src/api/users.ts web/apps/admin-web/src/api/users.test.ts
git commit -m "feat(platformusers): define v2 user identity contract"
~~~

### Task 2: v2 Fake 与 capability contracttest

**Worktree / PR:** ai/codex/XM-C-USER2-user-v2-fake；基线为 Task 1 已合入的
release；仅需 CORE_APPROVAL，不等待任何 real 样本。

**Files:**
- Create: connectors/platformusers/fake_v2.go
- Create: connectors/platformusers/contracttest/v2_suite.go
- Create: connectors/platformusers/contract_v2_test.go
- Modify: connectors/platformusers/contract_v2.go
- Modify: connectors/platformusers/fake.go

**Interfaces:**
- Consumes: Task 1 UserRef/GetUserQuery/UserDetail/UserDetailReader。
- Produces: FakeClient.GetUser、CapabilityUserDetailRead、source-specific
Fake V2Capabilities、共享 RunV2 contract suite。detail capability 与 Fake
UserDetailReader 在本 PR 同时出现。

- [ ] **Step 1: 写失败的 exact/fuzzy/incomplete capability 测试**

~~~go
func TestFakeV2GetUserIsExact(t *testing.T) {
    clock := func() time.Time {
        return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
    }
    c := platformusers.NewFakeClient(platformusers.SourceSub2API, clock)
    got, err := c.GetUser(context.Background(), platformusers.GetUserQuery{
        Ref: platformusers.UserRef{Platform: "sub2api", ID: "u_10241"},
    })
    if err != nil || got.Ref.ID != "u_10241" {
        t.Fatalf("GetUser = %+v, %v", got, err)
    }
    _, err = c.GetUser(context.Background(), platformusers.GetUserQuery{
        Ref: platformusers.UserRef{Platform: "sub2api", ID: "u_1024"},
    })
    if !errors.Is(err, platformusers.ErrNotFound) {
        t.Fatalf("partial ID must not match: %v", err)
    }
}
~~~

- [ ] **Step 2: 运行 RED**

Run: go test ./connectors/platformusers -run "TestFakeV2|TestV2Contract" -v

Expected: FAIL because FakeClient does not implement UserDetailReader.

- [ ] **Step 3: 实现 Fake GetUser 与能力差异**

Sub2API/NewAPI Fake 在本片都只声明 detail_read。daily/key 标识分别留到
Task 6/7 与 Reader 同片加入。GetUser 必须遍历 Fake 的 source-scoped 数据并仅接受
ID 全等；找不到返回 ErrNotFound，不回落第一条。

~~~go
func (c *FakeClient) GetUser(_ context.Context, q GetUserQuery) (UserDetail, error) {
    if err := q.Ref.Validate(); err != nil || q.Ref.Platform != c.source {
        return UserDetail{}, connectorBadSource(q.Ref.Platform)
    }
    period, err := q.period(c.now())
    if err != nil {
        return UserDetail{}, connectorBadPeriod(err)
    }
    for _, raw := range fakeUsers {
        if raw.id == q.Ref.ID {
            return c.detail(raw, period), nil
        }
    }
    return UserDetail{}, ErrNotFound
}
~~~

- [ ] **Step 4: 建共享 V2 suite**

RunV2 必须断言：

- UserRef source mismatch 被拒；
- exact not found；
- Snapshot source/observed 非空；
- Capabilities 全部可由 registry.ParseCapability 解析且 IsWrite=false；
- NewAPI 不出现 Sub2API-only capability；
- UserDetail 不含 plaintext email/full key 字段。

- [ ] **Step 5: 运行 connector 全包 GREEN**

Run: go test ./connectors/platformusers/... -v

Expected: PASS。

- [ ] **Step 6: 提交 Task 2**

~~~bash
git add connectors/platformusers/fake_v2.go connectors/platformusers/contracttest/v2_suite.go connectors/platformusers/contract_v2_test.go connectors/platformusers/contract_v2.go connectors/platformusers/fake.go
git commit -m "test(platformusers): add executable v2 fake contract"
~~~

### Task 3: GetUser Service、HTTP Query 与 B001 前端切换

**Worktree / PR:** ai/codex/XM-C-USER3-get-user-query；基线为 Task 2 已合入的
release；必须引用 CORE_APPROVAL。该批准只允许 Fake/core，不允许 real。

**Files:**
- Create: internal/platform/platformusers/detail_service.go
- Create: internal/platform/platformusers/detail_service_test.go
- Create: internal/platform/httpapi/users_detail.go
- Create: internal/platform/httpapi/users_detail_test.go
- Modify: internal/platform/httpapi/router.go
- Modify: cmd/platform-api/main.go
- Modify: web/apps/admin-web/src/api/users.ts
- Modify: web/apps/admin-web/src/api/users.test.ts
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx
- Modify: web/apps/admin-web/src/router.test.tsx

**Interfaces:**
- Consumes: Task 1/2 UserDetailReader、DecodeUserIDSegment、platformusers.ScopeRead。
- Produces: Service.Get、GET /api/v1/platforms/{platform}/users/{canonicalUserId}、
getPlatformUser 前端 helper。

- [ ] **Step 1: 确认审批硬门**

执行前在任务/PR 证据中必须存在产品+安全明确文本：允许 platform.users.read
覆盖 GetUser 基础事实。若不存在，停止 Task 3，不注册路由、不修改 RoleScopeMap。

- [ ] **Step 2: 写失败的 Service/HTTP 测试**

~~~go
type fakeUserDetailQuerier struct {
    called bool
    detail connusers.UserDetail
    err    error
}

func (f *fakeUserDetailQuerier) Get(
    _ context.Context, _ platformusers.DetailInput,
) (connusers.UserDetail, error) {
    f.called = true
    return f.detail, f.err
}

func serveUserDetail(
    t *testing.T, q *fakeUserDetailQuerier, target string,
) *httptest.ResponseRecorder {
    t.Helper()
    r := chi.NewRouter()
    r.Get("/platforms/{platform}/users/{userID}", GetPlatformUserHandler(q))
    req := httptest.NewRequest(http.MethodGet, target, nil)
    rec := httptest.NewRecorder()
    r.ServeHTTP(rec, req)
    return rec
}

func TestGetUserRouteRejectsNonCanonicalBeforeQuery(t *testing.T) {
    q := &fakeUserDetailQuerier{}
    rec := serveUserDetail(t, q, "/platforms/sub2api/users/raw-id")
    if rec.Code != http.StatusNotFound || q.called {
        t.Fatalf("status=%d called=%v", rec.Code, q.called)
    }
}

func TestGetUserIncompleteIsNot404(t *testing.T) {
    q := &fakeUserDetailQuerier{err: platformusers.ErrLookupIncomplete}
    rec := serveUserDetail(t, q, "/platforms/sub2api/users/u-755f3130323431")
    if rec.Code == http.StatusNotFound {
        t.Fatal("incomplete lookup must not become not found")
    }
}

func TestGetUserErrorMappingIsFrozen(t *testing.T) {
    cases := []struct {
        err      error
        status   int
        code     string
        retryable bool
    }{
        {platformusers.ErrNotFound, http.StatusNotFound, "ACTION_NOT_REGISTERED", false},
        {platformusers.ErrLookupIncomplete, http.StatusBadGateway, "EXECUTION_FAILED", true},
    }
    for _, tc := range cases {
        q := &fakeUserDetailQuerier{err: tc.err}
        rec := serveUserDetail(t, q,
            "/platforms/sub2api/users/u-755f3130323431")
        assertErrorEnvelope(t, rec, tc.status, tc.code)
        if (rec.Code >= 500) != tc.retryable {
            t.Fatalf("status=%d retryable=%v", rec.Code, tc.retryable)
        }
    }
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./internal/platform/platformusers ./internal/platform/httpapi -run "TestGetUser|TestUserDetail" -v

Expected: FAIL because Service.Get and handler do not exist.

- [ ] **Step 4: 实现 Service 与安全 HTTP DTO**

~~~go
type DetailInput struct {
    Platform   string
    UserID     string
    Day        string
    Granularity string
}

func (s *Service) Get(ctx context.Context, in DetailInput) (connusers.UserDetail, error) {
    _, client, err := s.resolve(in.Platform)
    if err != nil {
        return connusers.UserDetail{}, err
    }
    reader, ok := client.(connusers.UserDetailReader)
    if !ok {
        return connusers.UserDetail{}, action.NewError(
            action.CodeAdvancedControlsRequired, "用户详情真实读取尚未接入", nil)
    }
    // normalize period, call exact reader, translate ErrNotFound/incomplete separately
}
~~~

HTTP DTO 必须逐字段白名单，金额使用十进制字符串，包含 ref/period/snapshot/capabilities，
不直接 JSON 序列化 Connector 结构体。Service 错误映射逐字冻结：

~~~go
case errors.Is(err, connusers.ErrNotFound):
    return action.NewError(action.CodeNotRegistered, "没有这条用户记录", err)
case errors.Is(err, connusers.ErrLookupIncomplete):
    return action.NewError(
        action.CodeExecutionFailed, "用户精确查找未完成，请重试", err)
~~~

由现有 HTTP error mapper 得到 ACTION_NOT_REGISTERED/HTTP 404 与
EXECUTION_FAILED/HTTP 502；Task 3 复用稳定 action.CodeNotRegistered，
不新增用户域 error code，也不修改全局 mapper；
context.Canceled/DeadlineExceeded 原样传播，不得进入上述两支。

- [ ] **Step 5: 写前端 RED，证明只调用精确 endpoint**

~~~ts
it("uses canonical GetUser and never scans the list", async () => {
  await getPlatformUser("sub2api", "u-755f3130323431");
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/api/v1/platforms/sub2api/users/u-755f3130323431",
  );
  expect(String(fetchMock.mock.calls[0]?.[0])).not.toContain("?q=");
});
~~~

Run: pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/api/users.test.ts src/pages/PlatformUserDetailPage.test.tsx src/router.test.tsx

Expected: FAIL because the page still calls lookupPlatformUserExact.

- [ ] **Step 6: 实现前端切换并保留状态语义**

删除详情页的 5x200 客户端扫描调用；列表 Query 与 B001 canonical route 保留。
HTTP 404 显示 definite not found；501 显示 unavailable；502/取消显示错误/卸载；
source/freshness/Fake/zero-vs-unknown 继续逐项可见。充值、invoice、Key、reqlog 面板
仍保持 unavailable，不在本任务偷接。

- [ ] **Step 7: 运行 Task 3 门禁**

Run:

~~~text
go test ./internal/platform/platformusers ./internal/platform/httpapi -v
pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck
pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/api/users.test.ts src/pages/PlatformUserDetailPage.test.tsx src/router.test.tsx
~~~

Expected: 全部 PASS。

- [ ] **Step 8: 提交 Task 3**

~~~bash
git add internal/platform/platformusers/detail_service.go internal/platform/platformusers/detail_service_test.go internal/platform/httpapi/users_detail.go internal/platform/httpapi/users_detail_test.go internal/platform/httpapi/router.go cmd/platform-api/main.go web/apps/admin-web/src/api/users.ts web/apps/admin-web/src/api/users.test.ts web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx web/apps/admin-web/src/router.test.tsx
git commit -m "feat(platformusers): add exact user detail query"
~~~

### Task 4: Sub2API real GetUser（真实样本门控）

**Worktree / PR:** ai/codex/XM-C-USER4-sub2-user-real；基线为 Task 3 已合入的
release；与 Task 5 无依赖。必须同时满足 SUB2_REAL_APPROVAL 和 Sub2 证据门。

**Files:**
- Create: docs/evidence/EV-2026-08-28-platformusers-sub2api-v2-shape.md
- Create: connectors/platformusers/testdata/sub2api/users_page.redacted.json
- Create: connectors/platformusers/testdata/sub2api/user_detail.redacted.json
- Create: connectors/platformusers/sub2api_v2.go
- Create: connectors/platformusers/sub2api_v2_test.go
- Modify: connectors/platformusers/client.go
- Modify: connectors/platformusers/contracttest/v2_suite.go
- Modify: cmd/platform-api/main.go

**Interfaces:**
- Consumes: UserDetailReader/GetUserQuery、现有 RealConfig 四道只读闸。
- Produces: Sub2API real exact GetUser 和 detail_read capability。

- [ ] **Step 1: 检查不可跳过的证据门**

必须同时存在两个独立条件：

1. 产品+安全签署的 SUB2_REAL_APPROVAL，文本明确写出 Task 4、实例/版本、
   允许实现的 Reader、证据文件路径和 SHA-256；CORE_APPROVAL、spec merge 或
   “允许收集样本”均不算 real 授权。
2. 对应真实样本证据已经过审并可追溯。

证据必须包含：

- 脱敏的 /api/v1/admin/users 响应形状和实例版本；
- 分页、ID、状态、余额 scale、created/last-active 的解释；
- 人工已完成 AdminComplianceGuard 的记录；
- 证明选用端点不是 mock 的源码/实例证据。

批准或任一证据缺失即停止 Task 4；Task 1~3/5~8 不受影响。平台不得为完成此任务
发 compliance POST。

- [ ] **Step 2: 写 RED fixture contract test**

~~~go
func TestSub2APIV2GetUserUsesExactIDAndFixedPoint(t *testing.T) {
    body, err := os.ReadFile(filepath.Join(
        "testdata", "sub2api", "users_page.redacted.json"))
    if err != nil {
        t.Fatal(err)
    }
    got, err := parseSub2UsersPage(
        body,
        platformusers.UserRef{Platform: "sub2api", ID: "u_10241"},
        time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC),
    )
    if err != nil || got.Ref.ID != "u_10241" || !got.User.Balance.Known {
        t.Fatalf("detail=%+v err=%v", got, err)
    }
}

func TestSub2APIV2PermanentlyRejectsMockUsageRoute(t *testing.T) {
    if sub2V2AllowedPath("/api/v1/admin/users/u_10241/usage") {
        t.Fatal("known mock route must remain blocked")
    }
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/platformusers -run TestSub2APIV2 -v

Expected: FAIL because sub2api_v2 client/allowlist do not exist.

- [ ] **Step 4: 实现最小 read-only parser**

只解 approved DTO 字段；邮箱立即 MaskEmail；decimal 金额走定点解析，不用
ParseFloat；若没有原生 exact endpoint，按已验证分页完整扫描，页/时间预算到顶返回
ErrLookupIncomplete。上游正文不进错误链。

本任务固定以下纯解析与 allowlist 边界，网络层只负责取 body/meta：

~~~go
func parseSub2UsersPage(
    body []byte, ref UserRef, observedAt time.Time,
) (UserDetail, error)

func sub2V2AllowedPath(path string) bool
~~~

- [ ] **Step 5: 运行共享 v2 contracttest 和全包测试**

Sub2 client 只有在 GetUser Reader 实现和共享 contracttest 同时 PASS 的本 PR 中
才声明 platformusers.user.detail_read；不得预先修改 capability 列表。

Run: go test ./connectors/platformusers/... -run "TestSub2APIV2|TestV2" -v

Expected: PASS。

- [ ] **Step 6: 提交 Task 4**

~~~bash
git add docs/evidence/EV-2026-08-28-platformusers-sub2api-v2-shape.md connectors/platformusers/testdata/sub2api/users_page.redacted.json connectors/platformusers/testdata/sub2api/user_detail.redacted.json connectors/platformusers/sub2api_v2.go connectors/platformusers/sub2api_v2_test.go connectors/platformusers/client.go connectors/platformusers/contracttest/v2_suite.go cmd/platform-api/main.go
git commit -m "feat(platformusers): add evidence-gated Sub2API user reader"
~~~

### Task 5: NewAPI real GetUser（真实样本门控）

**Worktree / PR:** ai/codex/XM-C-USER5-newapi-user-real；基线为 Task 3 已合入的
release；与 Task 4 无依赖。必须同时满足 NEWAPI_REAL_APPROVAL 和 NewAPI 证据门。

**Files:**
- Create: docs/evidence/EV-2026-08-28-platformusers-newapi-v2-shape.md
- Create: connectors/platformusers/testdata/newapi/users_page.redacted.json
- Create: connectors/platformusers/testdata/newapi/user_detail.redacted.json
- Create: connectors/platformusers/newapi_v2.go
- Create: connectors/platformusers/newapi_v2_test.go
- Modify: connectors/platformusers/client.go
- Modify: connectors/platformusers/contracttest/v2_suite.go
- Modify: cmd/platform-api/main.go

**Interfaces:**
- Consumes: UserDetailReader/GetUserQuery、NewAPI read-only target/path guard。
- Produces: NewAPI real exact GetUser，只声明真实证明的 capabilities。

- [ ] **Step 1: 检查 NewAPI 证据门**

必须先有产品+安全签署的 NEWAPI_REAL_APPROVAL，明确 Task 5、实例/版本、
允许实现的 Reader、证据路径与 SHA-256。CORE_APPROVAL、SUB2_REAL_APPROVAL、
spec merge 或真实样本存在均不授权 NewAPI real。

证据必须冻结 /api/user/ 的尾斜杠、分页 envelope、soft-delete、ID、quota、
created_at、last_login_at、status、quota_per_unit 和 PII 字段。必须记录管理员 token
经 CredentialRef 注入，以及 /api/user/token、/api/user/aff、epay notify 等 GET 写端点
仍在黑名单。批准或任一证据缺失即停止 Task 5；其他 Task 不受影响。

- [ ] **Step 2: 写 RED parser 与 PII 测试**

~~~go
func TestNewAPIV2IgnoresSoftDeletedAndMasksContact(t *testing.T) {
    body, err := os.ReadFile(filepath.Join(
        "testdata", "newapi", "users_page.redacted.json"))
    if err != nil {
        t.Fatal(err)
    }
    got, err := parseNewAPIUsersPage(
        body,
        platformusers.UserRef{Platform: "newapi", ID: "20031"},
        time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC),
    )
    if err != nil || got.User.EmailMasked == "" || strings.Contains(got.User.EmailMasked, "real@") {
        t.Fatalf("detail=%+v err=%v", got, err)
    }
}

func TestNewAPIV2WriteLikeGETsStayBlocked(t *testing.T) {
    for _, path := range []string{"/api/user/token", "/api/user/aff", "/api/user/epay/notify"} {
        if newAPIV2AllowedPath(path) {
            t.Fatalf("write-like GET allowed: %s", path)
        }
    }
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/platformusers -run TestNewAPIV2 -v

Expected: FAIL because newapi_v2 reader does not exist.

- [ ] **Step 4: 实现最小 NewAPI parser**

只解 UserDetail 所需字段；quota 先整数累加/转换，最后按动态 quota_per_unit 换算；
soft-deleted 用户不命中；未知状态映射 unknown；绝不解 password、github_id、
telegram_id、remark 或完整 token。

本任务固定以下纯解析与 allowlist 边界：

~~~go
func parseNewAPIUsersPage(
    body []byte, ref UserRef, observedAt time.Time,
) (UserDetail, error)

func newAPIV2AllowedPath(path string) bool
~~~

- [ ] **Step 5: 验证 capability 不继承**

在真实样本尚未证明 DailyUsage/Key 元数据前，NewAPI capabilities 只含
platformusers.user.detail_read。该 capability 只在本 PR 的 NewAPI UserDetailReader
实现与 contracttest 同时 PASS 后声明。运行：

Run: go test ./connectors/platformusers/... -run "TestNewAPIV2|TestV2" -v

Expected: PASS。

- [ ] **Step 6: 提交 Task 5**

~~~bash
git add docs/evidence/EV-2026-08-28-platformusers-newapi-v2-shape.md connectors/platformusers/testdata/newapi/users_page.redacted.json connectors/platformusers/testdata/newapi/user_detail.redacted.json connectors/platformusers/newapi_v2.go connectors/platformusers/newapi_v2_test.go connectors/platformusers/client.go connectors/platformusers/contracttest/v2_suite.go cmd/platform-api/main.go
git commit -m "feat(platformusers): add evidence-gated NewAPI user reader"
~~~

### Task 6: DailyUsage capability 与七日趋势

**Worktree / PR:** ai/codex/XM-C-USER6-daily-usage；基线为 Task 3 已合入的
release；必须引用 DAILY_USAGE_APPROVAL。只实现 contract/Fake/HTTP/UI，不依赖
Task 4/5，也不修改尚未存在的 real 文件。

**Files:**
- Create: connectors/platformusers/daily_usage.go
- Create: connectors/platformusers/daily_usage_test.go
- Modify: connectors/platformusers/contract_v2.go
- Modify: connectors/platformusers/fake_v2.go
- Modify: connectors/platformusers/contracttest/v2_suite.go
- Create: internal/platform/httpapi/users_daily_usage.go
- Create: internal/platform/httpapi/users_daily_usage_test.go
- Modify: internal/platform/httpapi/router.go
- Modify: web/apps/admin-web/src/api/users.ts
- Modify: web/apps/admin-web/src/api/users.test.ts
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx

**Interfaces:**
- Consumes: DailyUsageReader/DailyUsageQuery/DailyUsageSeries、Task 3 GetUser route。
- Produces: CapabilityUserDailyUsageRead、Fake DailyUsageReader、
GET /api/v1/platforms/{platform}/users/{canonicalUserId}/daily-usage?day=&days=7。

- [ ] **Step 1: 检查独立审批并锁定 Fake/core 范围**

必须有 DAILY_USAGE_APPROVAL，明确允许 platform.users.read 扩大到 DailyUsage
Fake/core。Task 6 不读取真实样本、不实现任何 real Reader；real 模式保持
unavailable。未来某个平台的 real DailyUsage 只能在新的独立授权 PR 中实现，并在
该 Reader 与 contracttest 同片通过后声明 capability。

- [ ] **Step 2: 写 RED coverage 测试**

~~~go
func TestDailyUsageDistinguishesZeroFromMissingDay(t *testing.T) {
    clock := func() time.Time {
        return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
    }
    c := platformusers.NewFakeClient(platformusers.SourceSub2API, clock)
    reader := any(c).(platformusers.DailyUsageReader)
    series, err := reader.DailyUsage(context.Background(), platformusers.DailyUsageQuery{
        Ref:  platformusers.UserRef{Platform: "sub2api", ID: "u_10241"},
        Day:  "2026-08-27",
        Days: 7,
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(series.Points) != 7 || series.Coverage.ExpectedDays != 7 {
        t.Fatalf("series=%+v", series)
    }
    if !series.Points[0].Consumed.Known || series.Points[0].Consumed.MinorUnits != 0 {
        t.Fatal("known zero must remain zero")
    }
    if series.Points[1].Consumed.Known || series.Coverage.Complete {
        t.Fatal("missing day must be unknown and incomplete")
    }
    if series.Coverage.CoveredDays != 6 {
        t.Fatalf("coverage=%+v; want exactly one unknown day", series.Coverage)
    }
}

func TestDailyUsageWindowAndCoverage(t *testing.T) {
    // 2026-08-27 16:30 UTC 已是 CST 2026-08-28。
    clock := func() time.Time {
        return time.Date(2026, 8, 27, 16, 30, 0, 0, time.UTC)
    }
    c := platformusers.NewFakeClient(platformusers.SourceSub2API, clock)
    reader := any(c).(platformusers.DailyUsageReader)

    for _, days := range []int{-1, 32} {
        _, err := reader.DailyUsage(context.Background(), platformusers.DailyUsageQuery{
            Ref: platformusers.UserRef{Platform: "sub2api", ID: "u_10241"},
            Days: days,
        })
        if err == nil {
            t.Fatalf("days=%d accepted", days)
        }
    }

    series, err := reader.DailyUsage(context.Background(), platformusers.DailyUsageQuery{
        Ref:  platformusers.UserRef{Platform: "sub2api", ID: "u_10241"},
        Day:  "2026-09-02",
        Days: 7,
    })
    if err != nil {
        t.Fatal(err)
    }
    if series.From != "2026-08-27" || series.To != "2026-09-02" {
        t.Fatalf("range=%s..%s", series.From, series.To)
    }
    if series.Coverage.CoveredDays > series.Coverage.ExpectedDays {
        t.Fatalf("coverage=%+v", series.Coverage)
    }
    wantComplete := series.Coverage.CoveredDays == series.Coverage.ExpectedDays &&
        !series.Snapshot.IsPartial
    if series.Coverage.Complete != wantComplete {
        t.Fatalf("coverage=%+v partial=%v", series.Coverage, series.Snapshot.IsPartial)
    }
}

func TestDailyUsageDefaultsToSevenCSTDays(t *testing.T) {
    clock := func() time.Time {
        return time.Date(2026, 8, 27, 16, 30, 0, 0, time.UTC)
    }
    c := platformusers.NewFakeClient(platformusers.SourceSub2API, clock)
    series, err := any(c).(platformusers.DailyUsageReader).DailyUsage(
        context.Background(),
        platformusers.DailyUsageQuery{
            Ref: platformusers.UserRef{Platform: "sub2api", ID: "u_10241"},
            Days: 0,
        },
    )
    if err != nil || series.To != "2026-08-28" ||
        series.Coverage.ExpectedDays != 7 || len(series.Points) != 7 {
        t.Fatalf("series=%+v err=%v", series, err)
    }
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/platformusers ./internal/platform/httpapi -run TestDailyUsage -v

Expected: FAIL because series implementation/handler do not exist.

- [ ] **Step 4: 实现 CST 闭区间与 page-level evidence**

days 只接受 1..31，空 day 由服务端按 +08:00 解释；Points 每日一条、升序；
缺日填 UnknownAmount 并降低 coverage；跨币种或部分日失败不能输出完整合计。
HTTP DTO 金额用字符串/null，返回 expected_days/covered_days/complete 和 Snapshot。
CapabilityUserDailyUsageRead 只由本 Task 的 Fake DailyUsageReader 声明；real clients
不修改、不声明。

- [ ] **Step 5: 写前端 RED/GREEN**

测试必须断言：

- Fake banner 在趋势旁；
- 七点齐全才画完整趋势；
- incomplete 显式提示缺几日；
- NewAPI 无 capability 时不复制 Sub2API 趋势；
- 不调用 reqlog、invoice、payment。

Run: pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/api/users.test.ts src/pages/PlatformUserDetailPage.test.tsx

Expected: PASS after minimal implementation。

- [ ] **Step 6: 提交 Task 6**

~~~bash
git add connectors/platformusers/daily_usage.go connectors/platformusers/daily_usage_test.go connectors/platformusers/contract_v2.go connectors/platformusers/fake_v2.go connectors/platformusers/contracttest/v2_suite.go internal/platform/httpapi/users_daily_usage.go internal/platform/httpapi/users_daily_usage_test.go internal/platform/httpapi/router.go web/apps/admin-web/src/api/users.ts web/apps/admin-web/src/api/users.test.ts web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx
git commit -m "feat(platformusers): add capability-gated daily usage series"
~~~

### Task 7: Key metadata 与独立 scope

**Worktree / PR:** ai/codex/XM-C-USER7-key-metadata；基线为 Task 3 已合入的
release；必须引用 KEY_SCOPE_APPROVAL。只实现 contract/Fake/HTTP/UI，不依赖
Task 4/5，也不修改尚未存在的 real 文件。

**Files:**
- Create: connectors/platformusers/key_metadata.go
- Create: connectors/platformusers/key_metadata_test.go
- Modify: connectors/platformusers/contract_v2.go
- Modify: connectors/platformusers/fake_v2.go
- Modify: connectors/platformusers/contracttest/v2_suite.go
- Create: internal/platform/httpapi/users_keys.go
- Create: internal/platform/httpapi/users_keys_test.go
- Modify: internal/platform/platformusers/permissions.go
- Modify: internal/platform/httpapi/router.go
- Modify: internal/platform/oidcauth/resolver_test.go
- Modify: web/apps/admin-web/src/api/config.ts
- Modify: web/apps/admin-web/src/api/users.ts
- Modify: web/apps/admin-web/src/api/users.test.ts
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx

**Interfaces:**
- Consumes: KeyMetadataReader/KeyMetadataQuery/KeyMetadataPage。
- Produces: CapabilityUserKeysMetadataRead、Fake KeyMetadataReader、
ScopeKeyMetadataRead = platform.user_keys.read 和 capability-gated keys Query。

- [ ] **Step 1: 检查双审批与真实样本门**

必须有产品+安全签署的 KEY_SCOPE_APPROVAL，明确 platform.user_keys.read 与角色
映射。Task 7 不实现 real Reader，不要求 Task 4/5 已存在；真实样本和 real Key
capability 由以后对应平台的独立授权 PR 处理。审批缺失即停止；不得先把新 scope
放进 DEFAULT_SCOPES 或默认 admin/staff。

- [ ] **Step 2: 写 RED secret/PII 反射测试**

~~~go
func walkJSONKeys(
    t *testing.T,
    value any,
    allowed map[string]bool,
    forbidden *regexp.Regexp,
) {
    t.Helper()
    switch node := value.(type) {
    case map[string]any:
        for key, child := range node {
            if forbidden.MatchString(key) && !allowed[key] {
                t.Fatalf("forbidden JSON key: %s", key)
            }
            walkJSONKeys(t, child, allowed, forbidden)
        }
    case []any:
        for _, child := range node {
            walkJSONKeys(t, child, allowed, forbidden)
        }
    }
}

func collectJSONStringValues(value any, out *[]string) {
    switch node := value.(type) {
    case string:
        *out = append(*out, node)
    case map[string]any:
        for _, child := range node {
            collectJSONStringValues(child, out)
        }
    case []any:
        for _, child := range node {
            collectJSONStringValues(child, out)
        }
    }
}

func TestKeyMetadataContainsNoSecretMaterial(t *testing.T) {
    typ := reflect.TypeOf(platformusers.KeyMetadata{})
    forbidden := regexp.MustCompile("(?i)full.?key|secret|credential|token.?hash|plaintext")
    for i := 0; i < typ.NumField(); i++ {
        if forbidden.MatchString(typ.Field(i).Name) {
            t.Fatalf("forbidden field: %s", typ.Field(i).Name)
        }
    }
}

func TestKeyPrefixIsAtMostEightCodePoints(t *testing.T) {
    if err := platformusers.ValidateKeyPrefix("123456789"); err == nil {
        t.Fatal("long prefix accepted")
    }
}

func TestKeyMetadataJSONHasNoCredentialKeysOrValues(t *testing.T) {
    dto, err := toKeyMetadataPageBody(platformusers.KeyMetadataPage{
        Items: []platformusers.KeyMetadata{{
            ID: "key-meta-1", Prefix: "sk-abcd", Status: "active",
        }},
    })
    if err != nil {
        t.Fatal(err)
    }
    body, err := json.Marshal(dto)
    if err != nil {
        t.Fatal(err)
    }
    var decoded any
    if err := json.Unmarshal(body, &decoded); err != nil {
        t.Fatal(err)
    }
    allowed := map[string]bool{"prefix": true}
    walkJSONKeys(t, decoded, allowed, regexp.MustCompile(
        "(?i)full.?key|secret|credential|token.?hash|plaintext|email|phone|tax|bank"))

    forbiddenValues := []string{
        strings.Join([]string{"complete", "key", "sentinel"}, "-"),
        "person@example.test",
        "secret://test/key-reader",
    }
    var values []string
    collectJSONStringValues(decoded, &values)
    for _, got := range values {
        for _, forbidden := range forbiddenValues {
            if strings.Contains(got, forbidden) {
                t.Fatalf("forbidden value leaked: %q in %q", forbidden, got)
            }
        }
    }
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/platformusers ./internal/platform/httpapi -run "TestKeyMetadata|TestListUserKeys" -v

Expected: FAIL because key types/handler/scope do not exist.

- [ ] **Step 4: 实现 metadata-only 契约与 stable pagination**

默认 limit=50、最大 200；cursor 绑定 UserRef；Prefix 最大 8 code point；
ID 不得可逆到完整 Key；status 未知映射 unknown；零时间对外 null；每页 Snapshot
必须存在。CapabilityUserKeysMetadataRead 只由本 Task 的 Fake KeyMetadataReader
声明；真实来源没实现 Reader 时不声明。共享 v2 contracttest 必须对每个未来 Reader
返回的实际 JSON 同时运行 key walker 与 value sentinel/模式扫描，不能只做 struct 反射。

- [ ] **Step 5: 注册 scope，保持默认角色保守**

~~~go
const ScopeKeyMetadataRead = "platform.user_keys.read"
~~~

resolver_test 必须证明 staff/admin 默认映射不因新增常量自动获得该 scope；只有审批
文本明确允许的部署角色通过显式配置获得。开发默认 scope 仅在审批明确要求本地演示
时增加，否则 config.ts 保持不含该 scope。

- [ ] **Step 6: 前端测试只显示元数据**

断言列只有 Prefix、状态、今日峰值、最后使用；DOM/请求快照中不存在完整 Key、
复制按钮或导出；403 显示缺 platform.user_keys.read，不回落到 v1 lone prefix。

- [ ] **Step 7: 运行 Task 7 门禁并提交**

Run:

~~~text
go test ./connectors/platformusers/... ./internal/platform/httpapi ./internal/platform/oidcauth -v
pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck
pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/api/users.test.ts src/pages/PlatformUserDetailPage.test.tsx
~~~

~~~bash
git add connectors/platformusers/key_metadata.go connectors/platformusers/key_metadata_test.go connectors/platformusers/contract_v2.go connectors/platformusers/fake_v2.go connectors/platformusers/contracttest/v2_suite.go internal/platform/httpapi/users_keys.go internal/platform/httpapi/users_keys_test.go internal/platform/platformusers/permissions.go internal/platform/httpapi/router.go internal/platform/oidcauth/resolver_test.go web/apps/admin-web/src/api/config.ts web/apps/admin-web/src/api/users.ts web/apps/admin-web/src/api/users.test.ts web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx
git commit -m "feat(platformusers): add scoped key metadata query"
~~~

### Task 8: reqlog 稳定 UserRef 与用户 usage 面板

**Worktree / PR:** ai/codex/XM-C-USER8-reqlog-userref；基线为 Task 3 与 C002
均已合入的 release；必须同时满足 REQLOG_USERREF_APPROVAL 和 reqlog 真实证据门。
不依赖 Task 4/5/6/7。

**Files:**
- Modify: contracts/connectors/reqlog.read.v1.md
- Modify: connectors/reqlog/contract.go
- Modify: connectors/reqlog/contracttest/suite.go
- Modify: connectors/reqlog/fake.go
- Modify: connectors/reqlog/client.go
- Modify: connectors/reqlog/client_contract_test.go
- Modify: internal/platform/requestlog/service.go
- Modify: internal/platform/requestlog/service_test.go
- Modify: internal/platform/httpapi/requests.go
- Modify: internal/platform/httpapi/requests_test.go
- Modify: web/apps/admin-web/src/api/requests.ts
- Modify: web/apps/admin-web/src/api/requests.test.ts
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx
- Modify: web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx

**Interfaces:**
- Consumes: platformusers.UserRef semantics、reqlog ScopeRead/ScopeContentRead。
- Produces: reqlog Summary.UserRef、ListFilter.UserRef、by-user metadata Query 和
reqlog.requests.by_user_read capability、Sub2API/NewAPI capability-gated
usage/request panel。capability 与 stable filter implementation/contracttest 同片声明。

- [ ] **Step 1: 检查 reqlog 真实关联门**

必须同时满足：

1. 产品+安全签署的 REQLOG_USERREF_APPROVAL（spec 审批项 6/原审批项 4），明确
   Task 8、允许变更 reqlog stable link/filter、证据路径与 SHA-256；
2. 真实 API/源码和脱敏 fixture 证明抄录记录或受信映射能返回 platform +
   source user ID，并冻结 retention/cursor/watermark/partial 语义。

CORE_APPROVAL、request.read 已存在、真实源码可读或只有 Username/TokenPrefix 都不算
通过。任一条件缺失即停止 Task 8，用户详情继续 unavailable，Task 1~7 不受影响。

- [ ] **Step 2: 写 RED anti-join 测试**

~~~go
func TestReqlogNeverAssociatesByEmailUsernameOrPrefix(t *testing.T) {
    users := []struct {
        ref  platformusers.UserRef
        user platformusers.User
    }{
        {
            ref: platformusers.UserRef{Platform: "sub2api", ID: "u_1"},
            user: platformusers.User{
                Username: "same", EmailMasked: "s***@example.test",
                TokenPrefix: "sk-abcd",
            },
        },
        {
            ref: platformusers.UserRef{Platform: "newapi", ID: "u_2"},
            user: platformusers.User{
                Username: "same", EmailMasked: "s***@example.test",
                TokenPrefix: "sk-abcd",
            },
        },
    }
    rows := []reqlog.RequestLogSummary{
        {Source: "sub2api", Username: "same", TokenPrefix: "sk-abcd"},
        {Source: "newapi", Username: "same", TokenPrefix: "sk-abcd"},
    }
    for _, candidate := range users {
        for _, row := range rows {
            if row.MatchesUser(candidate.ref) {
                t.Fatalf("display identity guessed a link: user=%+v row=%+v",
                    candidate.user, row)
            }
        }
    }
    if _, ok := reflect.TypeOf(reqlog.ListFilter{}).FieldByName("Email"); ok {
        t.Fatal("email filter must not become a user association path")
    }
}

func TestReqlogUserFilterRunsBeforePaginationAndStats(t *testing.T) {
    clock := func() time.Time {
        return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
    }
    c := reqlog.NewFake(reqlog.FakeOptions{Now: clock})
    ref := platformusers.UserRef{Platform: "sub2api", ID: "u_1"}
    page, err := c.ListRequests(context.Background(), reqlog.ListFilter{
        Source: "sub2api",
        User:   &ref,
        Limit:  2,
    })
    if err != nil {
        t.Fatal(err)
    }
    if page.Stats.RequestCount != 3 || len(page.Items) != 2 || page.NextCursor == "" {
        t.Fatalf("page=%+v", page)
    }
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/reqlog/... ./internal/platform/requestlog ./internal/platform/httpapi -run "TestReqlog.*User|TestListRequests.*User" -v

Expected: FAIL because reqlog has no stable user reference/filter.

- [ ] **Step 4: 扩展 reqlog DRAFT contract**

Summary 增加可空 UserRef；ListFilter 增加可空 UserRef。source 必须等于
UserRef.Platform；未关联记录保留在普通列表，但 by-user Query 永不命中。
filter 在 stats 和 cursor 之前执行；cursor 绑定 UserRef。若真实 API 只能在平台侧
后过滤，则 capability 保持 unsupported。

~~~go
func (s RequestLogSummary) MatchesUser(ref platformusers.UserRef) bool {
    return s.User != nil && *s.User == ref
}
~~~

reqlog.requests.by_user_read 只能在上述 Reader/filter、真实 parser 和共享 contracttest
同片通过后加入该 client 的 capability 列表。

- [ ] **Step 5: 保持权限与正文审计边界**

by-user metadata 仍要求 request.read；点击一行进入正文仍调用已有内容 Query，
要求 request.content.read，成功返回前写 request.content.viewed。不得把正文预取进
用户详情或 platformusers DTO。

- [ ] **Step 6: 前端接入两平台各自布局**

Sub2API usage Tab 显示按 UserRef 精确过滤的元数据；NewAPI 只填它原型中的“区间请求”
面板，不复制 Sub2API 的 recharge/invoice/keys Tabs。页面展示 reqlog retention、
source、freshness、partial/unlinked coverage；没有 stable capability 时保持 unavailable。

- [ ] **Step 7: 运行 Task 8 门禁**

Run:

~~~text
go test ./connectors/reqlog/... ./internal/platform/requestlog ./internal/platform/httpapi -v
pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck
pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/api/requests.test.ts src/pages/PlatformUserDetailPage.test.tsx
~~~

Expected: 全部 PASS，且负向测试证明没有 invoice/payment/key API 调用。

- [ ] **Step 8: 提交 Task 8**

~~~bash
git add contracts/connectors/reqlog.read.v1.md connectors/reqlog/contract.go connectors/reqlog/contracttest/suite.go connectors/reqlog/fake.go connectors/reqlog/client.go connectors/reqlog/client_contract_test.go internal/platform/requestlog/service.go internal/platform/requestlog/service_test.go internal/platform/httpapi/requests.go internal/platform/httpapi/requests_test.go web/apps/admin-web/src/api/requests.ts web/apps/admin-web/src/api/requests.test.ts web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx
git commit -m "feat(reqlog): add stable platform user filtering"
~~~

## Follow-on Plans Outside These Eight Tasks

以下各自是独立领域，必须另立 spec/plan，不得追加进上述提交：

1. Payment M3：UserRef、recharge order DTO、payment.read、retention/watermark、
   对账边界与用户详情 recharge Tab。
2. Invoice：CR-0002/CR-0003 双方确认、platform + source_user_id、invoice.read、
   PII-free filter 与 invoices Tab。
3. 本地 link 表：只有来源无法给出 UserRef 且产品批准人工维护时才设计迁移和 Action。

## Final Verification After Approved Implementation

Run:

~~~text
go fmt ./connectors/platformusers ./connectors/reqlog ./internal/platform/platformusers ./internal/platform/requestlog ./internal/platform/httpapi ./internal/platform/oidcauth ./cmd/platform-api
go vet ./...
go test -p 1 ./...
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
D:\Git\bin\bash.exe scripts/check-governance.sh
git diff --check
~~~

Browser gate:

- 1440 / 1024 / 800 / 390；
- source/freshness/retention/coverage/Fake 分 section 可见；
- 键盘可达 back link、Tabs、分页和详情链接；
- 403、501、404、incomplete、empty 不混淆；
- 无右侧抽屉、无完整 Key、无 request body 预取、无 invoice PII；
- 390px 组件自身无横向溢出，既有 AdminShell overflow 单独记录。
