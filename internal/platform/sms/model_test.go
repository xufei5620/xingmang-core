package sms

import "testing"

// 只有确定失败的操作才允许重试。
//
// 买号重试一次就是再花一次钱。unknown 尤其不行——它的含义正是「不知道花没
// 花出去」，而人「觉得」没成功不构成依据，那种确定必须走人工核对。
func TestRetryOnlyAllowedForFailed(t *testing.T) {
	for _, state := range []OperationState{
		StatePrepared, StateSubmitted, StateSucceeded, StateUnknown,
		StateReconciledSucceeded, StateReconciledFailed,
	} {
		if (Operation{State: state}).RetryAllowed() {
			t.Fatalf("状态 %s 不该允许重试", state)
		}
	}
	if !(Operation{State: StateFailed}).RetryAllowed() {
		t.Fatal("确定失败应允许重试")
	}
}

// 62 只支持 purchase，五个生命周期动作都是 Hero 独有的。
//
// 在领域层挡掉而不是让它打到上游换一个含糊的 404——那种 404 看起来像
// 「号码不存在」，会把人引到完全错的方向。
func TestSMS62SupportsPurchaseOnly(t *testing.T) {
	if !SupportsAction(ProviderSMS62, KindPurchase) {
		t.Fatal("62 应支持购买")
	}
	for _, kind := range []string{KindCancel, KindFinish, KindReplace, KindReactivate, KindProlong} {
		if SupportsAction(ProviderSMS62, kind) {
			t.Fatalf("62 不该支持 %s", kind)
		}
		if !SupportsAction(ProviderHero, kind) {
			t.Fatalf("Hero 应支持 %s", kind)
		}
	}
}

// 请求指纹**不含 operation ID**：换个 UUID 重发同一份请求要被挡住。
func TestRequestHashIgnoresOperationID(t *testing.T) {
	params := map[string]string{"goods_id": "1-2-3", "num": "2"}
	a := CanonicalRequestHash(ProviderSMS62, KindPurchase, params)
	b := CanonicalRequestHash(ProviderSMS62, KindPurchase, params)
	if a != b {
		t.Fatal("同一份请求必须算出同一个 hash")
	}
	// 参数变了就该是另一个 hash，否则未决索引会把两笔不同的购买当成一笔。
	params["num"] = "3"
	if CanonicalRequestHash(ProviderSMS62, KindPurchase, params) == a {
		t.Fatal("参数变了 hash 也要变")
	}
}

// 供应商不同 = 不同的请求，哪怕参数一模一样。
func TestRequestHashSeparatesProviders(t *testing.T) {
	params := map[string]string{"num": "1"}
	if CanonicalRequestHash(ProviderSMS62, KindPurchase, params) ==
		CanonicalRequestHash(ProviderHero, KindPurchase, params) {
		t.Fatal("两家的同参数请求不该撞 hash")
	}
}

// 掩码保留后四位：清单里区分两个号靠的就是那四位。
func TestMaskPhoneKeepsLastFour(t *testing.T) {
	got := MaskPhone("+1 (555) 013-5258")
	if len(got) < 4 || got[len(got)-4:] != "5258" {
		t.Fatalf("应保留后四位, got %q", got)
	}
	if got == "15550135258" {
		t.Fatal("中间几位必须被遮住")
	}
}

// 短号码全遮，不越界。
func TestMaskPhoneHandlesShortInput(t *testing.T) {
	if got := MaskPhone("123"); got != "***" {
		t.Fatalf("短号码应全遮, got %q", got)
	}
}

// token 指纹是单向的：**不能拿它去取码**。
//
// 用它当 external ID 是因为 ID 要进索引、日志与 URL，而 token 不能进这些
// 地方；这条测试钉住「指纹 ≠ token」这个前提。
func TestTokenFingerprintIsNotTheToken(t *testing.T) {
	token := "secret-order-token"
	fp := TokenFingerprint(token)
	if fp == token {
		t.Fatal("指纹不能等于 token 本身")
	}
	if len(fp) != 64 {
		t.Fatalf("SHA-256 十六进制应是 64 位, got %d", len(fp))
	}
	if TokenFingerprint(token) != fp {
		t.Fatal("同一 token 必须算出同一指纹")
	}
}
