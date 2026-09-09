package localauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id 参数是团队固定值，不对用户暴露成配置项。t=3、m=64MiB、p=2 是
// OWASP 密码存储备忘录给出的两组推荐配置之一（低内存场景）：在没有专门硬件
// 的登录 QPS 下足以抬高离线破解成本，又不会让单次登录请求的 CPU/内存开销
// 大到影响同一进程里其它请求的时延。
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB = 64 MiB
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	saltLen             = 16

	// MinPasswordLen / MaxPasswordLen 是口令策略。下限 10 是"不是一个四位数字"
	// 的最低门槛；上限 128 只是防止有人把一整份文档粘进密码框。
	MinPasswordLen = 10
	MaxPasswordLen = 128
)

var (
	// ErrPasswordPolicy：口令不满足长度策略。
	ErrPasswordPolicy = errors.New("password does not meet policy")
	// ErrHashFormat：PHC 串形态不合法（算法标识、版本、字段数不对，或
	// base64 解不出来）。
	ErrHashFormat = errors.New("invalid password hash format")
)

// ValidatePasswordPolicy 只校验长度：10..128 个 Unicode 码点。
//
// 刻意不做"必须含大小写/数字/符号"这类字符集规则——这类规则已被反复证明会
// 让人写出更容易猜的密码（改成可预测的"大写首字母 + 数字 + 感叹号结尾"）。
// 现在的共识（OWASP、NIST 800-63B）是长度优先，真正的强度来自 argon2id 的
// 计算成本，不是字符集规则。
func ValidatePasswordPolicy(pw string) error {
	n := len([]rune(pw))
	if n < MinPasswordLen || n > MaxPasswordLen {
		return fmt.Errorf("密码长度必须在 %d..%d 个字符之间: %w", MinPasswordLen, MaxPasswordLen, ErrPasswordPolicy)
	}
	return nil
}

// HashPassword 生成 argon2id 的 PHC 编码串：
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt-base64>$<hash-base64>
//
// base64 用 RawStdEncoding（标准字母表、无填充）——这是 PHC 规范的约定编码，
// 与 argon2 参考实现、libsodium 等生成的串互相兼容。
func HashPassword(pw string) (string, error) {
	if err := ValidatePasswordPolicy(pw); err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	hash := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword 用常数时间比较校验口令是否匹配 PHC 串。
//
// 返回的 bool 才是"密码对不对"的答案；error 只在**哈希串本身格式不对**时
// 非 nil（比如库里存了一条损坏的记录）。调用方不应把 error 当作"密码错误"
// 处理，也不应因为拿到 error 就跳过 dummy-hash 校验——这个区分是为了不把
// "内部数据坏了"和"用户输错密码"混为一谈。
func VerifyPassword(encodedHash, pw string) (bool, error) {
	salt, hash, t, m, p, keyLen, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(pw), salt, t, m, p, keyLen)
	return subtle.ConstantTimeCompare(hash, candidate) == 1, nil
}

func decodeHash(encoded string) (salt, hash []byte, timeCost, memory uint32, threads uint8, keyLen uint32, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("%w: 段数或算法标识不符", ErrHashFormat)
	}
	var version int
	if _, e := fmt.Sscanf(parts[2], "v=%d", &version); e != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("%w: version 段: %v", ErrHashFormat, e)
	}
	if version != argon2.Version {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("%w: 不支持的 version %d", ErrHashFormat, version)
	}
	var m, t uint32
	var p uint8
	if _, e := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); e != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("%w: 参数段: %v", ErrHashFormat, e)
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("%w: 盐不是合法 base64: %v", ErrHashFormat, e)
	}
	hash, e2 := base64.RawStdEncoding.DecodeString(parts[5])
	if e2 != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("%w: 哈希不是合法 base64: %v", ErrHashFormat, e2)
	}
	return salt, hash, t, m, p, uint32(len(hash)), nil
}

// dummyPasswordHash 是登录处理器在"用户名不存在"时用来占位比较的哈希：即便
// 没有真实账号可比，也要花掉与真实校验相同量级的 CPU，防止响应时延本身
// 泄漏"这个用户名存不存在"（经典的用户名枚举计时攻击）。
//
// 惰性计算而非包级变量初始化：本包在 dev-header / oidc 模式下也会被链接进
// 同一个 platform-api 二进制（cmd/platform-api/config.go 的三个模式分支都在
// 同一个可执行文件里），包级变量初始化在 import 时无条件执行，会让每一次
// 进程启动都白付一次 argon2id 的内存/CPU 成本，即便这次启动根本用不到本地
// 登录。sync.Once 把这笔成本推迟到第一次真正调用登录端点的时候。
var (
	dummyHashOnce sync.Once
	dummyHashVal  string
)

func dummyPasswordHash() string {
	dummyHashOnce.Do(func() {
		h, err := HashPassword("xm-login-dummy-constant-time-check")
		if err != nil {
			// 上面的常量满足长度策略，HashPassword 唯一的失败源是策略校验与
			// crypto/rand 读取失败，因此这条分支在实践中不可达；留一个语法
			// 合法但推不出任何真实密码的兜底串，不让本函数有 panic 或返回
			// 空串的可能。
			h = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}
		dummyHashVal = h
	})
	return dummyHashVal
}
