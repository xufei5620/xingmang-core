// Package platformusers 定义「被管平台的终端用户清单」的只读接入契约
// (XM-0046,交接文档 §9.3)。
//
// ⚠️ **为什么不并进 connectors/sub2api 与 connectors/newapi**:与 metering 同一条
// 理由。那两个包读的是**自营实例的运营面板汇总**(用户总数、总余额、日收入),
// 本包读的是**逐用户明细**——两者的敏感度、分页形态与端点都不同。汇总数字进
// 指标表、逐用户明细不进(它是 PII,平台只做带权限的只读网关)。
//
// ⚠️ **本契约为 DRAFT。** 端点普查已确认上游有 `sub2api /admin/users` 与
// `newapi /api/user`,但响应形状尚未对着实现核对过。因此:
//
//   - `fake` 是当前唯一走得通的模式,样本形状即本契约的可执行说明;
//   - `real` 只搭骨架,调用即返回 not_supported——编一组字段名再标 DRAFT,
//     只会让接入那天的人拿到一串 bad_response,误以为是自己配错了。
//
// **原型自己也说了这件事**(运营工作台之外唯一一处 warnbar 逐字):
//
//	当前只读契约 v1 仅提供用户总数与总余额;逐用户今日充值、今日消费和消费
//	明细是目标界面,接真实数据前需扩展 read contract v2。
//
// 所以本契约把「逐用户充值/消费」建模成**可能缺席**(见 Amount.Known),
// 而不是缺省 0:0 和「上游没给」在界面上长得一样,而它们在运营上完全相反。
//
// 铁律(与 reqlog / metering 同源):
//   - ReadClient **只有读方法**(ADR-018 闸 4);
//   - 每个结果都带 Snapshot(规格 §9.1:禁止裸数字);
//   - 金额一律 int64 最小货币单位 + Currency,**全程零 float**(宪法 13 条);
//   - **邮箱在契约层就打码**(见 redact.go)——明文邮箱一个字都不出连接器。
package platformusers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	// ConnectorKey 是本 Connector 在 Registry 中的键。
	ConnectorKey = "platformusers"
	// ContractVersion 是本只读契约的版本。破坏性变更必须发新版本。
	//
	// 仍是 "1":DRAFT 说的是「还没对着真实响应核对过」,不是版本号可以随便动。
	ContractVersion = "1"
)

// SourceSub2API / SourceNewAPI 是本连接器认得的来源,取值与平台侧的
// service_type 同名,于是路由参数 {platform} 可以直接当 source 用。
//
// 这不是可以依赖的巧合:真实端点若用了别的写法,映射要落在 ParseSource
// 这一个函数里,不许散到调用方去。
const (
	SourceSub2API = "sub2api"
	SourceNewAPI  = "newapi"
)

// Sources 是有终端用户清单的平台。
//
// CPA 与服务器**不在**这里:CPA 是渠道代理(它的「用户」是代理商,语义不同,
// 原型自己留了这个提问),服务器根本没有终端用户。给它们挂一个永远空的用户
// 页签,等于说「这个平台没有用户」,而事实是我们压根没接。
var Sources = []string{SourceSub2API, SourceNewAPI}

// ParseSource 校验来源。不认识的一律拒绝,不猜。
func ParseSource(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	for _, known := range Sources {
		if s == known {
			return known, nil
		}
	}
	return "", fmt.Errorf("platformusers: 未知来源 %q(只支持 %s)", raw, strings.Join(Sources, " / "))
}

// ReadCapabilities 是本连接器的只读能力清单。
//
// 每一项都必须能被 registry.ParseCapability 解析且 IsWrite() 为 false——
// contracttest 会断言这一点,让「只读」成为可验证属性而非口头承诺。
var ReadCapabilities = []registry.Capability{
	"platformusers.users.read",
}

// ErrNotFound 表示上游确实答了,但没有这条记录。
var ErrNotFound = errors.New("platformusers: 记录不存在")

// Snapshot 是每个读取结果都必须携带的新鲜度元数据(规格 §9.1)。
//
// 对本连接器而言这是「我们**什么时候**问的上游」:逐用户的余额没有各自的
// 观测时刻,整页数据的新鲜度取决于这一次读取。没有它,一屏余额就是裸数字。
type Snapshot struct {
	// ObservedAt 是这次读取的时刻(UTC)。
	ObservedAt time.Time
	// Source 是上游实例标识,供界面回答「这是哪台机器上的数」。
	Source string
	// Watermark 是上游给的数据水位;上游不提供时为空串。
	Watermark string
}

// Amount 是一个**可能缺席**的金额。
//
// Known 为 false 表示上游没给这个数——与「值为 0」是相反的两件事:
// 前者要显示成「—」,后者是一条真实的读数。把两者压成一个 int64,
// 界面上就再也分不开了(NewAPI 渠道表的「未配置余额」踩过同一个坑)。
type Amount struct {
	MinorUnits int64
	Currency   string
	Known      bool
}

// KnownAmount 构造一个已知金额。
func KnownAmount(minorUnits int64, currency string) Amount {
	return Amount{MinorUnits: minorUnits, Currency: currency, Known: true}
}

// UnknownAmount 是「上游没给」。
func UnknownAmount() Amount { return Amount{} }

// UserStatus 是终端用户的账号状态。
type UserStatus string

const (
	// StatusActive 正常。
	StatusActive UserStatus = "active"
	// StatusLimited 受限(余额不足、风控标记等,上游语义各异)。
	StatusLimited UserStatus = "limited"
	// StatusDisabled 停用。
	StatusDisabled UserStatus = "disabled"
	// StatusUnknown 上游给了一个我们不认识的状态。
	//
	// **不回落到 active**:把一个被停用的账号显示成正常,人会据此排除掉
	// 真正的故障原因(与渠道令牌状态的「未知」同一条理由)。
	StatusUnknown UserStatus = "unknown"
)

// ParseUserStatus 把上游的状态串翻译成本契约的四态。
func ParseUserStatus(raw string) UserStatus {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active", "enabled", "normal", "ok", "1":
		return StatusActive
	case "limited", "restricted", "warn", "frozen":
		return StatusLimited
	case "disabled", "banned", "deleted", "0":
		return StatusDisabled
	default:
		return StatusUnknown
	}
}

// BusinessDayLayout 是业务日字符串的格式。
//
// 与 metering / invoice 逐字一致:业务日一旦有两种写法,同一天就会变成两条
// 记录。这里不复用那两个包的常量,是因为三个契约独立演进(见包注释),
// 但**判据必须一样**。
const BusinessDayLayout = "2006-01-02"

// ErrInvalidDay:业务日不符合 BusinessDayLayout。
var ErrInvalidDay = errors.New("platformusers: invalid business day")

// ValidateBusinessDay 严格校验 YYYY-MM-DD。
//
// 严格是必要的:time.Parse 对 "2026-8-1" 是宽容的,而一个宽容的解析会让
// 「2026-8-1」与「2026-08-01」查出两份不同的区间数,人却以为问的是同一天。
func ValidateBusinessDay(day string) error {
	parsed, err := time.Parse(BusinessDayLayout, day)
	if err != nil {
		return fmt.Errorf("business day %q 须形如 %s: %w", day, BusinessDayLayout, ErrInvalidDay)
	}
	if parsed.Format(BusinessDayLayout) != day {
		return fmt.Errorf("business day %q 必须严格为 %s(不接受少位写法): %w",
			day, BusinessDayLayout, ErrInvalidDay)
	}
	return nil
}

// DefaultBusinessDayLocation 返回 CST 固定 +08:00(无夏令时)。
//
// 用 FixedZone 而不是 time.LoadLocation("Asia/Shanghai"):后者依赖容器里有
// tzdata(scratch 镜像会失败),而且会随 tzdata 更新而变。核算口径里的 CST
// 必须是固定偏移(与 metering.DefaultBusinessDayLocation 同一条理由)。
//
// **业务日不是浏览器时区**(宪法 14 条):前端按本地时区切日的话,一个在
// UTC-5 的运营看到的「今天」会比账面上的业务日早一天。
func DefaultBusinessDayLocation() *time.Location {
	return time.FixedZone("+08:00", 8*3600)
}

// Granularity 是统计区间的粒度(原型顶部的日 / 周 / 月三个按钮)。
type Granularity string

const (
	// GranularityDay 单个业务日。
	GranularityDay Granularity = "day"
	// GranularityWeek 所选日期**所在的那一周**(周一至周日)。
	GranularityWeek Granularity = "week"
	// GranularityMonth 所选日期**所在的那个自然月**。
	GranularityMonth Granularity = "month"
)

// DefaultGranularity 是不传粒度时的默认值(原型默认选中「日」)。
const DefaultGranularity = GranularityDay

// ParseGranularity 校验粒度;空值给默认值,不认识的报错。
//
// 不静默回落:一个拼错的 `granularity=weekly` 若被当成「日」,人会拿着一天的
// 数字当一周的看——而两个数长得一样合理,界面上分辨不出来。
func ParseGranularity(raw string) (Granularity, error) {
	g := Granularity(strings.ToLower(strings.TrimSpace(string(raw))))
	if g == "" {
		return DefaultGranularity, nil
	}
	switch g {
	case GranularityDay, GranularityWeek, GranularityMonth:
		return g, nil
	}
	return "", fmt.Errorf("platformusers: 未知统计粒度 %q(只支持 day / week / month)", raw)
}

// Period 是一次查询的统计区间(原型的「统计区间」控件)。
//
// 它是**一个锚点日 + 一个粒度**,不是一对起止日:原型给的就是「选一个日期,
// 再选日/周/月」。存成起止对的话,「周」这个语义会在往返之间丢掉——回显时
// 只能说「2026-08-24 到 2026-08-30」,而人选的是「这一周」。
type Period struct {
	// Day 是锚点业务日(YYYY-MM-DD)。空串表示「今天」,由 Normalize 填。
	Day string
	// Granularity 是粒度。空串由 Normalize 填成 day。
	Granularity Granularity
	// From / To 是解析出来的**闭区间**业务日,由 Normalize 填。
	// 回显给调用方,让「这一周到底是哪七天」在界面上说得清。
	From string
	To   string
}

// Normalize 补齐默认值并解析出 From / To。
//
// now 用来解释「今天」,loc 用来决定业务日怎么切(nil 时用 CST +08:00)。
func (p Period) Normalize(now time.Time, loc *time.Location) (Period, error) {
	out := p
	if loc == nil {
		loc = DefaultBusinessDayLocation()
	}
	g, err := ParseGranularity(string(out.Granularity))
	if err != nil {
		return Period{}, err
	}
	out.Granularity = g

	out.Day = strings.TrimSpace(out.Day)
	if out.Day == "" {
		out.Day = now.In(loc).Format(BusinessDayLayout)
	}
	if err := ValidateBusinessDay(out.Day); err != nil {
		return Period{}, err
	}

	anchor, err := time.ParseInLocation(BusinessDayLayout, out.Day, loc)
	if err != nil {
		return Period{}, fmt.Errorf("platformusers: 解析业务日 %q: %w", out.Day, err)
	}
	from, to := windowOf(anchor, out.Granularity)
	out.From = from.Format(BusinessDayLayout)
	out.To = to.Format(BusinessDayLayout)
	return out, nil
}

// windowOf 返回锚点日所在区间的**闭区间**首尾日。
//
// 周取周一至周日(ISO 口径)而不是周日起算:中文运营语境里「本周」默认从周一开始,
// 而 Go 的 time.Weekday 里 Sunday==0,直接拿它算会把周日划进上一周。
func windowOf(anchor time.Time, g Granularity) (time.Time, time.Time) {
	switch g {
	case GranularityWeek:
		// Weekday(): Sunday=0…Saturday=6。要让周一=0,得把周日映射成 6。
		offset := (int(anchor.Weekday()) + 6) % 7
		start := anchor.AddDate(0, 0, -offset)
		return start, start.AddDate(0, 0, 6)
	case GranularityMonth:
		start := time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, anchor.Location())
		return start, start.AddDate(0, 1, -1)
	default: // GranularityDay
		return anchor, anchor
	}
}

// Days 返回区间跨了几天(日=1、周=7、月=28~31)。
//
// 供实现方按天摊算,也让「这个数是几天的合计」在界面上可解释。
func (p Period) Days() int {
	if p.From == "" || p.To == "" {
		return 0
	}
	loc := DefaultBusinessDayLocation()
	from, err1 := time.ParseInLocation(BusinessDayLayout, p.From, loc)
	to, err2 := time.ParseInLocation(BusinessDayLayout, p.To, loc)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(to.Sub(from).Hours()/24) + 1
}

// Totals 是一页查询的**区间合计**(原型顶部第 3、4 格)。
//
// 为什么不是两个裸 Amount:合计只能把**上游给得出流水的那些用户**加起来,
// 而 v1 契约对一部分用户给不出(见包注释的 warnbar)。一个盖住这件事的合计
// 会被读成全量,于是「区间充值 ¥8,100」实际只覆盖了八个用户里的六个——
// 那正是宪法 12 条要防的「裸数字冒充完整数据」。
type Totals struct {
	Recharge Amount
	Consumed Amount
	// CoveredUsers 是流水已知的用户数,TotalUsers 是符合筛选条件的用户数。
	// 两者不等 = 合计是**下界**,界面必须说出来。
	CoveredUsers int64
	TotalUsers   int64
}

// Complete 报告这个合计是否覆盖了全部用户。
func (t Totals) Complete() bool {
	return t.TotalUsers > 0 && t.CoveredUsers == t.TotalUsers
}

// User 是一个终端用户在平台侧的只读投影。
//
// **不含明文邮箱、不含手机号、不含 API Key 明文**:前两样在契约层就打码
// (见 redact.go),第三样根本不进这个结构——令牌前缀足以让运营对上一条请求,
// 而完整 Key 属于凭据,只经 CredentialRef(宪法 7 条)。
type User struct {
	// ID 是上游的用户标识,平台一律当不透明字符串。
	ID string
	// Username 是展示名。
	Username string
	// EmailMasked 是**已打码**的邮箱。连接器保证这里永远不是明文。
	EmailMasked string
	// Status 是账号状态。
	Status UserStatus
	// Balance 是可用余额。
	Balance Amount
	// PeriodRecharge 是所选区间的充值额。
	//
	// v1 上游多半给不出(见包注释里原型的那句 warnbar),那时 Known 为 false。
	PeriodRecharge Amount
	// PeriodConsumed 是所选区间的消费额(计费口径)。同样可能缺席。
	PeriodConsumed Amount
	// Last30dConsumed 是**近 30 天**消费额(滚动窗口,与所选区间无关)。
	//
	// 它刻意不随区间变:原型把它和「区间消费」并排放,正是为了让人一眼看出
	// 「这个用户今天花得少,但一个月来一直很稳」。两列都跟着区间走的话,
	// 这个对照就没有了。同样可能缺席(v1 契约给不出逐用户流水)。
	Last30dConsumed Amount
	// LastActiveAt 是最后活跃时刻;上游没给时为零值。
	LastActiveAt time.Time
	// TokenPrefix 是该用户某个 API Key 的前缀,供与请求列表对账。
	// 上游没给时为空串。**永远不是完整 Key。**
	TokenPrefix string
}

// UserPage 是一页用户。
type UserPage struct {
	Users []User
	// TotalCount 是符合条件的总数;Known 为 false 表示上游只给了这一页。
	//
	// 编一个总数出来(比如拿本页条数当总数)会让分页控件显示一个错的总页数,
	// 而那是人用来判断「还有没有更多」的唯一依据。
	TotalCount CountValue
	// TotalBalance 是**全体**用户的余额合计(不只本页)。
	// 这是原型顶部那个「所有用户总余额」格子的数据源。
	TotalBalance Amount
	// ActiveToday 是**今日活跃**用户数(原型第 1 格的副行「今日活跃 311」)。
	//
	// 「今日」按业务日算,不按最近 24 小时:副行要和第一格的总数是同一个口径,
	// 而那个总数说的是「账上有多少用户」。上游给不出时 Known=false。
	ActiveToday CountValue
	// PeriodTotals 是区间合计(原型顶部第 3、4 格),含覆盖率。
	PeriodTotals Totals
	// Period 回显服务端**实际用的**区间。
	//
	// 回显而不是让前端自己记:粒度到区间的换算(周从哪天起、月怎么切)只该有
	// 一份实现。前端自己算一遍,两边对不上的那天没人能说清哪个是对的。
	Period Period
	// NextCursor 为空表示没有下一页。
	NextCursor string
	Snapshot   Snapshot
}

// CountValue 是一个可能缺席的计数。理由同 Amount。
type CountValue struct {
	Value int64
	Known bool
}

// KnownCount 构造一个已知计数。
func KnownCount(v int64) CountValue { return CountValue{Value: v, Known: true} }

// UnknownCount 是「上游没给」。
func UnknownCount() CountValue { return CountValue{} }

// SortKey 是列表排序键。
//
// 做成受限枚举而不是透传字符串:排序键要拼进上游请求,透传等于把一个
// 未经校验的串送到别人家的查询里去。
type SortKey string

const (
	// SortBalanceDesc 余额从高到低。默认——运营最常问的是「谁还有钱」。
	SortBalanceDesc SortKey = "balance_desc"
	// SortConsumedDesc 区间消费从高到低。
	SortConsumedDesc SortKey = "consumed_desc"
	// SortLastActiveDesc 最近活跃优先。
	SortLastActiveDesc SortKey = "last_active_desc"
	// SortUsernameAsc 按用户名。
	SortUsernameAsc SortKey = "username_asc"
)

// DefaultSort 是不传排序时的默认值。
const DefaultSort = SortBalanceDesc

// ParseSortKey 校验排序键;空值给默认值,不认识的报错(不静默回落)。
func ParseSortKey(raw string) (SortKey, error) {
	s := SortKey(strings.ToLower(strings.TrimSpace(string(raw))))
	if s == "" {
		return DefaultSort, nil
	}
	switch s {
	case SortBalanceDesc, SortConsumedDesc, SortLastActiveDesc, SortUsernameAsc:
		return s, nil
	}
	return "", fmt.Errorf("platformusers: 未知排序键 %q", raw)
}

// MaxLimit / DefaultLimit 约束单页条数。
//
// 上限不是性能考虑,是**披露面**考虑:一次能拉走多少条用户明细,应该是一个
// 有上限的数,而不是由调用方决定。
const (
	MaxLimit     = 200
	DefaultLimit = 50
)

// ListFilter 是列表查询的过滤条件。
type ListFilter struct {
	Source string
	// Query 是搜索串,匹配用户名 / 用户 ID / 令牌前缀。
	//
	// **不匹配邮箱**:邮箱在契约层已经打码,拿明文去搜一份打过码的数据
	// 只会永远搜不到;而为了搜索保留一份明文,等于把打码这件事做了个寂寞。
	Query  string
	Status UserStatus
	Sort   SortKey
	// Period 是统计区间。零值由 Normalize 填成「今天 · 按日」。
	Period Period
	Limit  int
	Cursor string
}

// Normalize 补齐默认值、夹紧越界值,并把统计区间解析出来。
//
// **带时钟且会报错**:区间要把「今天」解释出来(需要时钟),而一个拼错的
// 业务日或粒度必须当场失败,不能静默回落——回落之后人会拿着一天的数字
// 当一周的看,两个数长得一样合理。
//
// loc 为 nil 时用 CST +08:00(业务日不按浏览器时区切,宪法 14 条)。
func (f ListFilter) Normalize(now time.Time, loc *time.Location) (ListFilter, error) {
	out := f
	out.Query = strings.TrimSpace(out.Query)
	if out.Sort == "" {
		out.Sort = DefaultSort
	}
	if out.Limit <= 0 {
		out.Limit = DefaultLimit
	}
	if out.Limit > MaxLimit {
		out.Limit = MaxLimit
	}
	period, err := out.Period.Normalize(now, loc)
	if err != nil {
		return ListFilter{}, err
	}
	out.Period = period
	return out, nil
}

// ReadClient 是本契约的**只读**接口。
//
// 只有一个方法,而且是读:写能力在 Foundation-B 之后另立接口,不得往这个
// 接口上加方法(ADR-018 闸 4 在这里体现为接口形状本身)。
type ReadClient interface {
	ListUsers(ctx context.Context, filter ListFilter) (UserPage, error)
}

// notSupported 是 real 骨架的统一返回。
//
// 归 KindNotSupported 而不是 KindBadResponse:这不是上游坏了,是平台这条
// 链路还没接通。前端据此显示「功能待上线」而不是「上游故障」。
func notSupported(source, detail string) error {
	return connector.NewError(connector.KindNotSupported, "platformusers.list_users",
		fmt.Errorf("%s: %s", source, detail))
}
