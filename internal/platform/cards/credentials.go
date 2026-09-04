package cards

import "strings"

// credentialScopePrefix 是凭据 scope 的前缀。
//
// 完整 scope 形如 infini-chris，与账号 id 一一对应。
const credentialScopePrefix = "infini-"

// 两个凭据的 name。keyId 是公开半边（每次请求都明文放在 Authorization 头里），
// secret 才是真正的秘密——分成两个引用是为了让它们能各自轮换。
const (
	credentialNameKeyID  = "api-key-id"
	credentialNameSecret = "api-secret"
)

// CredentialRefsFor 从账号 id 推出这个账号的两个 CredentialRef。
//
// **推导而不是配置**：少两个环境变量就是少两处可以配错的地方，而配错凭据
// 引用的症状（「凭据没配」）与值填错了长得一模一样，排查时分不开。
//
// scope 一律小写：管理端按这个 scope 写文件，客户端按同一个 scope 读，
// 两边大小写不一致会写进一个目录、读另一个目录。
func CredentialRefsFor(accountID string) (keyIDRef, secretRef string) {
	scope := credentialScopePrefix + strings.ToLower(strings.TrimSpace(accountID))
	return "secret://" + scope + "/" + credentialNameKeyID,
		"secret://" + scope + "/" + credentialNameSecret
}
