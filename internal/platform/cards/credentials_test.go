package cards

import "testing"

// 凭据引用由账号 id 推出，不再单独配置。
//
// 少两个环境变量意味着少两处可以配错的地方——而配错凭据引用的症状是
// 「凭据没配」，与「值填错了」长得一模一样，排查时分不开。
func TestCredentialRefsAreDerivedFromAccountID(t *testing.T) {
	keyID, secret := CredentialRefsFor("CHRIS")

	if keyID != "secret://infini-chris/api-key-id" {
		t.Fatalf("keyId 引用 = %q", keyID)
	}
	if secret != "secret://infini-chris/api-secret" {
		t.Fatalf("secret 引用 = %q", secret)
	}
}

// 账号 id 大小写不该影响引用：配置里写 CHRIS，scope 一律小写。
// 两边不一致会让管理端写进 infini-CHRIS/，而客户端去读 infini-chris/。
func TestCredentialRefsLowercaseTheScope(t *testing.T) {
	upper, _ := CredentialRefsFor("LINFENG")
	lower, _ := CredentialRefsFor("linfeng")

	if upper != lower {
		t.Fatalf("大小写不该影响引用: %q vs %q", upper, lower)
	}
}

// 两个账号必须推出不同的引用——推成同一个等于两个账号共用一把密钥。
func TestCredentialRefsDifferPerAccount(t *testing.T) {
	a, _ := CredentialRefsFor("CHRIS")
	b, _ := CredentialRefsFor("LINFENG")

	if a == b {
		t.Fatal("不同账号必须推出不同的凭据引用")
	}
}
