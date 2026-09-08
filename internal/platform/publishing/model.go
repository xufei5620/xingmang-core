package publishing

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Platform 是渠道所属的外部社交平台。
//
// **闭集，且刻意不含站内公告**：Sub2API/NewAPI 的公告接口都要求平台向它们
// 发写请求，被 ADR-018 与 ADR-021 排除（见 doc.go）。列一个发不出去的取值
// 等于在渠道下拉框里摆一个假入口。取值与迁移 000052 的 CHECK 逐字一致。
type Platform string

const (
	PlatformX        Platform = "x"
	PlatformTelegram Platform = "telegram"
	// PlatformOther 是「X 以外的其它社交平台」的逃生口——产品负责人的原话是
	// 「x 以及其他社交平台」，而具体是哪些还没定。它同样没有出站投递器。
	PlatformOther Platform = "other"
)

// AllPlatforms 是全部取值，顺序固定（供页面下拉与用例遍历）。
//
// 用例遍历它而不是各写各的清单：新增一个平台时，「这个平台有没有投递器」
// 那条缺席用例会自动覆盖到它，不靠作者记得去补。
var AllPlatforms = []Platform{PlatformX, PlatformTelegram, PlatformOther}

// ParsePlatform 解析平台取值。
func ParsePlatform(s string) (Platform, error) {
	for _, p := range AllPlatforms {
		if string(p) == s {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: 平台 %q 不在 %v 内", ErrInvalidInput, s, AllPlatforms)
}

// ChannelStatus 是渠道账号的状态。
type ChannelStatus string

const (
	ChannelActive  ChannelStatus = "ACTIVE"
	ChannelPaused  ChannelStatus = "PAUSED"
	ChannelRetired ChannelStatus = "RETIRED"
)

var allChannelStatuses = []ChannelStatus{ChannelActive, ChannelPaused, ChannelRetired}

// ParseChannelStatus 解析渠道状态。
func ParseChannelStatus(s string) (ChannelStatus, error) {
	for _, st := range allChannelStatuses {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("%w: 渠道状态 %q 不在 %v 内", ErrInvalidInput, s, allChannelStatuses)
}

// DraftStatus 是草稿状态。
//
// **没有 PUBLISHED**：草稿不会因为提交了发布就变成「已发布」——今天没有任何
// 东西发得出去，多一个终态就是多一处可以骗人的地方。「这一版发到哪儿了」由
// PublishRecord 回答。
type DraftStatus string

const (
	DraftDraft     DraftStatus = "DRAFT"
	DraftScheduled DraftStatus = "SCHEDULED"
	DraftArchived  DraftStatus = "ARCHIVED"
)

// AssetKind 是素材类型。素材是**引用**不是上传（平台没有对象存储）。
type AssetKind string

const (
	AssetImage AssetKind = "image"
	AssetVideo AssetKind = "video"
	AssetLink  AssetKind = "link"
)

var allAssetKinds = []AssetKind{AssetImage, AssetVideo, AssetLink}

// ParseAssetKind 解析素材类型。
func ParseAssetKind(s string) (AssetKind, error) {
	for _, k := range allAssetKinds {
		if string(k) == s {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: 素材类型 %q 不在 %v 内", ErrInvalidInput, s, allAssetKinds)
}

// DeliveryResult 是一条发布记录的投递结果。
//
// 今天只有一个取值，与迁移 000052 的 CHECK 一致。见 doc.go「本包不做什么」。
type DeliveryResult string

// ResultNotDelivered：审批与排期都是真的，**内容没有发出去**。
const ResultNotDelivered DeliveryResult = "NOT_DELIVERED"

// NotDeliveredReason 是发布记录 detail 列的固定文案，也是页面上那句话的来源。
//
// 定成常量而不是各处现写：这句话是本片对使用者最重要的一句交代，散成三份
// 迟早有一份被改软（「暂未投递」「投递中」都是把「没有这个能力」说成
// 「这次没成功」）。用例逐字断言它——只断错误码不够，文案也要逐字。
const NotDeliveredReason = "未投递：平台尚无出站投递器，排期与审批是真的，内容没有发送到任何外部平台。"

// ErrInvalidInput：入参不合法（由 Action handler 映射成 INVALID_PARAMS）。
var ErrInvalidInput = errors.New("publishing: invalid input")

// ErrNotFound：目标对象不存在（或不在本环境）。
var ErrNotFound = errors.New("publishing: not found")

// ErrConflict：与既有数据冲突（重复登记、素材仍被引用等）。
var ErrConflict = errors.New("publishing: conflict")

// ErrChannelNotPublishable：渠道当前不接受发布（暂停或已退役）。
var ErrChannelNotPublishable = errors.New("publishing: channel is not accepting content")

const (
	maxTitleLen       = 200
	maxBodyLen        = 20000
	maxNoteLen        = 1000
	maxHandleLen      = 100
	maxURILen         = 2000
	maxAssetsPerDraft = 20
)

// httpsURI 与迁移 000052 的 CHECK 同义：素材地址会随对外内容一起公开，
// 允许 http 等于允许把一条明文链接发到公网。
var httpsURI = regexp.MustCompile(`^https://[^\s]+$`)

// Channel 是一个已登记的对外发布渠道账号。
type Channel struct {
	ID          uuid.UUID
	Environment string
	Platform    Platform
	Handle      string
	DisplayName string
	Purpose     string
	// CredentialRef 是凭据**引用**，形如 secret://<scope>/<name>。
	// 空串表示还没登记引用。明文永不出现在这个结构里（宪法 7 条）。
	CredentialRef string
	Status        ChannelStatus
	Note          string
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AcceptsContent 报告这个渠道此刻是否接受新的发布请求。
func (c Channel) AcceptsContent() bool { return c.Status == ChannelActive }

// Draft 是一条内容草稿。
type Draft struct {
	ID             uuid.UUID
	Environment    string
	Title          string
	Body           string
	ScheduledAt    *time.Time
	Status         DraftStatus
	CurrentVersion int
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// AssetIDs 只在详情查询里填充；列表查询留空。
	AssetIDs []uuid.UUID
}

// Revision 是草稿的一版不可变修订。
type Revision struct {
	DraftID     uuid.UUID
	Version     int
	Title       string
	Body        string
	ScheduledAt *time.Time
	Note        string
	CreatedBy   string
	CreatedAt   time.Time
}

// Asset 是一条素材引用。
type Asset struct {
	ID          uuid.UUID
	Environment string
	Name        string
	Kind        AssetKind
	URI         string
	Note        string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PublishRecord 是一次发布请求的记录。
//
// **不是「已发出」的记录**：它回答「谁在什么时候请求把哪一版发到哪个渠道、
// 结果如何」。今天结果恒为 ResultNotDelivered。
//
// 没有审批单号字段，理由见迁移 000052 该表的注释（Handler 拿不到单号，
// 链路经审计事件绕一跳仍然完整）。
type PublishRecord struct {
	ID           uuid.UUID
	Environment  string
	DraftID      uuid.UUID
	DraftVersion int
	ChannelID    uuid.UUID
	ScheduledAt  *time.Time
	RequestedBy  string
	Result       DeliveryResult
	// ExternalRef 是平台返回编号，DeliveredAt 是投递时刻。两者都是第二层的
	// 落点；今天必须为空，库层 CHECK 也这么要求。
	ExternalRef string
	DeliveredAt *time.Time
	Detail      string
	CreatedAt   time.Time
}

// Delivered 报告这条记录是否真的投递出去了。今天恒为 false。
//
// 做成方法而不是让调用方各自比 Result：页面、Query 与用例都问同一个问题，
// 三处各写一次比较迟早有一处把「未投递」读成「投递中」。
func (r PublishRecord) Delivered() bool {
	return r.Result != ResultNotDelivered && r.DeliveredAt != nil
}

// NormalizeTitle 校验并规整标题。
func NormalizeTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%w: 标题不能为空", ErrInvalidInput)
	}
	if len([]rune(s)) > maxTitleLen {
		return "", fmt.Errorf("%w: 标题超过 %d 字", ErrInvalidInput, maxTitleLen)
	}
	return s, nil
}

// NormalizeBody 校验正文。允许为空（先起标题、后写正文是常见做法）。
func NormalizeBody(s string) (string, error) {
	if len([]rune(s)) > maxBodyLen {
		return "", fmt.Errorf("%w: 正文超过 %d 字", ErrInvalidInput, maxBodyLen)
	}
	return s, nil
}

// NormalizeNote 校验备注。
func NormalizeNote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > maxNoteLen {
		return "", fmt.Errorf("%w: 备注超过 %d 字", ErrInvalidInput, maxNoteLen)
	}
	return s, nil
}

// NormalizeHandle 校验账号标识。
func NormalizeHandle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%w: 账号标识不能为空", ErrInvalidInput)
	}
	if len([]rune(s)) > maxHandleLen {
		return "", fmt.Errorf("%w: 账号标识超过 %d 字", ErrInvalidInput, maxHandleLen)
	}
	return s, nil
}

// NormalizeAssetURI 校验素材地址。
func NormalizeAssetURI(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxURILen {
		return "", fmt.Errorf("%w: 素材地址超过 %d 字符", ErrInvalidInput, maxURILen)
	}
	if !httpsURI.MatchString(s) {
		return "", fmt.Errorf("%w: 素材地址必须是 https:// 开头且不含空白", ErrInvalidInput)
	}
	return s, nil
}

// NormalizeCredentialRef 校验凭据引用。
//
// 空串合法（还没登记）。非空时必须解析成一个合法 CredentialRef——这道校验挡的
// 是**运营手滑把 token 本身粘进来**：那会当场变成一条永久的明文泄漏，而
// 这张表是页面上直接可填的。库层另有同义的正则（迁移 000052），两道都要。
func NormalizeCredentialRef(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	ref, err := secrets.ParseCredentialRef(s)
	if err != nil {
		// **不把 err 的文本拼进来**：ParseCredentialRef 的错误消息里带着原串，
		// 而原串此刻可能正是一条被误粘进来的明文 token。
		return "", fmt.Errorf("%w: 凭据引用必须形如 secret://<scope>/<name>，不要粘贴凭据本身", ErrInvalidInput)
	}
	return ref.String(), nil
}

// ValidateAssetCount 限制一条草稿引用的素材数量。
func ValidateAssetCount(n int) error {
	if n > maxAssetsPerDraft {
		return fmt.Errorf("%w: 一条草稿最多引用 %d 个素材", ErrInvalidInput, maxAssetsPerDraft)
	}
	return nil
}

// ClampListLimit 把列表上限收进 [1, 500]。
//
// 与 alerts.ClampListLimit 同一条理由：limit 由调用方给，服务端必须有自己的
// 天花板，否则一个 limit=1000000 就能把一页拉成一次全表扫描。
func ClampListLimit(limit int32) int32 {
	const maxLimit int32 = 500
	if limit <= 0 {
		return 100
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
