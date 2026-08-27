package oidcauth

import (
	"encoding/json"
	"strconv"
)

// jwtHeader 是 JWS 保护头。只解析需要判定的字段。
type jwtHeader struct {
	Alg  string   `json:"alg"`
	Kid  string   `json:"kid"`
	Typ  string   `json:"typ"`
	Crit []string `json:"crit"`
}

// roleList 对应 Keycloak 的 {"roles": [...]} 结构。
type roleList struct {
	Roles []string `json:"roles"`
}

// claims 是本包关心的令牌声明。
//
// 刻意不做成 map[string]any：字段写下来就是一份「平台到底读了令牌里的什么」
// 的清单，新增一个字段是显式动作。map 会让「不小心开始依赖某个声明」变成
// 一次无人察觉的改动。
type claims struct {
	Issuer          string       `json:"iss"`
	Subject         flexString   `json:"sub"`
	Audience        audienceList `json:"aud"`
	AuthorizedParty string       `json:"azp"`
	ExpiresAt       *int64       `json:"exp"`
	NotBefore       *int64       `json:"nbf"`
	IssuedAt        *int64       `json:"iat"`

	// TokenType 是 Keycloak 的 typ 载荷声明（访问令牌为 "Bearer"，
	// ID Token 为 "ID"，刷新令牌为 "Refresh"），不是 JWS 头里的那个 typ。
	TokenType flexString `json:"typ"`

	PreferredUsername string     `json:"preferred_username"`
	ACR               flexString `json:"acr"`
	AMR               []string   `json:"amr"`

	// Scope 是 OAuth 的 scope 串。**平台从不采纳它**（ADR-016），
	// 解析出来只为巡检漂移，见 driftInScopeClaim。
	Scope string `json:"scope"`

	// RealmAccess 是唯一被采纳的角色来源（CR-0001 §5 的粗粒度 Realm Role）。
	RealmAccess roleList `json:"realm_access"`

	// ResourceAccess 是 Client 角色。同样**不采纳**，只巡检。
	ResourceAccess map[string]roleList `json:"resource_access"`
}

// audienceList 兼容 aud 的两种合法形态：字符串或字符串数组（RFC 7519 §4.1.3）。
type audienceList []string

func (a *audienceList) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*a = nil
		return nil
	}
	if b[0] == '[' {
		var xs []string
		if err := json.Unmarshal(b, &xs); err != nil {
			return err
		}
		*a = xs
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*a = audienceList{s}
	return nil
}

// flexString 兼容「同一个声明有的实现发字符串、有的发数字」的情况（典型是 acr）。
//
// 为什么不直接用 string：类型不符会让 json.Unmarshal 整体失败，于是一个无害的
// 表示差异（acr: 2 而不是 "2"）会变成「所有人都登不进来」。这类兼容要放在
// **解析**层，不能放到判定层——判定层一放宽就成了绕过。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	// 归一成十进制整数串；浮点形态（1.0）也统一成 "1"
	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		*f = flexString(strconv.FormatInt(i, 10))
		return nil
	}
	*f = flexString(n.String())
	return nil
}
