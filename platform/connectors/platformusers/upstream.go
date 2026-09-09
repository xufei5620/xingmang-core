package platformusers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 上游(Sub2API / NewAPI)的路由与响应形状——**唯一定义处**。
//
// 形状依据:
//   - Sub2API:`K:/sub2api-src`(backend/internal/handler/admin/user_handler.go
//     的 List、backend/internal/handler/dto/types.go 的 User/AdminUser、
//     backend/internal/pkg/response/response.go 的 Success/Paginated/
//     ParsePagination)。与 connectors/sub2api/upstream.go 已经核对过的
//     `/api/v1/admin/*` 信封、鉴权头、分页参数同源,本文件直接复用那份核对
//     结论(信封 {code,message,data}、X-Api-Key、page/page_size)。
//   - NewAPI:`K:/newapi-src`(controller/user.go 的 GetAllUsers/SearchUsers、
//     model/user.go 的 User、common/page_info.go 的 PageInfo、
//     common/constants.go 的 UserStatusEnabled=1/UserStatusDisabled=2、
//     router/api-router.go 的 `/api/user/` 挂载)。同样与
//     connectors/newapi/upstream.go 已核对过的信封、鉴权头、分页参数同源
//     (信封 {success,message,data}、Authorization: Bearer、p/page_size)。
//
// 上游改版时,要改的应该只有这一个文件。
//
// ---------------------------------------------------------------------------
// 为什么不透传 Query 给上游的 search/keyword 参数(两边都有,都没用上)
// ---------------------------------------------------------------------------
//
// Sub2API 的 `search`(user_handler.go:119)与 NewAPI 的 `keyword`
// (model/user.go SearchUsers 第 444 行:`username LIKE ? OR email LIKE ?
// OR display_name LIKE ?`)都**同时匹配邮箱**。而本契约的 ListFilter.Query
// 明确禁止匹配邮箱(contract.go:"不匹配邮箱……为了搜索保留一份明文,等于把
// 打码这件事做了个寂寞")——把 Query 原样转发给上游的 search/keyword,等于
// 给运营一个"逐个试邮箱、看有没有命中"的存在性预言机,而这正是契约层打码
// 想堵住的那类泄漏。
//
// 所以本文件的两个 fetch* 函数从不把 Query/Status 发给上游:**翻全页、
// 在本机按 contract.go 的 User 字段过滤**,与 fake.go 的 ListUsers 同一套
// 过滤/排序/分页语义(见 listing.go)。这也顺带解决了 UserPage 的
// TotalBalance/ActiveToday 必须是"全体用户"口径这件事——那两个数本来就要
// 翻完全量才算得出来,不因为加了筛选条件而少翻。

// platformCurrency / platformCurrencyScale 是两个上游的记账币种。
//
// Sub2API"上游全线用美元记账"(connectors/sub2api/upstream.go 的
// defaultCurrency 注释);NewAPI 的钱只有 quota 一种单位,换算成钱的唯一口径
// 是"美元 = quota / quota_per_unit"(同上,newapi/upstream.go)。两边都是
// USD,不是原型样本数据用的 CNY——那只是 fake.go 的演示选择,不代表真实币种。
const (
	platformCurrency      = "USD"
	platformCurrencyScale = 2
)

const opListUsers = "platformusers.list_users"

// parseUpstreamTime 解析上游的 RFC3339 时间戳;解析不出返回零值。
//
// 零值是有意义的信号:调用方会把这个用户当"没有最后活跃时刻"处理,
// 而不是拿当前时间冒充一次观测。
func parseUpstreamTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// Sub2API:GET /api/v1/admin/users
// ---------------------------------------------------------------------------

const (
	sub2apiRouteUsers = "/api/v1/admin/users"
	// sub2apiAuthHeader 是上游为程序化访问准备的专用头,值形如
	// "admin-"+64 位十六进制(user_handler.go 的鉴权中间件、与
	// connectors/sub2api/upstream.go 的 authHeader 同源核对)。
	sub2apiAuthHeader = "X-Api-Key"
	// sub2apiBalanceScale 是用户余额在上游的小数位(dto.User.Balance 是
	// float64,ent 字段 decimal(20,8))。
	sub2apiBalanceScale = 8
	// sub2apiPageSize / sub2apiMaxPages:ParsePagination 把 page_size 钳到
	// 1000(response.go),translations 与 connectors/sub2api 的
	// userPageSize/maxUserPages 保持一致的预算(每次 ListUsers 都要翻完全量
	// 才能算出 TotalBalance/ActiveToday,预算不能无限)。
	sub2apiPageSize = 1000
	sub2apiMaxPages = 20
)

// sub2apiEnvelope 是 /api/v1 下所有 JSON 响应的统一外壳(response.Success)。
type sub2apiEnvelope struct {
	Code    rawAmount       `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e sub2apiEnvelope) decode(op string, out any) error {
	if code := strings.TrimSpace(string(e.Code)); code != "" && code != "0" {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("upstream business code %s", code))
	}
	if len(e.Data) == 0 {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("响应缺少 data 字段"))
	}
	if err := json.Unmarshal(e.Data, out); err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	return nil
}

// sub2apiPage 是 response.PaginatedData 的形状(items/total/page/page_size/pages)。
// 本包只用得到 items 与 total。
type sub2apiPage[T any] struct {
	Items []T       `json:"items"`
	Total rawAmount `json:"total"`
}

// sub2apiUserItem 是 dto.AdminUser 里本包用得到的字段。
//
// **刻意只解这六个。** 上游那个 DTO 还带着 role/frozen_balance/concurrency/
// allowed_groups/notes/group_rates 等运营配置字段,与"终端用户清单"这个契约
// 无关,多解一个字段就多一处让运营配置混进用户明细结构的机会。
//
// 不解 api_keys:List 端点不预置(需要单独查 GET /:id/api-keys),契约允许
// TokenPrefix 留空(见 contract.go User.TokenPrefix 的注释)。
type sub2apiUserItem struct {
	ID       rawAmount `json:"id"`
	Email    string    `json:"email"`
	Username string    `json:"username"`
	// Status 只有两个取值(backend/internal/domain/constants.go):
	// "active" / "disabled"。两者都已被 ParseUserStatus 的既有分支覆盖。
	Status string `json:"status"`
	// Balance 是 float64 美元(dto.User.Balance),可能为负(允许透支)。
	Balance rawAmount `json:"balance"`
	// LastActiveAt 可能为 null(dto: *time.Time, omitempty)。
	LastActiveAt *string `json:"last_active_at"`
}

// fetchSub2APIUsers 翻页拉取 Sub2API 的全部用户,映射成契约 User。
//
// 为什么翻全量而不是把 Limit/Cursor 转成 page/page_size 直接转发给上游:
// UserPage.TotalBalance / ActiveToday 是"全体用户"口径(contract.go 的字段
// 注释),而 Query/Status 又不能转发给上游(见包顶注释),所以无论如何都要
// 翻完全量——翻完之后一次性在本机过滤/排序/分页,是唯一能同时满足这两件事
// 的做法(见 listing.go)。
func (c *RealClient) fetchSub2APIUsers(ctx context.Context, now time.Time) ([]User, Amount, CountValue, Snapshot, error) {
	const op = opListUsers
	loc := DefaultBusinessDayLocation()
	today := now.In(loc).Format(BusinessDayLayout)

	var (
		out             []User
		totalBalanceSub int64 // 按 sub2apiBalanceScale 精度累加,最后一次性折算
		fetched         int64
		reported        int64
		truncated       bool
		activeCount     int64
		lastMeta        respMeta
	)

	for page := 1; page <= sub2apiMaxPages; page++ {
		var env sub2apiEnvelope
		query := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(sub2apiPageSize)},
		}
		meta, err := c.get(ctx, op, sub2apiRouteUsers, query, &env)
		if err != nil {
			return nil, Amount{}, CountValue{}, Snapshot{}, err
		}
		lastMeta = meta

		var pageData sub2apiPage[sub2apiUserItem]
		if err := env.decode(op, &pageData); err != nil {
			return nil, Amount{}, CountValue{}, Snapshot{}, err
		}
		if page == 1 {
			t, err := pageData.Total.count()
			if err != nil {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			reported = t
		}

		for _, item := range pageData.Items {
			id := strings.TrimSpace(string(item.ID))
			if id == "" {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op,
					fmt.Errorf("用户条目缺少 id 字段"))
			}
			balanceSub, err := item.Balance.minorUnits(sub2apiBalanceScale)
			if err != nil {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			totalBalanceSub += balanceSub
			balance, err := rescaleMinorUnits(balanceSub, sub2apiBalanceScale, platformCurrencyScale)
			if err != nil {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}

			var lastActive time.Time
			if item.LastActiveAt != nil {
				lastActive = parseUpstreamTime(*item.LastActiveAt)
			}
			if !lastActive.IsZero() && lastActive.In(loc).Format(BusinessDayLayout) == today {
				activeCount++
			}

			out = append(out, User{
				ID:              id,
				Username:        strings.TrimSpace(item.Username),
				EmailMasked:     MaskEmail(item.Email),
				Status:          ParseUserStatus(item.Status),
				Balance:         KnownAmount(balance, platformCurrency),
				PeriodRecharge:  UnknownAmount(),
				PeriodConsumed:  UnknownAmount(),
				Last30dConsumed: UnknownAmount(),
				LastActiveAt:    lastActive,
			})
		}

		fetched += int64(len(pageData.Items))
		if len(pageData.Items) == 0 || (reported > 0 && fetched >= reported) {
			break
		}
		if page == sub2apiMaxPages {
			truncated = true
		}
	}
	if reported > fetched {
		truncated = true
	}

	totalBalance, err := rescaleMinorUnits(totalBalanceSub, sub2apiBalanceScale, platformCurrencyScale)
	if err != nil {
		return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	snapshot := c.snapshotFor(lastMeta, truncated, "")
	return out, KnownAmount(totalBalance, platformCurrency), KnownCount(activeCount), snapshot, nil
}

// ---------------------------------------------------------------------------
// NewAPI:GET /api/user/ (+ GET /api/status 取 quota_per_unit)
// ---------------------------------------------------------------------------

const (
	// newapiRouteUsers **尾斜杠不能省**:router/api-router.go 把它注册成 "/",
	// 少一个斜杠会拿到 301,而只读客户端拒绝一切重定向(与
	// connectors/newapi/upstream.go 的同一个坑同一条纪律)。
	newapiRouteUsers  = "/api/user/"
	newapiRouteStatus = "/api/status"
	newapiAuthHeader  = "Authorization"
	newapiAuthScheme  = "Bearer "
	// newapiPageSize 是上游的硬上限(common/page_info.go 把 PageSize>100 钳到
	// 100)。newapiMaxPages 预算同 connectors/newapi 的 maxUserPages。
	newapiPageSize = 100
	newapiMaxPages = 50
)

// newapiEnvelope 是 NewAPI 所有 JSON 响应的统一外壳。
//
// ⚠️ **业务失败是 HTTP 200 + success:false**(上游 common.ApiError 走 200),
// 状态码检查之后必须再看这个 success 字段。
type newapiEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e newapiEnvelope) decode(op string, out any) error {
	if !e.Success {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("upstream reported success=false"))
	}
	if len(e.Data) == 0 || string(e.Data) == "null" {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("响应缺少 data 字段"))
	}
	if err := json.Unmarshal(e.Data, out); err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	return nil
}

// newapiPage 是 common.PageInfo 的形状(items/total/page/page_size)。
type newapiPage[T any] struct {
	Items []T       `json:"items"`
	Total rawAmount `json:"total"`
}

// newapiPageQuery 拼分页参数。
//
// 页码参数叫 **p** 不是 page(common/page_info.go):写成 page 不会报错,
// 上游会静静地按第 1 页返回,翻页变成原地打转。
func newapiPageQuery(page, size int) url.Values {
	return url.Values{"p": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}}
}

// newapiUserItem 是 /api/user/ 列表里本包用得到的字段(model/user.go 的
// User,json tag 逐字核对)。
//
// **刻意只解这七个。** 上游那个 struct 还带着 github_id/discord_id/
// wechat_id/telegram_id/oidc_id/linux_do_id/remark/setting/
// stripe_customer/aff_* 一堆第三方身份与内部字段,与"终端用户清单"这个契约
// 无关。核对过 model/user.go 全字段:**没有手机号字段**,联系方式只有
// email 一项(接入清单第 6 项)。
type newapiUserItem struct {
	ID          rawAmount `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	// Status 是整数(1=启用,2=停用,common/constants.go 的
	// UserStatusEnabled/UserStatusDisabled;"别用 0,那是默认值"),
	// 用 rawAmount 接住再转成串喂给 ParseUserStatus。
	Status rawAmount `json:"status"`
	// Quota 是用户当前剩余额度(上游是 int,单位 quota)。
	Quota rawAmount `json:"quota"`
	// LastLoginAt 是最后登录时刻(Unix 秒);json tag 是 last_login_at,
	// 不是 last_login_time——抄错不会报错,只会永远解出 0。
	LastLoginAt rawAmount `json:"last_login_at"`
	// DeletedAt 没有 json tag(gorm.DeletedAt 字段本身没写 tag),序列化出来
	// 的键是大驼峰 "DeletedAt"。列表查询是 Unscoped 的,软删除用户混在结果
	// 里,不看这个字段就会把删掉的人当成还在的用户列出来。
	DeletedAt json.RawMessage `json:"DeletedAt"`
}

func (u newapiUserItem) softDeleted() bool {
	text := strings.TrimSpace(string(u.DeletedAt))
	return text != "" && text != "null"
}

// newapiQuotaPerUnit 读取 quota → 美元的换算基数。
//
// **运行期可变**:root 能通过后台随时改它,所以每次 ListUsers 都要现读,
// 不能编译期写死,也不能跨请求缓存(RealClient 的生命周期就是一次读取,
// 见 client.go 的 NewRealClient 注释)。
func (c *RealClient) newapiQuotaPerUnit(ctx context.Context) (int64, error) {
	const op = opListUsers
	var env newapiEnvelope
	if _, err := c.get(ctx, op, newapiRouteStatus, nil, &env); err != nil {
		return 0, err
	}
	var payload struct {
		QuotaPerUnit rawAmount `json:"quota_per_unit"`
	}
	if err := env.decode(op, &payload); err != nil {
		return 0, err
	}
	unit, err := payload.QuotaPerUnit.count()
	if err != nil {
		return 0, connector.NewError(connector.KindBadResponse, op, err)
	}
	if unit <= 0 {
		// 上游的 option 写入路径把 ParseFloat 的 error 丢掉了,非法值会把
		// QuotaPerUnit 置 0。拿 0 做除数是崩溃,拿默认值顶上是编数,两个都不行。
		return 0, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("upstream quota_per_unit=%d,无法换算金额", unit))
	}
	return unit, nil
}

// fetchNewAPIUsers 翻页拉取 NewAPI 的全部(未软删除)用户,映射成契约 User。
// 翻全量的理由与 fetchSub2APIUsers 相同,见包顶注释。
func (c *RealClient) fetchNewAPIUsers(ctx context.Context, now time.Time) ([]User, Amount, CountValue, Snapshot, error) {
	const op = opListUsers

	unit, err := c.newapiQuotaPerUnit(ctx)
	if err != nil {
		return nil, Amount{}, CountValue{}, Snapshot{}, err
	}

	loc := DefaultBusinessDayLocation()
	today := now.In(loc).Format(BusinessDayLayout)

	var (
		out         []User
		totalQuota  int64
		fetched     int64
		reported    int64
		truncated   bool
		activeCount int64
		lastMeta    respMeta
	)

	for page := 1; page <= newapiMaxPages; page++ {
		var env newapiEnvelope
		meta, err := c.get(ctx, op, newapiRouteUsers, newapiPageQuery(page, newapiPageSize), &env)
		if err != nil {
			return nil, Amount{}, CountValue{}, Snapshot{}, err
		}
		lastMeta = meta

		var pageData newapiPage[newapiUserItem]
		if err := env.decode(op, &pageData); err != nil {
			return nil, Amount{}, CountValue{}, Snapshot{}, err
		}
		if page == 1 {
			// ⚠️ 这个 total 含软删除用户(Unscoped 查询),只用来判断翻页翻完没,
			// 绝不当作契约的 TotalCount 报出去。
			t, err := pageData.Total.count()
			if err != nil {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			reported = t
		}

		for _, item := range pageData.Items {
			if item.softDeleted() {
				continue
			}
			id := strings.TrimSpace(string(item.ID))
			if id == "" {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op,
					fmt.Errorf("用户条目缺少 id 字段"))
			}
			quota, err := item.Quota.count()
			if err != nil {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			totalQuota += quota
			balance, err := quotaToMinorUnits(quota, unit, platformCurrencyScale)
			if err != nil {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			statusInt, err := item.Status.count()
			if err != nil {
				return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
			}

			var lastActive time.Time
			if secs, err := item.LastLoginAt.count(); err == nil && secs > 0 {
				lastActive = time.Unix(secs, 0).UTC()
			}
			if !lastActive.IsZero() && lastActive.In(loc).Format(BusinessDayLayout) == today {
				activeCount++
			}

			username := strings.TrimSpace(item.Username)
			if dn := strings.TrimSpace(item.DisplayName); dn != "" {
				username = dn
			}

			out = append(out, User{
				ID:              id,
				Username:        username,
				EmailMasked:     MaskEmail(item.Email),
				Status:          ParseUserStatus(strconv.FormatInt(statusInt, 10)),
				Balance:         KnownAmount(balance, platformCurrency),
				PeriodRecharge:  UnknownAmount(),
				PeriodConsumed:  UnknownAmount(),
				Last30dConsumed: UnknownAmount(),
				LastActiveAt:    lastActive,
			})
		}

		fetched += int64(len(pageData.Items))
		if len(pageData.Items) == 0 || (reported > 0 && fetched >= reported) {
			break
		}
		if page == newapiMaxPages {
			truncated = true
		}
	}
	if reported > fetched {
		truncated = true
	}

	totalBalance, err := quotaToMinorUnits(totalQuota, unit, platformCurrencyScale)
	if err != nil {
		return nil, Amount{}, CountValue{}, Snapshot{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	snapshot := c.snapshotFor(lastMeta, truncated, fmt.Sprintf("qpu:%d", unit))
	return out, KnownAmount(totalBalance, platformCurrency), KnownCount(activeCount), snapshot, nil
}

// snapshotFor 组出这次读取的 Snapshot。
//
// Watermark 是这个契约版本里唯一能捎带"这次读取有没有缺口"的自由文本通道
// (contract.go 的 Snapshot 没有 IsPartial 字段,不像 v2 的 EvidenceSnapshot)。
// truncated 时追加 ";truncated",让运维一眼看出这次的 TotalBalance/
// ActiveToday 是"翻到页数上限为止"的部分合计,而不是全量——数字依然给出
// (与 contract.go Totals 的"合计是下界也要报"同一条理由),但缺口必须可见
// (宪法 12 条)。extra 用来再带一条上下文(目前只有 NewAPI 的换算基数)。
func (c *RealClient) snapshotFor(meta respMeta, truncated bool, extra string) Snapshot {
	observedAt := meta.observedAt(time.Time{})
	watermark := fmt.Sprintf("obs-%d", observedAt.Unix())
	if extra != "" {
		watermark += ";" + extra
	}
	if truncated {
		watermark += ";truncated"
	}
	return Snapshot{
		ObservedAt: observedAt,
		Source:     c.cfg.Source,
		Watermark:  watermark,
	}
}
