package localauth

import (
	"strings"
	"testing"
	"time"
)

// rfc6238SHA1Secret 是 RFC 6238 附录 B 测试向量表用的 ASCII 密钥
// ("12345678901234567890"，20 字节)——规范明确说明测试向量直接用这串
// ASCII 字节本身作 HMAC key，不是它的 base32 编码。
var rfc6238SHA1Secret = []byte("12345678901234567890")

// TestHOTPMatchesRFC6238Appendix_B_SHA1 用 RFC 6238 附录 B 公开的 8 位
// SHA1 测试向量核对 hotp()（含动态截断）与 totpCounter() 的时间步折算是否
// 正确。生产路径用 6 位（totpDigits），但截断算法与位数无关：位数只影响
// 最后 `% 10^digits` 这一步，8 位向量通过即可证明核心算法正确，6 位只是
// 同一个正确算法在更小的模数下取值。
func TestHOTPMatchesRFC6238Appendix_B_SHA1(t *testing.T) {
	cases := []struct {
		unixTime int64
		want     string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, tc := range cases {
		ts := time.Unix(tc.unixTime, 0).UTC()
		counter := totpCounter(ts, totpPeriod)
		got := hotp(rfc6238SHA1Secret, counter, 8)
		if got != tc.want {
			t.Fatalf("time=%d counter=%d: hotp = %q, want %q", tc.unixTime, counter, got, tc.want)
		}
	}
}

// TestVerifyTOTPAcceptsCurrentStepWithProductionParams 用生产参数
// （6 位、30 秒步长）验证 VerifyTOTP 对"此刻"生成的码判真。
func TestVerifyTOTPAcceptsCurrentStepWithProductionParams(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	code := GenerateTOTP(secret, now)
	if len(code) != totpDigits {
		t.Fatalf("code = %q, want %d digits", code, totpDigits)
	}
	if !VerifyTOTP(secret, code, now) {
		t.Fatal("当前时间步生成的码应当校验通过")
	}
}

// TestVerifyTOTPAcceptsAdjacentStepsOnly 校验 ±1 步窗口：上一步/下一步的码
// 应放行，±2 步应拒绝。
func TestVerifyTOTPAcceptsAdjacentStepsOnly(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	prevStep := now.Add(-totpPeriod)
	nextStep := now.Add(totpPeriod)
	twoStepsAway := now.Add(-2 * totpPeriod)

	if !VerifyTOTP(secret, GenerateTOTP(secret, prevStep), now) {
		t.Fatal("上一步的码应在 ±1 窗口内放行")
	}
	if !VerifyTOTP(secret, GenerateTOTP(secret, nextStep), now) {
		t.Fatal("下一步的码应在 ±1 窗口内放行")
	}
	if VerifyTOTP(secret, GenerateTOTP(secret, twoStepsAway), now) {
		t.Fatal("±2 步之外的码不应放行")
	}
}

func TestVerifyTOTPRejectsMalformedCode(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, bad := range []string{"", "12345", "1234567", "12345a", " 123456", "abcdef"} {
		if VerifyTOTP(secret, bad, now) {
			t.Fatalf("非法输入 %q 不应校验通过", bad)
		}
	}
}

func TestVerifyTOTPRejectsWrongSecret(t *testing.T) {
	secretA, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	secretB, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	code := GenerateTOTP(secretA, now)
	if VerifyTOTP(secretB, code, now) {
		t.Fatal("用另一把密钥生成的码不应通过校验")
	}
}

func TestEncodeDecodeTOTPSecretRoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	encoded := EncodeTOTPSecret(secret)
	if encoded == "" || strings.ContainsAny(encoded, "01") {
		// base32 标准字母表不含数字 0/1（避免与 O/I 混淆），编码结果里
		// 出现它们说明用错了字母表或实现有误。
		t.Fatalf("encoded = %q 形态可疑", encoded)
	}
	decoded, err := DecodeTOTPSecret(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(secret) {
		t.Fatal("编码/解码应互逆")
	}

	// 容忍用户手抄时混入的空格与小写字母
	messy := strings.ToLower(encoded[:4]) + " " + encoded[4:]
	decodedMessy, err := DecodeTOTPSecret(messy)
	if err != nil {
		t.Fatal(err)
	}
	if string(decodedMessy) != string(secret) {
		t.Fatal("应容忍空白与大小写")
	}
}

func TestBuildOTPAuthURIShape(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	uri := BuildOTPAuthURI("alice", secret)
	if !strings.HasPrefix(uri, "otpauth://totp/xingmang:alice?") {
		t.Fatalf("uri = %q 形态不符", uri)
	}
	for _, want := range []string{
		"secret=" + EncodeTOTPSecret(secret),
		"issuer=xingmang",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Fatalf("uri = %q 缺少 %q", uri, want)
		}
	}
}

func TestGenerateRecoveryCodesAreUniqueAndHashable(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("len(codes) = %d, want %d", len(codes), RecoveryCodeCount)
	}
	seen := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		if len(c) != recoveryCodeLen {
			t.Fatalf("code %q 长度 = %d, want %d", c, len(c), recoveryCodeLen)
		}
		if _, dup := seen[c]; dup {
			t.Fatalf("恢复码重复: %q", c)
		}
		seen[c] = struct{}{}
		hash := HashRecoveryCode(c)
		if len(hash) != 64 {
			t.Fatalf("hash 长度 = %d, want 64", len(hash))
		}
	}
}

func TestNormalizeRecoveryCodeToleratesFormatting(t *testing.T) {
	raw := "aB3d-EfG7-h9"
	normalized := NormalizeRecoveryCode(raw)
	if normalized != "AB3DEFG7H9" {
		t.Fatalf("normalized = %q", normalized)
	}
	// 展示态带分隔符与原始态哈希应相等
	if HashRecoveryCode("AB3D-EFG7-H9") != HashRecoveryCode("ab3defg7h9") {
		t.Fatal("大小写/分隔符不同但内容相同的恢复码应哈希到同一个值")
	}
}
