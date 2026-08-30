package sub2api

import (
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// RegisteredRouteSpecs exposes the static, non-secret outbound inventory used
// by the R2-15 policy review.  It does not alter request construction.
func RegisteredRouteSpecs() []connector.RouteSpec {
	return []connector.RouteSpec{
		{ConnectorType: ConnectorKey, Capability: "sub2api.health.read", RouteID: "sub2api.health", Method: "GET", PathTemplate: "/health"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.service.version_read", RouteID: "sub2api.version", Method: "GET", PathTemplate: "/api/v1/admin/system/version"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.users.read", RouteID: "sub2api.dashboard.stats", Method: "GET", PathTemplate: "/api/v1/admin/dashboard/stats"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.users.balance_read", RouteID: "sub2api.users", Method: "GET", PathTemplate: "/api/v1/admin/users"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.orders.read", RouteID: "sub2api.payment.dashboard", Method: "GET", PathTemplate: "/api/v1/admin/payment/dashboard"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.orders.read", RouteID: "sub2api.dashboard.trend", Method: "GET", PathTemplate: "/api/v1/admin/dashboard/trend"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.orders.read", RouteID: "sub2api.payment.orders", Method: "GET", PathTemplate: "/api/v1/admin/payment/orders"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.accounts.read", RouteID: "sub2api.accounts", Method: "GET", PathTemplate: "/api/v1/admin/accounts"},
		{ConnectorType: ConnectorKey, Capability: "sub2api.channels.balance_read", RouteID: "sub2api.channels.balance", Method: "GET", PathTemplate: "/api/v1/admin/accounts"},
	}
}

func BudgetRouteSpecs() []connector.RouteSpec { return RegisteredRouteSpecs() }
