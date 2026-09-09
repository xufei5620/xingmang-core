package platformusers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// Sub2API real GetUser v2(XM-USERS-V2-REAL,Task 4)。
//
// 授权:docs/approvals/SUB2_REAL_APPROVAL.md(APPROVED 2026-09-03)。
// 证据:docs/evidence/users-real/sub2api/20260902T054723Z/
//
//	(SHA256SUMS: users_page 02013b4f7761cbd6…、user_detail d250d91f403b4dba…、
//	 version 8f3991ad282def8b…;上游 api.solov.cc 版本 0.1.184)。
//
// 产品负责人附加约束(批准时提出):不得改动 Sub2API 源码,只能用它既有的
// 只读 HTTP API。
//
// **上游没有原生的按 ID 精确查询端点。** SUB2_REAL_APPROVAL 的证据只覆盖了
// GET /api/v1/admin/users(清单端点)——/api/v1/admin/users/:id/usage 已被
// EV-2026-08-27-sub2api-read-survey.md 证实是 mock(即使返回 200 也不得作为
// 数据源),永久加入禁用测试。所以 GetUser 对清单分页做完整扫描,直到命中或
// 翻页预算耗尽,预算与 v1 ListUsers 的 fetchSub2APIUsers 一致
// (sub2apiMaxPages)。
//
// 只映射批准的六个字段:id/email/username/status/balance/last_active_at
// (与 upstream.go 的 sub2apiUserItem、SUB2_REAL_APPROVAL"字段捕获"一节
// 逐字一致)。created_at 等字段在证据里被列为 dropped,RegisteredAt 因此
// 保持零值(未知)——不在最小结构里编一个"看起来有数据"的时间出来。

// opGetUser 是 GetUser 的 op 标签,Sub2API/NewAPI 两条路径共用同一个值:
// 与 opListUsers 的角色一样,只用于 connector.Error 的 Op 字段与日志,
// 不区分平台(平台已经在 UserRef.Platform / Snapshot.Source 里体现)。
const opGetUser = "platformusers.get_user"

// sub2V2AllowedPath 是 v2 GetUser 允许请求的路径白名单。
//
// **只放行裸列表端点。** SUB2_REAL_APPROVAL 的证据只覆盖 users_page 这一个
// 请求形状;/api/v1/admin/users/:id/usage 等端点已被证实是 mock(见包顶
// 注释),即使将来有人想加一个"详情端点抄近路",这里也要机械拒绝——
// TestSub2APIV2PermanentlyRejectsMockUsageRoute 钉死这条纪律,不依赖评审时
// 有没有人记得去翻证据文档。
func sub2V2AllowedPath(path string) bool {
	clean := "/" + strings.Trim(strings.TrimSpace(path), "/")
	want := "/" + strings.Trim(sub2apiRouteUsers, "/")
	return clean == want
}

// ErrSub2APIComplianceConfirmationRequired 标记 Sub2API 的
// AdminComplianceGuard(0.1.183 起,未确认合规声明的 admin 账号,全部 admin
// GET 都返回 423)。
//
// **平台永远不会代为发送那个 POST**(SUB2_REAL_APPROVAL 前置条件、
// docs/runbooks/SWITCH-SUB2API-REAL.md"前置"一节):合规确认是一次性的
// 人工操作,必须由人亲自在 Sub2API 后台点掉。getSub2APIUser 只发 GET
// (见该方法与 client.go 的 get,请求方法在那里硬编码为 http.MethodGet),
// 结构上就不可能替用户发出这个确认——本错误只是把 classifyStatus 已经
// 归好的 connector.KindAuth 再包一层可识别标记,让调用方与运维排查能用
// errors.Is 把"这是合规门"和"这是别的鉴权失败(比如 token 被吊销)"分开,
// 不必去猜 err.Error() 的自由文本。
var ErrSub2APIComplianceConfirmationRequired = errors.New(
	"sub2api: 管理员账号未完成 AdminComplianceGuard 合规确认(423);" +
		"需人工在 Sub2API 后台确认一次,平台不会代为发送该 POST")

// matchSub2UserInPage 在已解出的一页 items 里找 ref.ID,命中则映射成
// UserDetail。parseSub2UsersPage(纯函数,供固件测试直接调用)与
// getSub2APIUser(生产分页循环)共用这一份字段映射,避免两条路径各写一遍、
// 迟早对不上。
//
// 返回的 UserDetail 不含 Period/Capabilities:那两项由调用方
// (RealClient.GetUser)统一补齐,与 Ref 本身的校验一样,不在这里重复。
func matchSub2UserInPage(page sub2apiPage[sub2apiUserItem], ref UserRef, observedAt time.Time) (UserDetail, error) {
	for _, item := range page.Items {
		id := strings.TrimSpace(string(item.ID))
		if id == "" || id != ref.ID {
			continue
		}
		balanceSub, err := item.Balance.minorUnits(sub2apiBalanceScale)
		if err != nil {
			return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, err)
		}
		balance, err := rescaleMinorUnits(balanceSub, sub2apiBalanceScale, platformCurrencyScale)
		if err != nil {
			return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, err)
		}
		var lastActive time.Time
		if item.LastActiveAt != nil {
			lastActive = parseUpstreamTime(*item.LastActiveAt)
		}
		return UserDetail{
			Ref: ref,
			User: User{
				ID:              id,
				Username:        strings.TrimSpace(item.Username),
				EmailMasked:     MaskEmail(item.Email),
				Status:          ParseUserStatus(item.Status),
				Balance:         KnownAmount(balance, platformCurrency),
				PeriodRecharge:  UnknownAmount(),
				PeriodConsumed:  UnknownAmount(),
				Last30dConsumed: UnknownAmount(),
				LastActiveAt:    lastActive,
			},
			Snapshot: EvidenceSnapshot{
				ObservedAt: observedAt,
				Source:     SourceSub2API,
				Watermark:  fmt.Sprintf("obs-%d", observedAt.Unix()),
			},
		}, nil
	}
	return UserDetail{}, ErrNotFound
}

// parseSub2UsersPage 从一页 /api/v1/admin/users 响应体里找出 ref.ID 对应的
// 那一条,映射成 UserDetail。这是纯函数:只做 JSON 解析与字段映射,不做任何
// I/O——单元测试直接喂 testdata/sub2api 下的脱敏样本字节
// (docs/evidence/users-real/sub2api/20260902T054723Z/ 的逐字节拷贝)。
func parseSub2UsersPage(body []byte, ref UserRef, observedAt time.Time) (UserDetail, error) {
	var env sub2apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, err)
	}
	var page sub2apiPage[sub2apiUserItem]
	if err := env.decode(opGetUser, &page); err != nil {
		return UserDetail{}, err
	}
	return matchSub2UserInPage(page, ref, observedAt)
}

// getSub2APIUser 翻页扫描 /api/v1/admin/users,直到命中 ref.ID 或翻完全量。
//
// 预算与判据与 v1 fetchSub2APIUsers(upstream.go)一致(sub2apiMaxPages、
// reported-vs-fetched 判断"翻完了没"):这不是 v1 的另一套实现,只是找到就
// 提前返回,不必像 ListUsers 那样非翻完不可(GetUser 不需要
// TotalBalance/ActiveToday 这类全体口径的聚合)。
func (c *RealClient) getSub2APIUser(ctx context.Context, ref UserRef) (UserDetail, error) {
	if !sub2V2AllowedPath(sub2apiRouteUsers) {
		// 不可达的防御:sub2apiRouteUsers 是包内常量,理应永远在白名单内。
		// 这里让"只能走 allowlist"这条纪律对生产调用路径也成立,而不是只活
		// 在 sub2V2AllowedPath 自己的单元测试里。
		return UserDetail{}, connector.NewError(connector.KindWriteAttempt, opGetUser,
			fmt.Errorf("blocked route %s", sub2apiRouteUsers))
	}

	var fetched, reported int64
	for p := 1; p <= sub2apiMaxPages; p++ {
		var env sub2apiEnvelope
		query := url.Values{"page": {strconv.Itoa(p)}, "page_size": {strconv.Itoa(sub2apiPageSize)}}
		meta, err := c.get(ctx, opGetUser, sub2apiRouteUsers, query, &env)
		if err != nil {
			if meta.status == http.StatusLocked {
				// 423:合规确认门。原始 connector.Error 仍然通过 %w 保留在
				// Unwrap 链里(connector.KindOf 与 errors.Is(err,
				// context.Canceled) 等既有判据继续成立),只是多包一层可识别
				// 标记。
				return UserDetail{}, fmt.Errorf("%w: %w", ErrSub2APIComplianceConfirmationRequired, err)
			}
			return UserDetail{}, err
		}
		var page sub2apiPage[sub2apiUserItem]
		if err := env.decode(opGetUser, &page); err != nil {
			return UserDetail{}, err
		}

		observedAt := meta.observedAt(time.Time{})
		detail, matchErr := matchSub2UserInPage(page, ref, observedAt)
		if matchErr == nil {
			return detail, nil
		}
		if !errors.Is(matchErr, ErrNotFound) {
			return UserDetail{}, matchErr
		}

		if p == 1 {
			t, terr := page.Total.count()
			if terr != nil {
				return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, terr)
			}
			reported = t
		}
		fetched += int64(len(page.Items))
		if len(page.Items) == 0 || (reported > 0 && fetched >= reported) {
			// 翻完了(拿到的条数已经追上上游声称的总数,或者这一页本来就是
			// 空的)还是没找到——是真的 ErrNotFound,不是预算耗尽。
			return UserDetail{}, ErrNotFound
		}
	}
	// 翻页预算到顶,上游还有更多页——不能断言"没有这个用户",只能说"这次
	// 没查完"(设计文档 §5.1)。
	return UserDetail{}, ErrLookupIncomplete
}
