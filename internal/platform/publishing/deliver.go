package publishing

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Delivery 是交给出站投递器的一次投递意图。
//
// 今天没有任何投递器会收到它（见 doc.go）。定义它是为了让第二层有一个明确的
// 接缝，也为了让「没有投递器」这条缺席主张**可以被变异验证**——一个连接口都
// 没有的缺席主张，只能靠读代码相信。
type Delivery struct {
	RecordID     uuid.UUID
	Environment  string
	Channel      Channel
	Draft        Draft
	DraftVersion int
	Assets       []Asset
	ScheduledAt  *time.Time
	RequestedBy  string
}

// Receipt 是投递器的回执。ExternalRef 是平台返回编号。
type Receipt struct {
	ExternalRef string
	DeliveredAt time.Time
	Detail      string
}

// Deliverer 是「把内容真的发到某个外部平台」的出站投递器。
//
// 实现它需要的东西一样都还没有：X 的 Connector 契约、OAuth 流程、速率限制、
// 失败重试，以及一条经 ADR-021 登记的写通道（`connector.VendorWriteTransport`
// 今天只给 Infini 用，而 ADR-021 明确它**不覆盖**「平台不拥有其业务真相」的
// 上游）。所以本仓库里没有任何实现，这是第二层的活。
type Deliverer interface {
	// Platform 是这个投递器负责的平台。
	Platform() Platform
	// Deliver 真的把内容发出去。返回的 Receipt 会写进发布记录。
	Deliver(ctx context.Context, d Delivery) (Receipt, error)
}

// delivererTable 是平台 → 投递器的查找表。
//
// 生产装配传 nil（见 NewService 的调用点 cmd/platform-api）。做成字段而不是包级
// 变量：包级变量会让「有没有投递器」变成一个跨用例共享的全局状态，一条用例注册了
// 假投递器就会污染其它用例——而这里恰恰有一条用例要靠注册假投递器来做变异验证。
type delivererTable map[Platform]Deliverer

// DelivererFor 返回该平台的出站投递器；**今天恒为 nil**。
//
// 调用方据此决定发布记录的结果。写成一次查表而不是 `return nil`：后者会让
// 「接上投递器」变成一次要改控制流的改动，而查表只需要往表里加一项——
// 接缝留在这里，缺席由装配（空表）与用例共同保证，不是由这行代码保证。
func (s *Service) DelivererFor(p Platform) Deliverer {
	if s == nil || s.deliverers == nil {
		return nil
	}
	return s.deliverers[p]
}

// PlatformsWithDeliverer 列出此刻**真的**能发出去的平台，顺序固定。
//
// 页面用它来说实话：空列表 → 「排期与审批是真的，真正发出去还没接」。
// 让服务端算而不是让前端假定为空——前端假定的那一刻，这句话就从一个事实
// 变成了一句写死的文案，接上投递器的那天没人会想起来改它。
func (s *Service) PlatformsWithDeliverer() []Platform {
	var out []Platform
	for _, p := range AllPlatforms {
		if s.DelivererFor(p) != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
