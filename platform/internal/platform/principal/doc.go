// Package principal 提供跨模块的统一身份抽象（规格 §4.1）。
//
// 铁律：
//   - 业务模块不得直接依赖 Keycloak Realm 名、Group 名或 Role 名，只依赖本包；
//   - 机器身份（SERVICE/AI/SERVER_AGENT）不复用人类账号（ADR-005）；
//   - Principal 必须显式携带 Environment，生产权限不从测试继承（规格 §20.5）；
//   - 本包零 I/O、不依赖数据库与 HTTP，可被任何层引用。
package principal
