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
	Limit  int
	Cursor string
}

// Normalize 补齐默认值并夹紧越界值。
func (f ListFilter) Normalize() ListFilter {
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
	return out
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
