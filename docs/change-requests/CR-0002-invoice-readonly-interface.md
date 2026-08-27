# CR-0002:平台 ↔ 开票系统只读对接需求

- 状态:**待开票线确认**(开票线已在 #49 两轮反馈门禁,本文档已按其修订;
  确认前本 CR 不构成跨线批准,XM-0028 契约保持草案)
- 提出方:平台线(Claude)
- 承接方:开票系统线(Win Codex)
- 依据:ADR-018(开票/平台通道隔离,四道只读闸)、ADR-004(Connector 隔离)、
  ADR-014(CredentialRef)、宪法 7/12 条

## 背景与目标

开票系统基础版已完成。平台需要以**只读**方式把它接入运营看板(开票量、金额、
失败/待处理数、日汇总),与 Sub2API 完全同一模式:平台侧已有可复用的
Connector 骨架、新鲜度模型、契约测试套件(见 connectors/sub2api/)。

**平台承诺(四道只读闸,ADR-018)**:
1. 连接配置校验拒绝任何可写意图(Config.Validate);
2. HTTP 客户端只放行 GET/HEAD,目标域名精确 allowlist,拒绝重定向;
3. 契约接口只声明读方法,写能力在类型层面不存在;
4. 契约测试断言所有能力 IsWrite()==false。
凭据只经 CredentialRef 引用,明文不入平台代码/日志/前端;平台**永不**直写
开票系统业务表。

## 请求开票系统提供(Codex 侧待填)

1. **只读 HTTP 端点**(https,建议独立只读路由前缀如 /readonly/v1/):
   - 开票记录分页查询(时间范围 + 状态过滤;字段清单请列出,金额一律整数最小
     货币单位 + 币种,禁止 float);
   - 单日汇总(业务日 2006-01-02:开票张数、总金额、失败数、待处理数);
   - 状态枚举清单(与语义说明)。
2. **数据新鲜度语义**:每个响应带观测时间(数据截至何时)与水位(watermark,
   用于判断是否读到完整区间);没有就说明如何推导。
3. **只读凭证**:形态(Header token?Basic?)、发放与轮换方式;平台侧将以
   secret://invoice-<env>/<name> 引用。
4. **版本与健康**:GET /version(可探测版本号)与 GET /healthz;平台的兼容
   矩阵将钉住你们声明的版本。
5. **环境**:staging 先行;生产端点与网络可达性(平台从哪个网段访问)。

## 平台侧勘察补充(2026-08-27,只读勘察 invoice-system 代码后按事实修订)

勘察结论(引用均为 invoice-system 仓库路径):

- **金额纪律已合规**:全后端零 float,`amount_minor BIGINT` + `currency CHAR(3)`
  (migrations/0001_init.sql:104-128),无需整改。
- **状态枚举 9 项已存在且 DB/Go 一致**(0001_init.sql:124-127、internal/domain/types.go:59-67):
  pending_review / needs_changes / approved / rejected / user_cancelled /
  manual_issuing / issued_awaiting_document / issued / refund_attention。
- **时区已合规**:全部 TIMESTAMPTZ,显示时区固定 Asia/Shanghai(0011:30)。
- **水位机制已存在**:source_economic_stream_watermarks 的 min(watermark_at)
  已在 consumption.go:369 使用;开票记录自身可用 max(updated_at)。
- **/healthz 已存在**(httpapi/server.go:155-157);**/version 不存在**(最小缺口,建议首件做)。
- **⚠️ 现有 admin 查询端点不可复用**:admin_dto.go:10 直接内嵌 domain.InvoiceRequest,
  会吐出 TaxID/BankAccount/Address/Phone/Email 全量 PII;且浏览器会话 cookie 绑定
  IP+UA、明确拒绝 Authorization 头(production_auth.go:135-137),机器无法持有。
  **只读投影必须新建,DTO 手写字段白名单,禁止 embed 领域结构。**
- **凭证地基已有**:internal/auth/bearer.go 有完整、带测试、未接线的 OIDC bearer
  校验器——接到新的 /readonly/v1/ 分支即可,/api/v1/ 拒绝 bearer 的行为保持不变。
- **staging 层级不存在**(APP_ENV 仅 development/production),补齐属中-大工程。

### 建议实现(Codex 侧,多数零件已存在)

1. 新增 `/readonly/v1/` 路由树,只注册 GET;DTO 白名单:id / request_no / status /
   amount_minor / currency / source_type / submitted_at / updated_at(**无 profile/税号/邮箱**);
2. `GET /readonly/v1/invoice-requests?from=&to=&status=&limit=&cursor=`
   (复用现有分页/状态过滤,RequestPageQuery 补 From/To 两字段);
3. `GET /readonly/v1/daily-summary?date=YYYY-MM-DD`(按 Asia/Shanghai 业务日现算
   count / sum(amount_minor) / 失败数 / 待处理数,无需建表);
4. 响应带 `observed_at` + `watermark_at`(复用现成水位);
5. 凭证:Keycloak client_credentials + invoice-readonly role,只在 /readonly/v1/
   校验 audience/azp;平台以 secret://invoice-<env>/<name> 引用;
6. `GET /version`(ldflags 注入 tag+sha),/healthz 补同一版本字段。

## 验收标准(修订:staging 降级,避免阻塞)

- [ ] Codex 在本文件「待填」各节补全并提 PR(状态枚举/金额语义/时区/healthz 四节
      现在就能填,无需写代码);
- [ ] Codex 在 **dev compose(AUTH_MODE=mock)** 上暴露 /readonly/v1/,平台 curl
      能取到带水位的样例数据(staging 层级暂不要求,生产再切 bearer);
- [ ] 平台侧完成 connectors/invoice 契约 + Fake + 契约测试(XM-0028,不被阻塞,先行);
- [ ] bearer 凭证落地后平台完成真实客户端(XM-0029)。

## 明确不变(开票线要求,平台承诺)

- 平台**永不**导入/复用开票系统 Bridge V4 的角色、函数、Agent、凭据;
- 平台**不复制、不推导**开票资格算法;daily-summary 只聚合开票系统已确定的请求事实;
- 「失败数/待处理数」的状态集合口径由**开票系统冻结**,平台不自行解释 9 个状态;
- 现有 /api/v1/ 与 admin 路由的鉴权行为不因本 CR 改变。

## 环境映射(诚实原则)

开票系统现仅有 development/production 两级。平台 staging 读取 invoice development 时:
- 响应与看板必须保留真实 `source_environment=development`,并带醒目非生产标识;
- **不得**把 invoice development 冒充 staging;production 权限绝不从任何环境继承。
是否新增隔离的 integration 环境由开票线决定,本 CR 不预设。

## dev mock 的定位

`AUTH_MODE=mock` 联调仅为**开发契约 smoke**(验证 DTO/分页/水位形状),
**不构成只读安全验收**。staging/只读凭证验收保持未完成状态;
production bearer 完成前不得宣称 ADR-018 四闸通过。

## Wire 契约补充(采纳开票线意见)

- 金额:HTTP JSON 中 `amount_minor` 以**十进制字符串**编码 int64(防经
  map/float64 在 >2^53 丢精度),平台解码使用 UseNumber 并做范围校验;
  同时返回 ISO 4217 currency;多币种必须分组,禁止跨币种求和;
- 水位:`watermark` 的作用域、单调性、与分页快照的一致性、source 迟到/失败时
  如何标 `is_partial`——**语义由开票线冻结后写入本节**,平台不自行推导
  (min(watermark_at) 与 max(updated_at) 不视为天然等价的完整性水位);
- 公共包络:environment、来源 observed_at(请求时刻不得冒充)、is_partial、
  contract_version;
- 版本:`GET /readonly/v1/version` 返回 service_version / contract_version /
  capabilities,未知或不兼容版本平台侧 fail closed;**不改动既有 /healthz 响应**;
- 错误:安全 error_code,不返回供应商原文。

## 服务端只读证明(ADR-018 两闸回补)

- /readonly/v1/ 数据访问使用只读 DB 角色或显式 read-only transaction(等价的
  库层不可写证明);
- 运行身份仅授予手写投影所需 SELECT;启动或验收测试查询实际 grants,
  证明写 SQL、admin handler、导出与原业务 API 均不可达;
- 平台 token 对全部写/管理/导出路径在 handler 之前被拒,并有测试。

## Keycloak 变更(独立 CR,占位 CR-0003)

`client_credentials + invoice-readonly` 触碰身份基础设施,须另立可执行变更单:
Realm(不动被 ADR-016 冻结的 solov 用户 Realm;若用 solov-staff 或第二 issuer
须显式列明并经开票线确认)、issuer、client_id、audience、azp、scope/role 映射、
token TTL、轮换/吊销、回滚方案、执行者。**本 CR 不含任何 Keycloak 执行内容。**

## 职责边界

- 本 CR 文档的修订由**平台线**提交,开票线(Win Codex)在 Issue #49 复核确认,
  不编辑平台仓库;
- /readonly/v1/ 的实施在开票系统仓库的独立任务中进行;
- 平台 connectors/invoice 归 XM-0028/0029,且 XM-0028 在双方确认前为**草案**,
  其 Fake/契约测试字段不构成冻结。

## 平台侧任务链

XM-0028 invoice 只读契约 + Fake + 契约测试(**不被本 CR 阻塞,先行**)→
XM-0029 真实 InvoiceClient + 看板卡片(依赖上述凭证与端点落地)。
