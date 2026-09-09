package newapi

import "github.com/xufei5620/xingmang-platform/internal/platform/connector"

// RegisteredRouteSpecs returns the static read-only route inventory for R2-15.
func RegisteredRouteSpecs() []connector.RouteSpec {
	return []connector.RouteSpec{
		{ConnectorType: ConnectorKey, Capability: "newapi.service.version_read", RouteID: "newapi.status.version", Method: "GET", PathTemplate: "/api/status"},
		{ConnectorType: ConnectorKey, Capability: "newapi.health.read", RouteID: "newapi.status.health", Method: "GET", PathTemplate: "/api/status"},
		{ConnectorType: ConnectorKey, Capability: "newapi.users.read", RouteID: "newapi.users", Method: "GET", PathTemplate: "/api/user/"},
		{ConnectorType: ConnectorKey, Capability: "newapi.orders.read", RouteID: "newapi.topups", Method: "GET", PathTemplate: "/api/user/topup"},
		{ConnectorType: ConnectorKey, Capability: "newapi.channels.read", RouteID: "newapi.channels", Method: "GET", PathTemplate: "/api/channel/"},
		{ConnectorType: ConnectorKey, Capability: "newapi.errors.read", RouteID: "newapi.logs", Method: "GET", PathTemplate: "/api/log/"},
		{ConnectorType: ConnectorKey, Capability: "newapi.models.usage_read", RouteID: "newapi.data", Method: "GET", PathTemplate: "/api/data/"},
	}
}

func BudgetRouteSpecs() []connector.RouteSpec { return RegisteredRouteSpecs() }
