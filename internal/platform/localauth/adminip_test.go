package localauth

import "testing"

func TestParseAdminIPAllowlistEmptyMeansDisabled(t *testing.T) {
	a, err := ParseAdminIPAllowlist("")
	if err != nil {
		t.Fatal(err)
	}
	if a.Enabled() {
		t.Fatal("空配置不应视为已启用")
	}
	for _, ip := range []string{"1.2.3.4", "::1", "not-an-ip", ""} {
		if !a.Allowed(ip) {
			t.Fatalf("未启用时应放行任意值，ip=%q", ip)
		}
	}
}

func TestParseAdminIPAllowlistRejectsInvalidCIDR(t *testing.T) {
	if _, err := ParseAdminIPAllowlist("10.0.0.0/8, not-a-cidr"); err == nil {
		t.Fatal("非法 CIDR 应报错")
	}
}

func TestAdminIPAllowlistMatchesCIDR(t *testing.T) {
	a, err := ParseAdminIPAllowlist("10.0.0.0/8,203.0.113.4/32")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Enabled() {
		t.Fatal("非空配置应视为已启用")
	}
	cases := map[string]bool{
		"10.1.2.3":       true,
		"10.255.255.255": true,
		"203.0.113.4":    true,
		"203.0.113.5":    false,
		"192.168.1.1":    false,
		"":               false,
		"not-an-ip":      false,
	}
	for ip, want := range cases {
		if got := a.Allowed(ip); got != want {
			t.Fatalf("Allowed(%q) = %v, want %v", ip, got, want)
		}
	}
}

func TestAdminIPAllowlistIgnoresBlankEntries(t *testing.T) {
	a, err := ParseAdminIPAllowlist(" , 10.0.0.0/8 , ")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Allowed("10.0.0.1") {
		t.Fatal("应正确解析出唯一的有效 CIDR")
	}
}

func TestAdminIPAllowlistIPv6(t *testing.T) {
	a, err := ParseAdminIPAllowlist("2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Allowed("2001:db8::1") {
		t.Fatal("应支持 IPv6 CIDR")
	}
	if a.Allowed("2001:db9::1") {
		t.Fatal("不在网段内应拒绝")
	}
}
