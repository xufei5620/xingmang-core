package localauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 固定要求 HOTP/TOTP 使用 SHA1 作为 HMAC 摘要函数；这不是通用哈希用途。
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP 参数（任务书固定值，RFC 6238 默认）：30 秒步长、6 位数字、SHA1、
// 允许 ±1 步的时钟偏移窗口（覆盖手机与服务器之间常见的几十秒漂移，
// 与大多数认证器 App/服务端实现的默认容忍度一致）。
const (
	totpDigits      = 6
	totpPeriod      = 30 * time.Second
	totpWindowSteps = 1
	totpSecretBytes = 20 // 160 bit，RFC 4226 §4 建议的密钥长度
	totpIssuer      = "xingmang"
)

// GenerateTOTPSecret 生成一把新的随机 TOTP 密钥（160 bit）。
func GenerateTOTPSecret() ([]byte, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成 TOTP 密钥失败: %w", err)
	}
	return buf, nil
}

// EncodeTOTPSecret 把密钥编码成人可抄写、认证器 App 可粘贴的 Base32 串
// （标准字母表、无填充——与 Google Authenticator 等主流实现的手动输入格式一致）。
func EncodeTOTPSecret(secret []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
}

// DecodeTOTPSecret 反解 EncodeTOTPSecret 的输出；同时容忍用户手抄时混入的
// 空白与小写字母（认证器 App 之间对大小写/空格的处理并不统一）。
func DecodeTOTPSecret(encoded string) ([]byte, error) {
	clean := strings.ToUpper(strings.Join(strings.Fields(encoded), ""))
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("TOTP 密钥不是合法 base32: %w", err)
	}
	return b, nil
}

// hotp 实现 RFC 4226 的 HOTP(K, C)：HMAC-SHA1 摘要 + 动态截断（§5.3/§5.4）。
// digits 由调用方指定，便于用 RFC 6238 附录 B 的 8 位测试向量核对本函数，
// 生产路径固定传 totpDigits（6 位）。
func hotp(secret []byte, counter uint64, digits int) string {
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)

	mac := hmac.New(sha1.New, secret)
	mac.Write(counterBytes[:])
	sum := mac.Sum(nil)

	// 动态截断（RFC 4226 §5.3 Step 2）：取摘要末字节低 4 位作偏移，
	// 从该偏移起 4 字节、清最高位后转成 31 位整数。
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%mod)
}

// totpCounter 把绝对时间折算成 RFC 6238 的时间步计数 T = floor((t - T0) / X)，
// T0 固定为 0（Unix 纪元），X = totpPeriod。
func totpCounter(t time.Time, period time.Duration) uint64 {
	steps := t.Unix() / int64(period.Seconds())
	if steps < 0 {
		return 0
	}
	return uint64(steps)
}

// GenerateTOTP 返回 now 所在时间步的 6 位动态码，供确认启用/测试使用。
func GenerateTOTP(secret []byte, now time.Time) string {
	return hotp(secret, totpCounter(now, totpPeriod), totpDigits)
}

// VerifyTOTP 校验 code 是否落在 now 前后各 totpWindowSteps 个时间步之内
// （RFC 6238 §6：允许一定的时钟偏移容忍）。只接受恰好 totpDigits 位的纯数字，
// 且用常数时间比较，避免逐字符比较把"哪一位先不匹配"泄漏成时序信号。
func VerifyTOTP(secret []byte, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	center := int64(totpCounter(now, totpPeriod))
	ok := false
	for delta := -totpWindowSteps; delta <= totpWindowSteps; delta++ {
		counter := center + int64(delta)
		if counter < 0 {
			continue
		}
		candidate := hotp(secret, uint64(counter), totpDigits)
		// 逐个候选都跑常数时间比较（不在命中后提前退出提前泄漏"第几个窗口
		// 命中"的时序差异；三次比较的总耗时差异在网络往返噪声下不可观测）。
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			ok = true
		}
	}
	return ok
}

// BuildOTPAuthURI 构造 otpauth://totp/ 形态的注册 URI（Key URI Format，
// 认证器 App 扫码/手动输入均按这个形状解析）。issuer 与 username 都必须是
// ASCII——username 已被 core.staff_account 的 CHECK 约束限定为
// ^[a-z0-9][a-z0-9._-]{1,63}$，issuer 固定为下面的 totpIssuer 常量，
// 不接受运行时可配置的非 ASCII 值（部分认证器 App 对 label 里的非 ASCII
// 字符处理不一致，ASCII 是唯一保证跨 App 兼容的选择）。
func BuildOTPAuthURI(username string, secret []byte) string {
	label := url.PathEscape(totpIssuer) + ":" + url.PathEscape(username)
	q := url.Values{}
	q.Set("secret", EncodeTOTPSecret(secret))
	q.Set("issuer", totpIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", int(totpPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// --- 恢复码 ---

const (
	// RecoveryCodeCount 是确认启用 TOTP 时一次性生成的恢复码数量。
	RecoveryCodeCount = 10
	recoveryCodeLen   = 10
	// recoveryAlphabet 与 generatePassword 用的是同一张表（去掉易混淆的
	// l/I/1/O/0）：恢复码是要被人抄下来存放在安全的地方的一次性凭据，
	// 抄错一个字符就永久作废，字母表设计的取舍与生成密码完全一致。
	recoveryAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// GenerateRecoveryCode 生成一个随机恢复码（原始明文，仅在生成的那一次
// 响应里出现，调用方负责只落哈希）。
func GenerateRecoveryCode() (string, error) {
	buf := make([]byte, recoveryCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成恢复码失败: %w", err)
	}
	out := make([]byte, recoveryCodeLen)
	for i, v := range buf {
		out[i] = recoveryAlphabet[int(v)%len(recoveryAlphabet)]
	}
	return string(out), nil
}

// GenerateRecoveryCodes 一次生成 RecoveryCodeCount 个互不相同的恢复码。
func GenerateRecoveryCodes() ([]string, error) {
	seen := make(map[string]struct{}, RecoveryCodeCount)
	codes := make([]string, 0, RecoveryCodeCount)
	for len(codes) < RecoveryCodeCount {
		code, err := GenerateRecoveryCode()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[code]; dup {
			continue // 20^10 空间里几乎不会撞上；撞上就重抽，不允许重复码
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes, nil
}

// NormalizeRecoveryCode 规整用户输入：去首尾空白、去内部空格与短横线
// （展示时按 5-5 分组用短横线分隔，输入时不应强制要求原样带回），大写化
// （字母表本身大小写混合，但比对前统一大写可以容忍用户手抄时的大小写误记——
// 字母表已经去掉了会因大小写混淆产生碰撞的字符，统一大写不会引入歧义）。
func NormalizeRecoveryCode(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r == ' ' || r == '-' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// HashRecoveryCode 返回恢复码规整后的 sha256 十六进制摘要（与
// core.staff_totp_recovery_code.code_hash 的 CHECK 约束同一形态）。
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}
