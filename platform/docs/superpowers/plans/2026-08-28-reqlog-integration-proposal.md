# 请求详情打通提案(reqlog 连接器)

> 状态:待产品负责人批准(按 2026-08-28 新规矩:为 UI 适配的后端调整须经同意)。
> 依据:UI 交接文档 §9.4 请求详情契约 + 用户提供的《请求审计系统》实现报告
> (artifact c4d39419,fiberstate:/root/reqlog,2026-08-25 上线)。

## 1. 发现:捕获层已存在且在生产运行

另一 AI 已在生产服务器外挂部署「请求审计系统」(下称 reqlog):

- **架构**:宝塔 nginx 与 NewAPI(:3000)/Sub2API(:8081) 之间的 Go 透明反向代理
  (:9301/:9302),零改上游源码;nginx backup 直连兜底(代理故障弃记录不断站)。
- **捕获**:仅 /v1/* POST;全量明文 req_body/resp_body(含完整 SSE 事件流)、
  token_prefix、in/out/cache token、upstream_request_id(X-Oneapi-Request-Id /
  X-Request-Id)、status/dur/ttfb、model/stream/ip/ua;敏感请求头落盘前截断。
- **存储**:单条 gzip + 当日索引行,按天分目录,30 天自动清理;~6.5GB/日。
- **查看**:127.0.0.1:9300 只读控制台(Basic Auth,SSH 隧道访问);
  令牌→用户名映射每 10 分钟从两库只读拉取。
- **规模**:~13k 请求/日,常驻内存 ~12GB(其首要盯防项),零重启。

结论:交接文档 §9.4 请求详情页的**数据层不需要平台重建**。平台角色 =
带权限与审计的只读网关;**请求正文永不落平台库**(PII 契约层排除,6.5GB/日
也不该复制)。

## 2. 打通架构

```
reqlog 控制台 API(127.0.0.1:9300,Basic Auth)
  ↑ staging 期:用户自开 SSH 隧道 / 最终态:平台部署在同机
平台 reqlog 连接器(GET-only + 主机 allowlist + CredentialRef;fake→real 同 Sub2API 模式)
  ↑
Query 层:①元数据分页列表(无正文) ②单条内容按需读(scope request.content.read,
  每次查看落审计事件,流式转发不缓存不落库)
  ↑
前端:平台页签「请求」(列表)+ 请求详情完整页(§9.4:分角色渲染/截断显示/
  默认无导出——与 reqlog 自带控制台的「下载原文」有意不同)
```

## 3. 需批准的后端调整清单

1. 统一平台页签模板增加「请求」页签(Sub2API/NewAPI 生效,其余平台无此数据源则不显示);
2. 新增 Query:`GET /api/v1/platforms/{p}/requests`(元数据分页)与
   `GET /api/v1/platforms/{p}/requests/{id}`(内容,scope `request.content.read`);
3. 新增审计事件类型 `request.content.viewed`(谁、何时、看了哪条);
4. RoleScopeMap 新增 `request.read` / `request.content.read`;
5. 契约新增 `contracts/connectors/reqlog`(只读,元数据/内容两形状)。

红线自查:不改上游 ✅(reqlog 本就是外挂,平台只读其 API);凭据走
CredentialRef、倾向用户自配 ✅;内容不落库、默认无导出 ✅;金额无涉 ✅。

## 4. 缺的输入(向用户)

1. **reqlog 控制台 HTTP API 形状**(列表/详情/统计端点与字段名)。最省事:
   把 /root/reqlog 的单文件 Go 源码(1,336 行)发来看一眼,或让写它的 AI
   出一页端点清单;**不需要任何凭据**。
2. staging 期是否用 SSH 隧道先接真实数据,还是先 fake 等平台上服务器再切。

## 5. 顺带价值(记录,不扩本切片)

- §9.3 用户管理的按用户请求聚合可复用同一连接器(注意 reqlog 仅 30 天窗);
- M1.5 模型保障:resp_body 是现成一致性证据源;
- upstream_request_id 锚点未来可做「站内日志 ↔ 明文」跨系跳转。
