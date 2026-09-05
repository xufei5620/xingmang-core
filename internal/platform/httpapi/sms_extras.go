package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

// 接码扩展能力的读端点（XM-SMS1）。
//
// 全部是**实时上游调用**，不是投影：历史、统计、目录、报价这些只在人要看的
// 时候才有用，落库只会让页面显示一份过期的价格。写全部走 Action，这里没有
// 第二条写路径。
//
// 路由按供应商分：/sms/hero/... 与 /sms/sms62/...。共用路径会让页面在
// 62 上请求一个只有 Hero 有的东西，然后拿到一个含糊的错误。

// SMSExtrasReader 是 Service 提供的扩展能力入口。
type SMSExtrasReader interface {
	Hero(ctx context.Context, provider string) (sms.HeroExtras, error)
	SMS62(ctx context.Context, provider string) (sms.SMS62Extras, error)
	RefreshEmail(ctx context.Context, emailID string) (sms.Email, error)
}

// SMSEmailQuerier 是邮箱清单的本地读（投影）。
type SMSEmailQuerier interface {
	ListEmails(ctx context.Context, limit int) ([]sms.Email, error)
	GetEmail(ctx context.Context, emailID string) (sms.Email, error)
	GetResource(ctx context.Context, resourceID string) (sms.Resource, error)
}

func mountSMSExtras(api chi.Router, store SMSQuerier, reader SMSExtrasReader) {
	emails, _ := store.(SMSEmailQuerier)
	read := RequireScope(sms.PermissionRead)

	// ---- Hero：账户与目录（只读，不花钱）----
	api.With(read).Get("/sms/hero/balance", heroBalanceHandler(reader))
	api.With(read).Get("/sms/hero/countries", heroCountriesHandler(reader))
	api.With(read).Get("/sms/hero/services", heroServicesHandler(reader))
	api.With(read).Get("/sms/hero/operators", heroOperatorsHandler(reader))
	api.With(read).Get("/sms/hero/prices", heroPricesHandler(reader))
	api.With(read).Get("/sms/hero/top-countries", heroTopCountriesHandler(reader))
	api.With(read).Get("/sms/hero/custom-durations", heroCustomDurationsHandler(reader))
	api.With(read).Get("/sms/hero/rent-offers", heroRentOffersHandler(reader))
	api.With(read).Get("/sms/hero/rent-count", heroRentCountHandler(reader))
	api.With(read).Get("/sms/hero/history", heroHistoryHandler(reader))
	api.With(read).Get("/sms/hero/stats", heroStatsHandler(reader))
	api.With(read).Get("/sms/hero/email-domains", heroEmailDomainsHandler(reader))
	// ---- Hero：号码级 ----
	if emails != nil {
		api.With(read).Get("/sms/resources/{resourceID}/upstream-codes", heroResourceOTPsHandler(reader, emails))
		api.With(read).Get("/sms/resources/{resourceID}/extend-options", heroExtendOptionsHandler(reader, emails))
		api.With(read).Get("/sms/resources/{resourceID}/prolong-history", heroProlongHistoryHandler(reader, emails))
		// ---- 邮箱 ----
		api.With(read).Get("/sms/emails", listSMSEmailsHandler(emails))
		api.With(read).Get("/sms/emails/{emailID}", getSMSEmailHandler(emails, reader))
	}
	// ---- 62 ----
	api.With(read).Get("/sms/sms62/goods/{goodsID}", sms62GoodsDetailHandler(reader))
	api.With(read).Get("/sms/sms62/orders", sms62OrdersHandler(reader))
}

func queryInt64(r *http.Request, name string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(name)), 10, 64)
	return v
}

func queryInt(r *http.Request, name string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(name)))
	return v
}

func heroOf(w http.ResponseWriter, r *http.Request, reader SMSExtrasReader) (sms.HeroExtras, bool) {
	extras, err := reader.Hero(r.Context(), sms.ProviderHero)
	if err != nil {
		WriteError(w, r, err)
		return nil, false
	}
	return extras, true
}

func heroBalanceHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		balance, err := extras.Balance(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		// 余额是十进制文本；币种上游没说，**不补一个 USD**。
		WriteJSON(w, http.StatusOK, map[string]any{"balance_text": balance})
	}
}

func heroCountriesHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		countries, err := extras.Countries(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(countries))
		for _, c := range countries {
			out = append(out, map[string]any{
				"id": c.ID, "name_en": c.NameEN, "name_cn": c.NameCN, "name_ru": c.NameRU,
				"visible": c.Visible, "retry": c.Retry,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func heroServicesHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		services, err := extras.Services(r.Context(), queryInt64(r, "country"), strings.TrimSpace(r.URL.Query().Get("lang")))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(services))
		for _, s := range services {
			out = append(out, map[string]any{"code": s.Code, "name": s.Name})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func heroOperatorsHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		operators, err := extras.Operators(r.Context(), queryInt64(r, "country"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"by_country": operators})
	}
}

func heroPricesHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		prices, err := extras.Prices(r.Context(), strings.TrimSpace(r.URL.Query().Get("service")), queryInt64(r, "country"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		// 摊平成行：页面是一张表，不是嵌套 map。
		out := make([]map[string]any, 0)
		for country, byService := range prices {
			for service, cell := range byService {
				out = append(out, map[string]any{
					"country": country, "service": service,
					"cost_text": cell.Cost, "count": cell.Count, "physical_count": cell.PhysicalCount,
				})
			}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func heroTopCountriesHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		q := r.URL.Query()
		top, err := extras.TopCountries(r.Context(), strings.TrimSpace(q.Get("service")),
			q.Get("free_price") == "true", q.Get("by_rank") == "true")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(top))
		for _, t := range top {
			out = append(out, map[string]any{
				"country": t.Country, "price_text": t.Price, "retail_price_text": t.RetailPrice, "count": t.Count,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func heroCustomDurationsHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		durations, err := extras.CustomDurations(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0)
		for service, byCountry := range durations {
			for country, hours := range byCountry {
				out = append(out, map[string]any{"service": service, "country": country, "hours": hours})
			}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func heroRentOffersHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		offers, err := extras.RentOffers(r.Context(), queryInt64(r, "country"), queryInt64(r, "hours"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		services := make([]map[string]any, 0, len(offers.Services))
		for _, o := range offers.Services {
			services = append(services, map[string]any{
				"service": o.Service, "quantity": o.Quantity,
				"price_text": o.Price, "retail_price_text": o.RetailPrice,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"operators": offers.Operators, "services": services})
	}
}

func heroRentCountHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		q := r.URL.Query()
		cells, err := extras.RentCount(r.Context(), strings.TrimSpace(q.Get("service")), queryInt64(r, "country"), strings.TrimSpace(q.Get("operator")))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0)
		for country, byDuration := range cells {
			for hours, cell := range byDuration {
				out = append(out, map[string]any{"country": country, "hours": hours, "price_text": cell.Cost, "count": cell.Count})
			}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func heroHistoryHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		q := r.URL.Query()
		query := sms.HeroHistoryQuery{
			From: strings.TrimSpace(q.Get("from")), To: strings.TrimSpace(q.Get("to")),
			Page: queryInt(r, "page"), Size: queryInt(r, "size"),
			SortDesc: q.Get("sort") != "asc", Search: strings.TrimSpace(q.Get("search")),
		}
		for _, s := range q["service"] {
			query.Services = append(query.Services, s)
		}
		for _, c := range q["country"] {
			if n, err := strconv.ParseInt(c, 10, 64); err == nil {
				query.Countries = append(query.Countries, n)
			}
		}
		page, err := extras.History(r.Context(), query)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items := make([]map[string]any, 0, len(page.Items))
		for _, it := range page.Items {
			items = append(items, map[string]any{
				"id": it.ID, "create_date": it.CreateDate, "service": it.Service, "country": it.Country,
				"phone": it.Phone, "more_codes": it.MoreCodes, "cost_text": it.Cost, "status": it.Status,
				"phone_code": it.PhoneCode, "currency": it.Currency,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": items,
			// totals 是**这一页的**合计（官方如此），不是全量。
			"page_sum_text": page.TotalSum, "page_success_count": page.SuccessCount,
			"page": page.Page, "size": page.Size, "total": page.Total, "has_more": page.HasMore,
		})
	}
}

func heroStatsHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		date := strings.TrimSpace(r.URL.Query().Get("date"))
		if date == "" {
			date = time.Now().UTC().Format("2006-01-02")
		}
		stats, err := extras.Stats(r.Context(), date)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(stats))
		for _, s := range stats {
			out = append(out, map[string]any{"country": s.Country, "service": s.Service, "count": s.Count, "sum_text": s.Sum, "raw": s.Raw})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"date": date, "items": out})
	}
}

func heroEmailDomainsHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		domains, err := extras.EmailDomains(r.Context(), strings.TrimSpace(r.URL.Query().Get("site")))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(domains))
		for _, d := range domains {
			out = append(out, map[string]any{"name": d.Name, "cost_text": d.Cost, "count": d.Count})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func resourceOf(w http.ResponseWriter, r *http.Request, store SMSEmailQuerier) (sms.Resource, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "resourceID"))
	if id == "" {
		WriteError(w, r, action.NewError(action.CodeInvalidParams, "缺少 resource id", nil))
		return sms.Resource{}, false
	}
	res, err := store.GetResource(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return sms.Resource{}, false
	}
	if res.Provider != sms.ProviderHero {
		WriteError(w, r, action.NewError(action.CodeInvalidParams, "这家没有这项能力（只有 Hero-SMS 有）", nil))
		return sms.Resource{}, false
	}
	return res, true
}

func heroResourceOTPsHandler(reader SMSExtrasReader, store SMSEmailQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		res, ok := resourceOf(w, r, store)
		if !ok {
			return
		}
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		codes, err := extras.ListOTPs(r.Context(), res)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		mayReveal := p.HasScope(sms.PermissionReveal)
		out := make([]map[string]any, 0, len(codes))
		for _, c := range codes {
			item := map[string]any{"code_id": c.ID, "sender": c.Sender}
			if !c.ReceivedAt.IsZero() {
				item["received_at"] = c.ReceivedAt.UTC().Format(time.RFC3339)
			}
			// 码本身只回给持有 sms.reveal 的调用方，与本地验证码端点同一条闸。
			if mayReveal {
				item["code"] = c.Code
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func heroExtendOptionsHandler(reader SMSExtrasReader, store SMSEmailQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, ok := resourceOf(w, r, store)
		if !ok {
			return
		}
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		if kind == "" {
			kind = sms.KindProlong
		}
		options, err := extras.ExtendOptions(r.Context(), res, kind)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(options))
		for _, o := range options {
			out = append(out, map[string]any{"duration": o.Duration, "unit": o.Unit, "price_text": o.Price})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"kind": kind, "items": out})
	}
}

func heroProlongHistoryHandler(reader SMSExtrasReader, store SMSEmailQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, ok := resourceOf(w, r, store)
		if !ok {
			return
		}
		extras, ok := heroOf(w, r, reader)
		if !ok {
			return
		}
		records, err := extras.ProlongHistory(r.Context(), res)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(records))
		for _, rec := range records {
			out = append(out, map[string]any{"duration": rec.Duration, "unit": rec.Unit, "price_text": rec.Price, "created_at": rec.CreatedAt})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func emailItem(e sms.Email, mayReveal bool) map[string]any {
	item := map[string]any{
		"email_id": e.ID, "provider": e.Provider, "external_id": e.ExternalID,
		"site": e.Site, "email": e.Email, "status": e.Status,
		"cost_text": e.CostText, "currency": e.Currency, "message": e.Message,
		// has_value 让没有 reveal 权限的人也看得到「到了」这件事。
		"has_value": e.Value != "",
	}
	if mayReveal {
		item["value"] = e.Value
	}
	if !e.UpstreamDate.IsZero() {
		item["upstream_date"] = e.UpstreamDate.UTC().Format(time.RFC3339)
	}
	if !e.SyncedAt.IsZero() {
		item["synced_at"] = e.SyncedAt.UTC().Format(time.RFC3339)
	}
	return item
}

func listSMSEmailsHandler(store SMSEmailQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		emails, err := store.ListEmails(r.Context(), 200)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		mayReveal := p.HasScope(sms.PermissionReveal)
		out := make([]map[string]any, 0, len(emails))
		for _, e := range emails {
			out = append(out, emailItem(e, mayReveal))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// getSMSEmailHandler 读一个邮箱；?refresh=1 时先从上游读回最新状态再回。
//
// 刷新是**人发起**的，与取码同一条纪律：常驻轮询会把配额烧在没人看的邮箱上。
func getSMSEmailHandler(store SMSEmailQuerier, reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "emailID"))
		var (
			email sms.Email
			err   error
		)
		if r.URL.Query().Get("refresh") == "1" {
			email, err = reader.RefreshEmail(r.Context(), id)
		} else {
			email, err = store.GetEmail(r.Context(), id)
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, emailItem(email, p.HasScope(sms.PermissionReveal)))
	}
}

func sms62GoodsDetailHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, err := reader.SMS62(r.Context(), sms.ProviderSMS62)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		detail, err := extras.GoodsDetail(r.Context(), strings.TrimSpace(chi.URLParam(r, "goodsID")))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"id": detail.ID, "name": detail.Name, "price_text": detail.Price,
			"country": detail.Country, "stock": detail.Stock, "durations": detail.Durations,
		})
	}
}

func sms62OrdersHandler(reader SMSExtrasReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extras, err := reader.SMS62(r.Context(), sms.ProviderSMS62)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		page, err := extras.Orders(r.Context(), queryInt(r, "page"), queryInt(r, "page_size"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(page.Orders))
		for _, o := range page.Orders {
			item := map[string]any{
				"order_id": o.OrderID, "goods_id": o.GoodsID, "quantity": o.Quantity,
				"status": o.Status, "status_text": o.StatusText, "amount_text": o.AmountText,
			}
			if o.CreatedAt > 0 {
				item["created_at"] = time.Unix(o.CreatedAt, 0).UTC().Format(time.RFC3339)
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": out, "page": page.Page, "page_size": page.PageSize, "total": page.Total,
		})
	}
}
