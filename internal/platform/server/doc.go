// Package server 是服务器登记簿（XM-SERVER0，2026-08-31）。
//
// 拍板原话：「服务器只做记录好了」——本包不装 Server Agent、不做任何
// SSH/docker/端口探测、不接实时监控。四张纯登记表，字段清单取自现有占位页
// ServerDetailPage.tsx 的蓝图态与 docs/architecture/ADMIN-IA.md §2.1：
//
//   - Asset       服务器资产：主机名、IP、机房/供应商、规格、状态、月付成本、到期日
//   - Supplier    供应商与购买账号（联系方式，不存密码——密码走 CredentialRef，
//     但本片未接凭据字段，见 handoff 的 follow_ups）
//   - ServerDomain 域名与证书生命周期（证书来源与到期日手工登记，不探测）
//   - ServiceNote  服务器上手工登记的服务/容器（不扫描 Docker）
//
// 与 internal/platform/finance 的登记簿同一套纪律：
//
//   - **写路径全部经 Action**（宪法 2 条），本包只提供领域类型、校验与仓储；
//   - **金额禁止 Float**（宪法 13 条）：月付成本落库为整数最小单位（scale 取决于
//     币种，见 internal/platform/money.CurrencyScale），Action 层只接受十进制
//     字符串参数，绝不经过 JSON number/float64。
//   - **日期是业务日期不是时间点**（宪法 14 条同一精神）：到期日用 date 而不是
//     timestamptz，本包用 time.Time 的零值表示「未登记」。
//   - **environment 显式**（宪法 15 条）：写操作的环境取自调用者 Principal，
//     不由参数自称；service_note 挂在 asset 下，不重复存 environment 列
//     （同 finance.token_map 挂在 upstream_account 下的理由）。
package server
