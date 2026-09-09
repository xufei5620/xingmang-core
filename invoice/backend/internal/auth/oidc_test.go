package auth

import "testing"

func TestOIDCConfigRequiresExactHTTPSAndAdminRole(t *testing.T) {
	valid := OIDCConfig{IssuerURL: "https://auth.solov.cc/realms/solov", ClientID: "invoice-web", RedirectURL: "https://invoice.solov.cc/auth/callback", AdminRole: "invoice-admin"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []OIDCConfig{{IssuerURL: "http://auth.example", ClientID: "x", RedirectURL: "https://invoice.example/cb", AdminRole: "admin"}, {IssuerURL: "https://auth.example?other=1", ClientID: "x", RedirectURL: "https://invoice.example/cb", AdminRole: "admin"}, {IssuerURL: "https://auth.example", ClientID: "", RedirectURL: "https://invoice.example/cb", AdminRole: "admin"}, {IssuerURL: "https://auth.example", ClientID: "x", RedirectURL: "https://invoice.example/cb", AdminRole: ""}}
	for _, config := range invalid {
		if err := config.Validate(); err == nil {
			t.Fatalf("accepted unsafe config: %+v", config)
		}
	}
}
