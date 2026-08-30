package localauth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashPasswordAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong password entirely")
	if err != nil || ok {
		t.Fatalf("错误密码不该通过: ok=%v err=%v", ok, err)
	}
}

func TestHashPasswordIsSaltedPerCall(t *testing.T) {
	h1, err := HashPassword("same-password-both-times")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword("same-password-both-times")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("两次哈希同一密码应得到不同的串（随机盐）")
	}
	for _, h := range []string{h1, h2} {
		ok, err := VerifyPassword(h, "same-password-both-times")
		if err != nil || !ok {
			t.Fatalf("h=%q ok=%v err=%v", h, ok, err)
		}
	}
}

func TestHashPasswordFormat(t *testing.T) {
	hash, err := HashPassword("a-valid-password-123")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		t.Fatalf("hash 形态不符 PHC 编码: %q", hash)
	}
	if parts[3] != "m=65536,t=3,p=2" {
		t.Fatalf("参数段 = %q", parts[3])
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	cases := []struct {
		n    int
		want bool
	}{
		{9, false}, {10, true}, {128, true}, {129, false},
	}
	for _, c := range cases {
		err := ValidatePasswordPolicy(strings.Repeat("a", c.n))
		if c.want && err != nil {
			t.Errorf("长度 %d 应通过, got %v", c.n, err)
		}
		if !c.want && !errors.Is(err, ErrPasswordPolicy) {
			t.Errorf("长度 %d 应拒绝并返回 ErrPasswordPolicy, got %v", c.n, err)
		}
	}
}

func TestHashPasswordRejectsPolicyViolation(t *testing.T) {
	if _, err := HashPassword("short"); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("过短密码应在 HashPassword 处就被拒绝, got %v", err)
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for name, h := range map[string]string{
		"完全不是 PHC 串":  "not-a-valid-hash",
		"算法标识不对":      "$bcrypt$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"version 不支持": "$argon2id$v=1$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"参数段损坏":       "$argon2id$v=19$bogus$c2FsdA$aGFzaA",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyPassword(h, "whatever12345"); !errors.Is(err, ErrHashFormat) {
				t.Fatalf("应返回 ErrHashFormat, got %v", err)
			}
		})
	}
}

func TestDummyPasswordHashIsStableAndValidPHC(t *testing.T) {
	h1 := dummyPasswordHash()
	h2 := dummyPasswordHash()
	if h1 != h2 {
		t.Fatal("dummyPasswordHash 应在同一进程内保持稳定（sync.Once）")
	}
	if _, _, _, _, _, _, err := decodeHash(h1); err != nil {
		t.Fatalf("dummy hash 应是可解码的合法 PHC 串: %v", err)
	}
	// 用它校验任意密码都应该是"合法调用、结果为 false"，不是报错
	ok, err := VerifyPassword(h1, "anything")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
