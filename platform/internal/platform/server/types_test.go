package server

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validAsset() Asset {
	return Asset{
		Hostname:    "srv-sin-01",
		IPAddresses: []string{"10.0.0.5", "203.0.113.9"},
		Status:      AssetActive,
		Environment: "production",
	}
}

func TestAssetValidateAcceptsMinimalRow(t *testing.T) {
	if err := validAsset().Validate(); err != nil {
		t.Fatalf("最小合法资产不该被拒: %v", err)
	}
}

func TestAssetValidateRequiresHostname(t *testing.T) {
	a := validAsset()
	a.Hostname = "   "
	err := a.Validate()
	if !errors.Is(err, ErrMissingField) {
		t.Fatalf("空主机名应返回 ErrMissingField, got %v", err)
	}
}

func TestAssetValidateRejectsBadStatus(t *testing.T) {
	a := validAsset()
	a.Status = "online" // 不是登记簿的三态之一——那是心跳判定的说法，不是本包的口径
	if err := a.Validate(); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("非法 status 应返回 ErrInvalidFormat, got %v", err)
	}
}

func TestAssetValidateRejectsMalformedIP(t *testing.T) {
	a := validAsset()
	a.IPAddresses = []string{"not-an-ip"}
	if err := a.Validate(); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("非法 IP 应返回 ErrInvalidFormat, got %v", err)
	}
}

func TestAssetValidateRejectsNonPositiveSpec(t *testing.T) {
	for name, mutate := range map[string]func(*Asset){
		"vcpu=0":      func(a *Asset) { v := 0; a.VCPU = &v },
		"vcpu 负数":     func(a *Asset) { v := -1; a.VCPU = &v },
		"memory_gb=0": func(a *Asset) { v := 0; a.MemoryGB = &v },
		"disk_gb=0":   func(a *Asset) { v := 0; a.DiskGB = &v },
	} {
		t.Run(name, func(t *testing.T) {
			a := validAsset()
			mutate(&a)
			if err := a.Validate(); !errors.Is(err, ErrInvalidFormat) {
				t.Fatalf("%s 应被拒, got %v", name, err)
			}
		})
	}
}

// TestAssetValidateRequiresCostCurrencyPaired 钉住「金额与币种成对出现」的
// 不变量——一个有金额没币种的行没法展示，一个有币种没金额的行代表着什么
// 也无法解释。
func TestAssetValidateRequiresCostCurrencyPaired(t *testing.T) {
	cost := int64(9990)
	t.Run("有金额没币种", func(t *testing.T) {
		a := validAsset()
		a.MonthlyCostMinorUnits = &cost
		if err := a.Validate(); !errors.Is(err, ErrInconsistent) {
			t.Fatalf("应返回 ErrInconsistent, got %v", err)
		}
	})
	t.Run("有币种没金额", func(t *testing.T) {
		a := validAsset()
		a.Currency = "USD"
		if err := a.Validate(); !errors.Is(err, ErrInconsistent) {
			t.Fatalf("应返回 ErrInconsistent, got %v", err)
		}
	})
	t.Run("金额为0且币种齐全时合法——0是免费的合法取值不是没填", func(t *testing.T) {
		a := validAsset()
		zero := int64(0)
		a.MonthlyCostMinorUnits = &zero
		a.Currency = "USD"
		if err := a.Validate(); err != nil {
			t.Fatalf("0 元 + 已登记币种应合法: %v", err)
		}
	})
}

func TestAssetValidateRejectsUnknownCurrency(t *testing.T) {
	a := validAsset()
	cost := int64(100)
	a.MonthlyCostMinorUnits = &cost
	a.Currency = "XYZ"
	if err := a.Validate(); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("未登记币种应被拒, got %v", err)
	}
}

func TestAssetValidateRejectsNegativeCost(t *testing.T) {
	a := validAsset()
	cost := int64(-1)
	a.MonthlyCostMinorUnits = &cost
	a.Currency = "USD"
	if err := a.Validate(); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("负数月付成本应被拒, got %v", err)
	}
}

func TestAssetValidateRejectsBadBillingCycle(t *testing.T) {
	a := validAsset()
	a.BillingCycle = "weekly"
	if err := a.Validate(); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("非法 billing_cycle 应被拒, got %v", err)
	}
}

func TestSupplierValidateRequiresName(t *testing.T) {
	s := Supplier{Environment: "production"}
	if err := s.Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("空供应商名应被拒, got %v", err)
	}
}

func TestSupplierValidateRejectsNonHTTPSURLs(t *testing.T) {
	base := func() Supplier { return Supplier{Name: "Vultr", Environment: "production"} }
	t.Run("website 非 https", func(t *testing.T) {
		s := base()
		s.Website = "http://vultr.com"
		if err := s.Validate(); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("非 https website 应被拒, got %v", err)
		}
	})
	t.Run("website 含用户信息段", func(t *testing.T) {
		s := base()
		s.Website = "https://user:pass@vultr.com"
		if err := s.Validate(); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("含 user:pass@ 的 website 应被拒, got %v", err)
		}
	})
	t.Run("console_url 非 https", func(t *testing.T) {
		s := base()
		s.ConsoleURL = "ftp://console.vultr.com"
		if err := s.Validate(); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("非 https console_url 应被拒, got %v", err)
		}
	})
}

func TestServerDomainValidateRejectsBadCertSource(t *testing.T) {
	d := ServerDomain{DomainName: "example.com", Environment: "production", CertSource: "self-signed"}
	if err := d.Validate(); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("非法 cert_source 应被拒, got %v", err)
	}
}

func TestServerDomainValidateAcceptsKnownCertSources(t *testing.T) {
	for _, cs := range []CertSource{CertACME, CertManaged, CertManual, ""} {
		d := ServerDomain{DomainName: "example.com", Environment: "production", CertSource: cs}
		if err := d.Validate(); err != nil {
			t.Fatalf("cert_source=%q 应合法: %v", cs, err)
		}
	}
}

func TestServiceNoteValidateRequiresServerID(t *testing.T) {
	n := ServiceNote{ServiceName: "api", ServiceKind: ServiceContainer}
	if err := n.Validate(); !errors.Is(err, ErrMissingField) {
		t.Fatalf("空 server_id 应被拒, got %v", err)
	}
}

func TestServiceNoteValidateRejectsBadPort(t *testing.T) {
	base := func() ServiceNote {
		return ServiceNote{ServerID: uuid.New(), ServiceName: "api", ServiceKind: ServiceContainer}
	}
	for name, port := range map[string]int{"0": 0, "负数": -1, "超范围": 70000} {
		t.Run(name, func(t *testing.T) {
			n := base()
			n.Port = &port
			if err := n.Validate(); !errors.Is(err, ErrInvalidFormat) {
				t.Fatalf("port=%d 应被拒, got %v", port, err)
			}
		})
	}
}

func TestParseAssetStatus(t *testing.T) {
	for _, ok := range []AssetStatus{AssetActive, AssetRetired, AssetPlanned} {
		if _, err := ParseAssetStatus(string(ok)); err != nil {
			t.Fatalf("%s 应合法: %v", ok, err)
		}
	}
	if _, err := ParseAssetStatus("ACTIVE"); err == nil {
		t.Fatal("大写不应被接受——只接受精确小写值")
	}
}

func TestParseServiceKind(t *testing.T) {
	for _, ok := range []ServiceKind{ServiceContainer, ServiceSystemd, ServiceProcess} {
		if _, err := ParseServiceKind(string(ok)); err != nil {
			t.Fatalf("%s 应合法: %v", ok, err)
		}
	}
	if _, err := ParseServiceKind("docker"); err == nil {
		t.Fatal("非枚举值不应被接受")
	}
}

// TestExpiringWithin 钉住到期日警示的判据：未登记（零值）永远不警示，
// 已过期同样算「需要关注」（不是只对着未来那一侧判断）。
func TestExpiringWithin(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	within30 := 30 * 24 * time.Hour

	if ExpiringWithin(time.Time{}, now, within30) {
		t.Fatal("零值到期日（未登记）不该被判定为即将到期")
	}
	if !ExpiringWithin(now.AddDate(0, 0, -5), now, within30) {
		t.Fatal("已过期的到期日应算「需要关注」")
	}
	if !ExpiringWithin(now.AddDate(0, 0, 10), now, within30) {
		t.Fatal("10 天后到期应落在 30 天警示窗口内")
	}
	if ExpiringWithin(now.AddDate(0, 0, 45), now, within30) {
		t.Fatal("45 天后到期不该落在 30 天警示窗口内")
	}
}
