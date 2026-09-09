package adminsettings

import (
	"context"
	"strings"
	"testing"
)

// XM-INV-NOTICE-WEBHOOK-SETTING：地址搬进管理端设置。
//
// 这一组钉的是**地址永远不出去**：不进展示、不进错误、不进审计口径。

const testWebhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=super-secret-value"

func noticeService(t *testing.T) (*Service, *memoryRepo) {
	t.Helper()
	repo := &memoryRepo{}
	return NewService(repo, testBox{}), repo
}

func noticeActor() Actor {
	return Actor{ID: "admin-1", RequestID: "req-1"}
}

func TestSetNoticeWebhookRejectsBadShapesWithoutEchoingTheAddress(t *testing.T) {
	svc, _ := noticeService(t)
	for _, address := range []string{
		"", "   ",
		"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=super-secret-value",
		"://bad",
	} {
		_, err := svc.SetNoticeWebhook(context.Background(), address, noticeActor())
		if err == nil {
			t.Fatalf("address %q must be rejected", address)
		}
		// 这个错误会一路回到管理端页面上。
		if strings.Contains(err.Error(), "super-secret-value") {
			t.Fatalf("错误回显了凭据：%v", err)
		}
	}
}

func TestSetNoticeWebhookStoresItAndShowsOnlyAFingerprint(t *testing.T) {
	svc, _ := noticeService(t)
	info, err := svc.SetNoticeWebhook(context.Background(), testWebhook, noticeActor())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Configured {
		t.Fatal("保存之后应当是「已配置」")
	}
	if info.UpdatedBy != "admin-1" {
		t.Fatalf("updated_by=%q", info.UpdatedBy)
	}
	// 展示面里**一个字符的地址都不许有**。
	if strings.Contains(info.Fingerprint, "qyapi") || strings.Contains(info.Fingerprint, "super-secret-value") ||
		strings.Contains(info.Fingerprint, "key=") {
		t.Fatalf("指纹里夹带了地址片段：%q", info.Fingerprint)
	}
	if !strings.HasPrefix(info.Fingerprint, "sha256:") || len(info.Fingerprint) != len("sha256:")+16 {
		t.Fatalf("指纹形状不对：%q", info.Fingerprint)
	}
	// 同一个地址算出同一个指纹（可以拿去和"我刚才粘的那个"比对）；
	// 不同地址算出不同指纹。
	if NoticeWebhookFingerprint(testWebhook) != info.Fingerprint {
		t.Fatal("指纹必须是地址的稳定函数")
	}
	if NoticeWebhookFingerprint(testWebhook+"2") == info.Fingerprint {
		t.Fatal("不同地址必须给出不同指纹")
	}
	// 首尾空白不该算成另一个地址。
	if NoticeWebhookFingerprint("  "+testWebhook+"  ") != info.Fingerprint {
		t.Fatal("指纹应当先 trim")
	}
}

func TestNoticeWebhookForDeliveryRoundTripsAndIsSeparateFromDisplay(t *testing.T) {
	svc, _ := noticeService(t)
	if _, err := svc.SetNoticeWebhook(context.Background(), testWebhook, noticeActor()); err != nil {
		t.Fatal(err)
	}
	got, err := svc.NoticeWebhookForDelivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != testWebhook {
		t.Fatalf("投递取到的地址不是存进去的那个：%q", got)
	}
	// 展示接口与投递接口是两条路：前者永远不含地址。
	info, err := svc.NoticeWebhook(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(info.Fingerprint+info.UpdatedBy, "qyapi") {
		t.Fatalf("展示面泄漏了地址：%+v", info)
	}
}

func TestClearNoticeWebhookMakesDeliveryFailClosed(t *testing.T) {
	svc, _ := noticeService(t)
	if _, err := svc.SetNoticeWebhook(context.Background(), testWebhook, noticeActor()); err != nil {
		t.Fatal(err)
	}
	info, err := svc.ClearNoticeWebhook(context.Background(), noticeActor())
	if err != nil {
		t.Fatal(err)
	}
	if info.Configured || info.Fingerprint != "" {
		t.Fatalf("清除之后不该还有指纹：%+v", info)
	}
	if _, err = svc.NoticeWebhookForDelivery(context.Background()); err == nil {
		t.Fatal("清除之后投递必须取不到地址（fail closed），不能回退到某个旧值")
	}
}

// 没有 actor 就不许写：审计里必须有人。
func TestNoticeWebhookWritesRequireAnActor(t *testing.T) {
	svc, _ := noticeService(t)
	if _, err := svc.SetNoticeWebhook(context.Background(), testWebhook, Actor{}); err == nil {
		t.Fatal("缺 actor 必须被拒")
	}
	if _, err := svc.ClearNoticeWebhook(context.Background(), Actor{}); err == nil {
		t.Fatal("缺 actor 必须被拒")
	}
}
