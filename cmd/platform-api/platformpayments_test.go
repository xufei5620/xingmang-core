package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

// dynamicPaymentsQuerier 的测试(XM-PAY0):镜像 platformusers_test.go 的
// dynamicUsersClient 测试——覆盖的是 resolve 的决策表与平台分派，
// 不是 HTTP 解析细节(那部分由 connectors/{sub2api,newapi} 的契约测试覆盖)。

func fixedPaymentsConfigSource(bySource map[string]*jobs.ConnectorConfig) jobs.ConnectorConfigSource {
	return jobs.ConnectorConfigSourceFunc(func(_ context.Context, platform, _ string) (*jobs.ConnectorConfig, error) {
		return bySource[platform], nil
	})
}

func failingPaymentsConfigSource(err error) jobs.ConnectorConfigSource {
	return jobs.ConnectorConfigSourceFunc(func(context.Context, string, string) (*jobs.ConnectorConfig, error) {
		return nil, err
	})
}

func samplePaymentsWindow() (time.Time, time.Time) {
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 23, 59, 59, 0, time.UTC)
	return from, to
}

func TestParsePaymentsMode(t *testing.T) {
	cases := map[string]paymentsMode{"": paymentsModeFake, "fake": paymentsModeFake, "FAKE": paymentsModeFake,
		"real": paymentsModeReal, "off": paymentsModeOff, " off ": paymentsModeOff}
	for input, want := range cases {
		got, err := parsePaymentsMode(input)
		if err != nil {
			t.Fatalf("parsePaymentsMode(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("parsePaymentsMode(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parsePaymentsMode("bogus"); err == nil {
		t.Fatal("未知模式应报错")
	}
}

func TestBuildPlatformPaymentsOffModeMountsNothing(t *testing.T) {
	q := buildPlatformPayments(paymentsModeOff, platformPaymentsDeps{})
	if q != nil {
		t.Fatalf("off 模式应返回 nil, got %+v", q)
	}
	if iface := platformPaymentsOrNil(q); iface != nil {
		t.Fatal("platformPaymentsOrNil(nil) 必须是真正的 nil 接口，不能是包着 nil 指针的非 nil 接口")
	}
}

func TestBuildPlatformPaymentsFakeModeWithoutPoolStillWorks(t *testing.T) {
	q := buildPlatformPayments(paymentsModeFake, platformPaymentsDeps{Secrets: noopSecrets()})
	if q == nil {
		t.Fatal("fake 模式即便没有 Pool 也该能构造出可用的 Querier")
	}
	from, to := samplePaymentsWindow()
	result, err := q.ListOrders(context.Background(), httpapi.PlatformOrdersInput{
		Platform: "sub2api", Environment: "development", From: from, To: to,
	})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if result.Source != "sub2api-fake" {
		t.Fatalf("Source = %q, want sub2api-fake", result.Source)
	}
	if len(result.Items) == 0 {
		t.Fatal("fake 客户端应给出若干笔样本订单")
	}
}

func TestDynamicPaymentsQuerierRejectsUnknownPlatform(t *testing.T) {
	q := &dynamicPaymentsQuerier{defaultMode: paymentsModeFake, secrets: noopSecrets()}
	from, to := samplePaymentsWindow()
	_, err := q.ListOrders(context.Background(), httpapi.PlatformOrdersInput{
		Platform: "bogus", Environment: "development", From: from, To: to,
	})
	if connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("未知平台分类 = %q, want not_supported (err=%v)", connector.KindOf(err), err)
	}
}

// TestDynamicPaymentsQuerierProductionFakeGate：生效模式为 fake 且环境为
// production 时必须 not_supported，绝不把演示订单当真实经营数据展示
// （宪法 12 条），与 dynamicUsersClient 同一纪律，两个平台各测一次。
func TestDynamicPaymentsQuerierProductionFakeGate(t *testing.T) {
	from, to := samplePaymentsWindow()

	t.Run("sub2api_进程缺省就是fake", func(t *testing.T) {
		q := &dynamicPaymentsQuerier{defaultMode: paymentsModeFake, secrets: noopSecrets()}
		_, err := q.ListOrders(context.Background(), httpapi.PlatformOrdersInput{
			Platform: "sub2api", Environment: "production", From: from, To: to,
		})
		if connector.KindOf(err) != connector.KindNotSupported {
			t.Fatalf("分类 = %q, want not_supported (err=%v)", connector.KindOf(err), err)
		}
		if !errors.Is(err, jobs.ErrConnectorProductionFake) {
			t.Fatalf("应能用 errors.Is 认出 jobs.ErrConnectorProductionFake: %v", err)
		}
	})

	t.Run("newapi_数据库行把生效模式覆盖成fake", func(t *testing.T) {
		// 进程缺省是 real，但后台配置行显式写了 fake——生效模式以行为准，
		// 闸门必须照样触发，不能因为「进程启动时选的是 real」就放行。
		q := &dynamicPaymentsQuerier{
			defaultMode: paymentsModeReal, secrets: noopSecrets(),
			configSource: fixedPaymentsConfigSource(map[string]*jobs.ConnectorConfig{
				jobs.ConnectorPlatformNewAPI: {Platform: jobs.ConnectorPlatformNewAPI, Environment: "production", Mode: "fake"},
			}),
		}
		_, err := q.ListOrders(context.Background(), httpapi.PlatformOrdersInput{
			Platform: "newapi", Environment: "production", From: from, To: to,
		})
		if connector.KindOf(err) != connector.KindNotSupported {
			t.Fatalf("分类 = %q, want not_supported (err=%v)", connector.KindOf(err), err)
		}
	})

	t.Run("非生产环境放行fake", func(t *testing.T) {
		q := &dynamicPaymentsQuerier{defaultMode: paymentsModeFake, secrets: noopSecrets()}
		result, err := q.ListOrders(context.Background(), httpapi.PlatformOrdersInput{
			Platform: "sub2api", Environment: "staging", From: from, To: to,
		})
		if err != nil {
			t.Fatalf("非生产环境的 fake 不该被拒: %v", err)
		}
		if result.Source != "sub2api-fake" {
			t.Fatalf("Source = %q, want sub2api-fake", result.Source)
		}
	})
}

// TestDynamicPaymentsQuerierConfigUnavailableFallsBackToEnvDefault：库读不到
// 时按进程级缺省处理，不是硬错误——与 dynamicUsersClient 同一条纪律。
func TestDynamicPaymentsQuerierConfigUnavailableFallsBackToEnvDefault(t *testing.T) {
	from, to := samplePaymentsWindow()
	q := &dynamicPaymentsQuerier{
		defaultMode:  paymentsModeFake,
		secrets:      noopSecrets(),
		configSource: failingPaymentsConfigSource(fmt.Errorf("connection refused")),
	}
	result, err := q.ListOrders(context.Background(), httpapi.PlatformOrdersInput{
		Platform: "newapi", Environment: "staging", From: from, To: to,
	})
	if err != nil {
		t.Fatalf("库读不到应当回落到 env 缺省，而不是报错: %v", err)
	}
	if result.Source != "newapi-fake" {
		t.Fatalf("Source = %q, want newapi-fake", result.Source)
	}
}

// TestDynamicPaymentsQuerierRejectsUnparsableMode：后台配置行的 mode 列写了
// 一个不认识的值，必须报错，而不是悄悄回落到进程缺省。
func TestDynamicPaymentsQuerierRejectsUnparsableMode(t *testing.T) {
	from, to := samplePaymentsWindow()
	q := &dynamicPaymentsQuerier{
		defaultMode: paymentsModeFake, secrets: noopSecrets(),
		configSource: fixedPaymentsConfigSource(map[string]*jobs.ConnectorConfig{
			jobs.ConnectorPlatformSub2API: {Platform: jobs.ConnectorPlatformSub2API, Environment: "development", Mode: "bogus"},
		}),
	}
	_, err := q.ListOrders(context.Background(), httpapi.PlatformOrdersInput{
		Platform: "sub2api", Environment: "development", From: from, To: to,
	})
	if connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("分类 = %q, want internal (err=%v)", connector.KindOf(err), err)
	}
}

// TestDynamicPaymentsQuerierIndependentPlatformRows：sub2api 与 newapi 的
// connector_config 行必须互不影响——一个平台切到 real 不该连带另一个平台。
//
// 直接测 resolve* 而不经过 ListOrders：real 模式下 ListOrders 会真的发起
// 只读 HTTP 读取，这里只关心"两个平台的配置行解析互不干扰"这件事，不需要
// 也不该为此拉起一次真实（或哪怕是失败的）网络调用去拖慢测试。
func TestDynamicPaymentsQuerierIndependentPlatformRows(t *testing.T) {
	q := &dynamicPaymentsQuerier{
		defaultMode: paymentsModeFake, secrets: noopSecrets(),
		configSource: fixedPaymentsConfigSource(map[string]*jobs.ConnectorConfig{
			jobs.ConnectorPlatformSub2API: {Platform: jobs.ConnectorPlatformSub2API, Environment: "production", Mode: "real",
				Endpoint: "https://sub2api.example.test", TargetAllowlist: []string{"sub2api.example.test"},
				CredentialRef: "secret://sub2api/readonly"},
			// newapi 行不存在（nil）：应回落到进程缺省 fake。
		}),
	}

	sub2Mode, sub2Cfg, err := q.resolveSub2API(context.Background(), "production")
	if err != nil {
		t.Fatalf("resolveSub2API: %v", err)
	}
	if sub2Mode != jobs.Sub2APIModeReal {
		t.Fatalf("sub2api 应按数据库行切到 real, got %q", sub2Mode)
	}
	if sub2Cfg.Endpoint != "https://sub2api.example.test" {
		t.Fatalf("sub2api Endpoint = %q, want 数据库行里的值", sub2Cfg.Endpoint)
	}

	newapiMode, _, err := q.resolveNewAPI(context.Background(), "production")
	if err != nil {
		t.Fatalf("resolveNewAPI: %v", err)
	}
	if newapiMode != jobs.NewAPIModeFake {
		t.Fatalf("newapi 没有配置行，应回落到进程缺省 fake, got %q", newapiMode)
	}
}

func TestLoadPaymentsDefaultsFallBackToPackageDefaultInstanceID(t *testing.T) {
	t.Setenv("XM_SUB2API_INSTANCE_ID", "")
	t.Setenv("XM_NEWAPI_INSTANCE_ID", "")
	sub2apiCfg := loadSub2APIPaymentsDefaults()
	if sub2apiCfg.InstanceID != jobs.DefaultSub2APIInstanceID {
		t.Fatalf("sub2api InstanceID = %q, want %q", sub2apiCfg.InstanceID, jobs.DefaultSub2APIInstanceID)
	}
	newapiCfg := loadNewAPIPaymentsDefaults()
	if newapiCfg.InstanceID != jobs.DefaultNewAPIInstanceID {
		t.Fatalf("newapi InstanceID = %q, want %q", newapiCfg.InstanceID, jobs.DefaultNewAPIInstanceID)
	}
}
