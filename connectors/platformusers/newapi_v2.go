package platformusers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// NewAPI real GetUser v2(XM-USERS-V2-REAL,Task 5)。
//
// 授权:docs/approvals/NEWAPI_REAL_APPROVAL.md(APPROVED 2026-09-03)。
// 证据:docs/evidence/users-real/newapi/20260902T054724Z/
//
//	(SHA256SUMS: users_page 58b5701b7230bf6a…、user_detail 2e344cfed070d83e…、
//	 version 19eb35ae14b75c70…;上游 xm.solov.cc 版本 v1.0.0-rc.30,
//	 quota_per_unit 观测值 500000——**只是采集那一刻的观测,不是永久基数**,
//	 real reader 必须每次现读,不得硬编码)。
//
// 产品负责人附加约束(批准时提出):不得改动 NewAPI 源码,只能用它既有的
// 只读 HTTP API。
//
// 只映射批准的八个字段:id/username/display_name/email/status/quota/
// last_login_at/DeletedAt(与 upstream.go 的 newapiUserItem、
// NEWAPI_REAL_APPROVAL"字段捕获"一节逐字一致)。批准文本记录 dropped 字段
// 列表里出现过 password/original_password/verification_code 的字段名
// (值从未离开采集进程,但字段名本身值得安全侧留意)——本实现同样一个字节
// 都不解析这三个字段:newapiUserItem(upstream.go)压根没有声明它们对应的
// 结构体字段,不存在的字段不可能被 json.Unmarshal 填充。
//
// 上游没有原生的按 ID 精确查询端点(与 Sub2API 同理),GetUser 对 /api/user/
// 的分页做完整扫描,预算与 v1 ListUsers 的 fetchNewAPIUsers 一致
// (newapiMaxPages)。

// newAPIV2AllowedPath 是 v2 GetUser 允许请求的路径白名单。
//
// 只放行 /api/user/(用户清单,尾斜杠不能省——见 upstream.go 顶部注释)与
// /api/status(quota_per_unit 换算基数)。GET 写端点黑名单
// (/api/user/token、/api/user/aff、/api/user/epay/notify 等,见
// connectors/newapi/upstream.go 的 writeDisguisedAsGetRoutes)在这里独立
// 复刻一份判据——本包与 connectors/newapi 故意不共享私有符号(contract.go
// 包顶注释解释了原因:两个上游的敏感度、分页形态与端点都不同),但"这些
// 写端点必须被拒绝"这条纪律必须一样。用允许清单而不是逐条搬运黑名单:
// 不在名单上的路径一律拒绝,机械覆盖黑名单里列出的每一条,也覆盖黑名单
// 没想到的新写端点。
func newAPIV2AllowedPath(path string) bool {
	clean := "/" + strings.Trim(strings.TrimSpace(path), "/")
	return clean == "/api/user" || clean == "/api/status"
}

// matchNewAPIUserInPage 在已解出的一页 items 里找 ref.ID,命中则映射成
// UserDetail。软删除用户视为不存在(设计文档 §10"soft-delete 形态"、
// NEWAPI_REAL_APPROVAL 证据表同一节):详情页不能对一个已经被删除的账号
// 答"找到了"。
//
// quotaPerUnit 由调用方现读后传入(见 getNewAPIUser),parseNewAPIUsersPage
// 与生产分页循环共用这一份字段映射。返回的 UserDetail 不含
// Period/Capabilities,由 RealClient.GetUser 统一补齐。
func matchNewAPIUserInPage(page newapiPage[newapiUserItem], quotaPerUnit int64, ref UserRef, observedAt time.Time) (UserDetail, error) {
	for _, item := range page.Items {
		id := strings.TrimSpace(string(item.ID))
		if id == "" || id != ref.ID {
			continue
		}
		if item.softDeleted() {
			return UserDetail{}, ErrNotFound
		}
		quota, err := item.Quota.count()
		if err != nil {
			return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, err)
		}
		balance, err := quotaToMinorUnits(quota, quotaPerUnit, platformCurrencyScale)
		if err != nil {
			return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, err)
		}
		statusInt, err := item.Status.count()
		if err != nil {
			return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, err)
		}
		var lastActive time.Time
		if secs, err := item.LastLoginAt.count(); err == nil && secs > 0 {
			lastActive = time.Unix(secs, 0).UTC()
		}
		username := strings.TrimSpace(item.Username)
		if dn := strings.TrimSpace(item.DisplayName); dn != "" {
			username = dn
		}
		return UserDetail{
			Ref: ref,
			User: User{
				ID:              id,
				Username:        username,
				EmailMasked:     MaskEmail(item.Email),
				Status:          ParseUserStatus(strconv.FormatInt(statusInt, 10)),
				Balance:         KnownAmount(balance, platformCurrency),
				PeriodRecharge:  UnknownAmount(),
				PeriodConsumed:  UnknownAmount(),
				Last30dConsumed: UnknownAmount(),
				LastActiveAt:    lastActive,
			},
			Snapshot: EvidenceSnapshot{
				ObservedAt: observedAt,
				Source:     SourceNewAPI,
				Watermark:  fmt.Sprintf("obs-%d;qpu:%d", observedAt.Unix(), quotaPerUnit),
			},
		}, nil
	}
	return UserDetail{}, ErrNotFound
}

// parseNewAPIUsersPage 从一页 /api/user/ 响应体里找出 ref.ID 对应的那一条,
// 映射成 UserDetail。
//
// **比 plan 里给出的示意签名多了 quotaPerUnit 一个参数,这是一处有意的偏离
// (已在 handoff 的 deviations 一节说明)。** quota → 美元的换算基数在上游
// 是运行期可变的(NEWAPI_REAL_APPROVAL 证据表原文:"quota_per_unit……运行期
// 可变,这里记录的只是采集那一刻的观测值,不是永久基数,real reader 必须
// 每次现读,不得硬编码")。一个不接收这个值的"纯"解析函数只剩两条路可走:
// 要么在函数体内悄悄写一个默认基数(直接违反批准文本的硬约束),要么把
// Balance 留成 UnknownAmount、指望调用方事后改写(那样"这一步做了金额换算"
// 这件事会从类型签名里消失,评审时容易被忽略)。显式收参数,让"不许硬编码"
// 变成一个编译期就能看见、测试里必须显式喂值的契约,是更安全的做法。
func parseNewAPIUsersPage(body []byte, ref UserRef, quotaPerUnit int64, observedAt time.Time) (UserDetail, error) {
	var env newapiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return UserDetail{}, connector.NewError(connector.KindBadResponse, opGetUser, err)
	}
	var page newapiPage[newapiUserItem]
	if err := env.decode(opGetUser, &page); err != nil {
		return UserDetail{}, err
	}
	return matchNewAPIUserInPage(page, quotaPerUnit, ref, observedAt)
}

// getNewAPIUser 先现读 quota_per_unit(每次 GetUser 调用都重新问上游一次
// /api/status,绝不跨调用缓存——与 upstream.go 的 newapiQuotaPerUnit 用于
// v1 ListUsers 时同一条纪律),再翻页扫描 /api/user/,直到命中 ref.ID 或
// 翻完全量。
func (c *RealClient) getNewAPIUser(ctx context.Context, ref UserRef) (UserDetail, error) {
	if !newAPIV2AllowedPath(newapiRouteUsers) || !newAPIV2AllowedPath(newapiRouteStatus) {
		// 不可达的防御,理由与 getSub2APIUser 的同名检查一致:两个路由都是
		// 包内常量,理应永远在允许清单内。
		return UserDetail{}, connector.NewError(connector.KindWriteAttempt, opGetUser,
			fmt.Errorf("blocked route %s or %s", newapiRouteUsers, newapiRouteStatus))
	}

	// newapiQuotaPerUnit 定义于 upstream.go,RealClient 上没有任何字段缓存
	// 它的返回值——每次 GetUser 调用都会走到这里,发起一次新的
	// /api/status 请求。
	unit, err := c.newapiQuotaPerUnit(ctx)
	if err != nil {
		return UserDetail{}, err
	}

	var fetched, reported int64
	for p := 1; p <= newapiMaxPages; p++ {
		var env newapiEnvelope
		meta, err := c.get(ctx, opGetUser, newapiRouteUsers, newapiPageQuery(p, newapiPageSize), &env)
		if err != nil {
			return UserDetail{}, err
		}
		var page newapiPage[newapiUserItem]
		if err := env.decode(opGetUser, &page); err != nil {
			return UserDetail{}, err
		}

		observedAt := meta.observedAt(time.Time{})
		detail, matchErr := matchNewAPIUserInPage(page, unit, ref, observedAt)
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
			return UserDetail{}, ErrNotFound
		}
	}
	return UserDetail{}, ErrLookupIncomplete
}
