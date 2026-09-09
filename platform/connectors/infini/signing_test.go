package infini

import "testing"

// 签名口径来自 Infini 文档第 4 章「授权与安全机制」：
//
//	待签名串 = {keyId}\n{METHOD} {path}\ndate: {GMT}\n
//	Authorization: Signature keyId="..",algorithm="hmac-sha256",
//	               headers="@request-target date",signature="{base64}"
//
// 注意两处反直觉的地方，都是文档明写的：
//   - 请求体**不参与**签名，Digest 只是一个独立的头；
//   - 待签名串以换行结尾，少一个换行就是 401。
//
// 这套口径尚未对真实端点验证过（密钥与 IP 白名单未就绪）。第一次真实调用
// 若返回 401，先怀疑这里，见 docs/handoffs/slices/XM-CARD0-infini-connector.md。
func TestSigningStringLayout(t *testing.T) {
	got := signingString("testkey", "GET", "/v2/cards/list?size=20", "Tue, 21 Jan 2025 12:00:00 GMT")
	want := "testkey\nGET /v2/cards/list?size=20\ndate: Tue, 21 Jan 2025 12:00:00 GMT\n"

	if got != want {
		t.Fatalf("待签名串不匹配\ngot  %q\nwant %q", got, want)
	}
}

// 期望值由 openssl 独立算出，不是拿本包的实现算的：
//
//	printf 'testkey\nGET /v2/cards/list?size=20\ndate: Tue, 21 Jan 2025 12:00:00 GMT\n' \
//	  | openssl dgst -sha256 -hmac "testsecret" -binary | base64
func TestSignatureMatchesIndependentlyComputedHMAC(t *testing.T) {
	const want = "UBQotuLdx+rQ8BO7Ft3YLOEgiKWyRtsUL7jbwv1XHCI="

	got := signature("testsecret", signingString("testkey", "GET", "/v2/cards/list?size=20", "Tue, 21 Jan 2025 12:00:00 GMT"))

	if got != want {
		t.Fatalf("签名 = %q, want %q", got, want)
	}
}

func TestAuthorizationHeaderFormat(t *testing.T) {
	got := authorizationHeader("testkey", "UBQotuLdx+rQ8BO7Ft3YLOEgiKWyRtsUL7jbwv1XHCI=")
	want := `Signature keyId="testkey",algorithm="hmac-sha256",headers="@request-target date",signature="UBQotuLdx+rQ8BO7Ft3YLOEgiKWyRtsUL7jbwv1XHCI="`

	if got != want {
		t.Fatalf("Authorization 头不匹配\ngot  %s\nwant %s", got, want)
	}
}

// Digest 只在有请求体时出现，算法是 Base64(SHA-256(body))。
// 期望值同样由 openssl 独立算出：
//
//	printf '{"product_id":1}' | openssl dgst -sha256 -binary | base64
func TestDigestHeaderMatchesIndependentlyComputedHash(t *testing.T) {
	const want = "SHA-256=W0SAyTJOpE6Ipbk+FKZDi8l18WoFR9c5T5lYAAiqf8M="

	got := digestHeader([]byte(`{"product_id":1}`))

	if got != want {
		t.Fatalf("Digest = %q, want %q", got, want)
	}
}

// 空请求体不该出现 Digest 头——发一个空 Digest 比不发更容易触发 400。
func TestDigestHeaderEmptyBodyYieldsNoHeader(t *testing.T) {
	if got := digestHeader(nil); got != "" {
		t.Fatalf("空请求体的 Digest = %q, want 空串", got)
	}
}
