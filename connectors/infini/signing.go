// Package infini 是 Infini 卡服务的连接器。
//
// 与仓库里其余九个连接器的根本区别：这一个**会写**。开卡、充值、冻结、
// 赎回都是花真钱的不可逆操作，走的是 connector.VendorWriteTransport
// 而不是 ReadOnlyTransport。
//
// ADR-018 的四道只读闸约束的是平台对 NewAPI/Sub2API 这类「平台不拥有其
// 业务真相」的上游；Infini 是平台作为客户去购买服务的供应商，调用它公开
// 的写接口不属于宪法条款 5 禁止的「直写第三方原始表」。
package infini

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// signingString 拼出待签名串。
//
// 格式来自文档第 4 章，逐字如下（末尾那个换行是必须的）：
//
//	{keyId}\n{METHOD} {path}\ndate: {GMT}\n
//
// path 含查询串。请求体不参与签名——Digest 是独立的头，不进这里。
func signingString(keyID, method, pathWithQuery, date string) string {
	return keyID + "\n" +
		method + " " + pathWithQuery + "\n" +
		"date: " + date + "\n"
}

// signature 用密钥对待签名串做 HMAC-SHA256，输出 base64（不是 hex）。
func signature(secret, signing string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// authorizationHeader 组装 Authorization 头。
//
// headers="@request-target date" 是固定值：签名涵盖范围由文档钉死，
// 不是我们可以选的。
func authorizationHeader(keyID, sig string) string {
	return `Signature keyId="` + keyID +
		`",algorithm="hmac-sha256",headers="@request-target date"` +
		`,signature="` + sig + `"`
}

// digestHeader 计算 Digest 头的值；空请求体返回空串，调用方据此决定不发这个头。
func digestHeader(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sum := sha256.Sum256(body)
	return "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
}
