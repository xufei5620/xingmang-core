package metering

import "github.com/xufei5620/xingmang-platform/internal/platform/connector"

// RegisteredRouteSpecs returns metering's static read-only HTTP route
// inventory.  NewAPI revenue is database-backed and intentionally absent.
func RegisteredRouteSpecs() []connector.RouteSpec {
	return []connector.RouteSpec{
		{ConnectorType: ConnectorKey, Capability: "metering.health.read", RouteID: "metering.sub2api.health", Method: "GET", PathTemplate: "/health"},
		{ConnectorType: ConnectorKey, Capability: "metering.service.version_read", RouteID: "metering.sub2api.version", Method: "GET", PathTemplate: "/api/v1/admin/system/version"},
		{ConnectorType: ConnectorKey, Capability: "metering.token.usage_read", RouteID: "metering.sub2api.token.usage", Method: "GET", PathTemplate: "/v1/usage"},
		{ConnectorType: ConnectorKey, Capability: "metering.account.revenue_read", RouteID: "metering.sub2api.account.revenue", Method: "GET", PathTemplate: "/api/v1/admin/accounts/{account_id}/stats"},
		{ConnectorType: ConnectorKey, Capability: "metering.health.read", RouteID: "metering.newapi.status.health", Method: "GET", PathTemplate: "/api/status"},
		{ConnectorType: ConnectorKey, Capability: "metering.service.version_read", RouteID: "metering.newapi.status.version", Method: "GET", PathTemplate: "/api/status"},
		{ConnectorType: ConnectorKey, Capability: "metering.token.usage_read", RouteID: "metering.newapi.token.usage", Method: "GET", PathTemplate: "/api/log/self/stat"},
	}
}

func BudgetRouteSpecs() []connector.RouteSpec { return RegisteredRouteSpecs() }
