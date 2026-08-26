# 星芒统一控制平台（Xingmang Unified Control Platform）

自研、自托管的运营控制平面：通过统一身份、权限、Query/Action、Connector、
任务、告警、审计体系，对多个独立业务系统（Sub2API、NewAPI、CPA、开票、支付）
进行统一观察和受控管理。

- 架构基线：`docs/architecture/BASELINE-v2.1.md`（ADR 索引）
- 全部规则：`PROJECT-CONSTITUTION.md`
- AI 入口：`CLAUDE.md` / `AGENTS.md` / `GEMINI.md`
- 版本锁：`VERSIONS.lock`

## 一句话架构原则

> 后台统一，身份分域，权限统一，动作统一，数据分治，接口集成；
> 控制平台不进入用户实时请求路径、不拥有第三方业务真相、不直接写第三方业务原表。

## 一键启动（staging）

```bash
cp deploy/compose/.env.example deploy/compose/.env   # 填 DATABASE_PASSWORD
docker compose -p xingmang-launch -f deploy/compose/launch.yaml up -d --wait
```

完整步骤、验证方式与已知问题见 `docs/runbooks/LAUNCH.md`。
这一档跑的是 Fake 连接器的构造数据（演示/联调用），不是业务真相。

## 开发

- 后端：`go test ./...`（Go 版本由 go.mod 决定，GOTOOLCHAIN=auto 自动获取）
- 前端：`pnpm install && pnpm -r run typecheck && pnpm -r run test`
- Storybook：`pnpm --filter ui-storybook run build`
- 治理检查：`bash scripts/check-governance.sh`
