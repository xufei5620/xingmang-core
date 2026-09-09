package adminsettings

import "testing"

func TestIsIssuerConfiguredRecognizesReservedPlaceholders(t *testing.T) {
	for name, fixture := range map[string]struct {
		issuer     string
		configured bool
	}{
		"empty":                      {issuer: "   ", configured: false},
		"exact placeholder":          {issuer: UnconfiguredIssuerName, configured: false},
		"production placeholder":     {issuer: " 待配置实际开票主体（上线前必须修改） ", configured: false},
		"legacy example placeholder": {issuer: "请替换为实际开票主体全称", configured: false},
		"configured legal entity":    {issuer: " 示例科技有限公司 ", configured: true},
		"contains non-prefix marker": {issuer: "示例待配置科技有限公司", configured: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsIssuerConfigured(fixture.issuer); got != fixture.configured {
				t.Fatalf("IsIssuerConfigured(%q)=%t want %t", fixture.issuer, got, fixture.configured)
			}
		})
	}
}
