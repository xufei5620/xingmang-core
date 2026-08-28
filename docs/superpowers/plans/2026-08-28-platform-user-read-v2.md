# Platform User Read Contract v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** 建立稳定的 platform-scoped UserRef 与精确 GetUser，并以 capability 分片逐步接入每日消费、Key 元数据和 reqlog 用户请求，同时保持 payment/invoice 独立和诚实不可用。

**Architecture:** platformusers v1 ListUsers 保持兼容；v2 在同一 Go 包中增加 UserDetailReader、DailyUsageReader、KeyMetadataReader 三个可选只读接口。HTTP 页面并列调用 platformusers 与 reqlog 等领域 Query，各自保留 scope、分页、retention、freshness 和 coverage；不建跨域聚合 Connector，不默认建本地 link 表。

**Tech Stack:** Go 1.27 support line、Chi HTTP、React 19、TypeScript 5.9、TanStack Query 5、Vitest 4、现有 Connector/Action error/Registry capability 框架。

**Spec:** docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md

## Global Constraints

- 本计划只有在产品负责人批准 spec 待审批项 1、2、3 后才能开始实现。
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
- 默认不建本地 link 表；需要时另立迁移、Action、证据与回滚设计。
- 实现代码使用 go fmt，不使用裸 gofmt；不升级依赖。

---

### Task 1: UserRef、canonical codec 与 v2 核心类型

**Files:**
- Create: contracts/connectors/platformusers.read.v2.md
- Create: contracts/testdata/platform-user-ref-v1.json
- Create: connectors/platformusers/userref.go
- Create: connectors/platformusers/userref_test.go
- Create: connectors/platformusers/contract_v2.go
- Modify: connectors/platformusers/contract_test.go
- Modify: web/apps/admin-web/src/api/users.ts
- Modify: web/apps/admin-web/src/api/users.test.ts

**Interfaces:**
- Consumes: platformusers.SourceSub2API / SourceNewAPI、Amount、CountValue、Period、User、registry.Capability。
- Produces: UserRef、EncodeUserIDSegment、DecodeUserIDSegment、EvidenceSnapshot、GetUserQuery、UserDetail、UserDetailReader、ErrLookupIncomplete、V2 capability constants。

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

- [ ] **Step 6: 让 TypeScript codec 读取同一份 golden**

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

- [ ] **Step 7: 同步 v2 contract 文档并格式化**

contracts/connectors/platformusers.read.v2.md 必须逐字冻结 UserRef、codec、
GetUser 404/incomplete 判据、能力清单、scope 门和跨域非目标。

Run: go fmt ./connectors/platformusers

- [ ] **Step 8: 提交 Task 1**

~~~bash
git add contracts/connectors/platformusers.read.v2.md contracts/testdata/platform-user-ref-v1.json connectors/platformusers/userref.go connectors/platformusers/userref_test.go connectors/platformusers/contract_v2.go connectors/platformusers/contract_test.go web/apps/admin-web/src/api/users.ts web/apps/admin-web/src/api/users.test.ts
git commit -m "feat(platformusers): define v2 user identity contract"
~~~

### Task 2: v2 Fake 与 capability contracttest

**Files:**
- Create: connectors/platformusers/fake_v2.go
- Create: connectors/platformusers/contracttest/v2_suite.go
- Create: connectors/platformusers/contract_v2_test.go
- Modify: connectors/platformusers/fake.go

**Interfaces:**
- Consumes: Task 1 UserRef/GetUserQuery/UserDetail/UserDetailReader 与 capability constants。
- Produces: FakeClient.GetUser、source-specific V2Capabilities、共享 RunV2 contract suite。

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

Sub2API Fake 声明 detail_read、daily_usage_read、keys_metadata_read；
NewAPI Fake 只声明 detail_read。GetUser 必须遍历 Fake 的 source-scoped 数据并仅接受
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
git add connectors/platformusers/fake_v2.go connectors/platformusers/contracttest/v2_suite.go connectors/platformusers/contract_v2_test.go connectors/platformusers/fake.go
git commit -m "test(platformusers): add executable v2 fake contract"
~~~

### Task 3: GetUser Service、HTTP Query 与 B001 前端切换

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
不直接 JSON 序列化 Connector 结构体。

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

必须同时存在：

- 脱敏的 /api/v1/admin/users 响应形状和实例版本；
- 分页、ID、状态、余额 scale、created/last-active 的解释；
- 人工已完成 AdminComplianceGuard 的记录；
- 证明选用端点不是 mock 的源码/实例证据。

任一缺失即停止。平台不得为完成此任务发 compliance POST。

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

Run: go test ./connectors/platformusers/... -run "TestSub2APIV2|TestV2" -v

Expected: PASS。

- [ ] **Step 6: 提交 Task 4**

~~~bash
git add docs/evidence/EV-2026-08-28-platformusers-sub2api-v2-shape.md connectors/platformusers/testdata/sub2api/users_page.redacted.json connectors/platformusers/testdata/sub2api/user_detail.redacted.json connectors/platformusers/sub2api_v2.go connectors/platformusers/sub2api_v2_test.go connectors/platformusers/client.go connectors/platformusers/contracttest/v2_suite.go cmd/platform-api/main.go
git commit -m "feat(platformusers): add evidence-gated Sub2API user reader"
~~~

### Task 5: NewAPI real GetUser（真实样本门控）

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

证据必须冻结 /api/user/ 的尾斜杠、分页 envelope、soft-delete、ID、quota、
created_at、last_login_at、status、quota_per_unit 和 PII 字段。必须记录管理员 token
经 CredentialRef 注入，以及 /api/user/token、/api/user/aff、epay notify 等 GET 写端点
仍在黑名单。缺一项即停止。

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
platformusers.user.detail_read。运行：

Run: go test ./connectors/platformusers/... -run "TestNewAPIV2|TestV2" -v

Expected: PASS。

- [ ] **Step 6: 提交 Task 5**

~~~bash
git add docs/evidence/EV-2026-08-28-platformusers-newapi-v2-shape.md connectors/platformusers/testdata/newapi/users_page.redacted.json connectors/platformusers/testdata/newapi/user_detail.redacted.json connectors/platformusers/newapi_v2.go connectors/platformusers/newapi_v2_test.go connectors/platformusers/client.go connectors/platformusers/contracttest/v2_suite.go cmd/platform-api/main.go
git commit -m "feat(platformusers): add evidence-gated NewAPI user reader"
~~~

### Task 6: DailyUsage capability 与七日趋势

**Files:**
- Create: connectors/platformusers/daily_usage.go
- Create: connectors/platformusers/daily_usage_test.go
- Modify: connectors/platformusers/contract_v2.go
- Modify: connectors/platformusers/fake_v2.go
- Modify: connectors/platformusers/sub2api_v2.go
- Modify: connectors/platformusers/newapi_v2.go
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
- Produces: GET /api/v1/platforms/{platform}/users/{canonicalUserId}/daily-usage?day=&days=7。

- [ ] **Step 1: 检查 source capability 证据**

每个平台分别判断。没有真实、非 mock 的逐日数据与稳定 user ID 时，该平台不实现
DailyUsageReader；Fake 可继续作为契约说明，但 UI real 模式必须 unavailable。

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
}
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/platformusers ./internal/platform/httpapi -run TestDailyUsage -v

Expected: FAIL because series implementation/handler do not exist.

- [ ] **Step 4: 实现 CST 闭区间与 page-level evidence**

days 只接受 1..31，空 day 由服务端按 +08:00 解释；Points 每日一条、升序；
缺日填 UnknownAmount 并降低 coverage；跨币种或部分日失败不能输出完整合计。
HTTP DTO 金额用字符串/null，返回 expected_days/covered_days/complete 和 Snapshot。

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
git add connectors/platformusers/daily_usage.go connectors/platformusers/daily_usage_test.go connectors/platformusers/contract_v2.go connectors/platformusers/fake_v2.go connectors/platformusers/sub2api_v2.go connectors/platformusers/newapi_v2.go connectors/platformusers/contracttest/v2_suite.go internal/platform/httpapi/users_daily_usage.go internal/platform/httpapi/users_daily_usage_test.go internal/platform/httpapi/router.go web/apps/admin-web/src/api/users.ts web/apps/admin-web/src/api/users.test.ts web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx
git commit -m "feat(platformusers): add capability-gated daily usage series"
~~~

### Task 7: Key metadata 与独立 scope

**Files:**
- Create: connectors/platformusers/key_metadata.go
- Create: connectors/platformusers/key_metadata_test.go
- Modify: connectors/platformusers/contract_v2.go
- Modify: connectors/platformusers/fake_v2.go
- Modify: connectors/platformusers/sub2api_v2.go
- Modify: connectors/platformusers/newapi_v2.go
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
- Produces: ScopeKeyMetadataRead = platform.user_keys.read 和 capability-gated keys Query。

- [ ] **Step 1: 检查双审批与真实样本门**

必须有产品+安全对 platform.user_keys.read 的明确批准、角色映射裁定、Sub2/New
Key metadata 的脱敏真实样本。任一缺失即停止；不得先把新 scope 放进
DEFAULT_SCOPES 或默认 admin/staff。

- [ ] **Step 2: 写 RED secret/PII 反射测试**

~~~go
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
~~~

- [ ] **Step 3: 运行 RED**

Run: go test ./connectors/platformusers ./internal/platform/httpapi -run "TestKeyMetadata|TestListUserKeys" -v

Expected: FAIL because key types/handler/scope do not exist.

- [ ] **Step 4: 实现 metadata-only 契约与 stable pagination**

默认 limit=50、最大 200；cursor 绑定 UserRef；Prefix 最大 8 code point；
ID 不得可逆到完整 Key；status 未知映射 unknown；零时间对外 null；每页 Snapshot
必须存在。真实来源没证明接口时不声明 keys capability。

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
git add connectors/platformusers/key_metadata.go connectors/platformusers/key_metadata_test.go connectors/platformusers/contract_v2.go connectors/platformusers/fake_v2.go connectors/platformusers/sub2api_v2.go connectors/platformusers/newapi_v2.go connectors/platformusers/contracttest/v2_suite.go internal/platform/httpapi/users_keys.go internal/platform/httpapi/users_keys_test.go internal/platform/platformusers/permissions.go internal/platform/httpapi/router.go internal/platform/oidcauth/resolver_test.go web/apps/admin-web/src/api/config.ts web/apps/admin-web/src/api/users.ts web/apps/admin-web/src/api/users.test.ts web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx
git commit -m "feat(platformusers): add scoped key metadata query"
~~~

### Task 8: reqlog 稳定 UserRef 与用户 usage 面板

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
Sub2API/NewAPI capability-gated usage/request panel。

- [ ] **Step 1: 检查 reqlog 真实关联门**

必须有真实 API/源码和脱敏 fixture，证明抄录记录或受信映射能返回 platform +
source user ID。只有 Username/TokenPrefix 不算通过。若不能证明，停止 Task 8，
用户详情继续 unavailable。

- [ ] **Step 2: 写 RED anti-join 测试**

~~~go
func TestReqlogNeverAssociatesByUsernameOrPrefix(t *testing.T) {
    rows := []reqlog.RequestLogSummary{
        {Source: "sub2api", Username: "same", TokenPrefix: "sk-abcd"},
        {Source: "newapi", Username: "same", TokenPrefix: "sk-abcd"},
    }
    ref := platformusers.UserRef{Platform: "sub2api", ID: "u_1"}
    for _, row := range rows {
        if row.MatchesUser(ref) {
            t.Fatalf("unlinked row was guessed: %+v", row)
        }
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
