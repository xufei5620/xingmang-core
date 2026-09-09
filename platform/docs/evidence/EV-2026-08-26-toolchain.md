# EV-2026-08-26-toolchain：建仓日工具链与版本核对

核验日期：2026-08-26。来源：go.dev/dl JSON、npm registry `npm view <pkg> version`、
Docker Hub manifest、quay.io tag API、GitHub `repos/*/git/ref/tags/*`。

## 结论

1. Go 官方最新稳定版 1.27.0，与规格支持线 Go 1.27.x 一致；本机 go 1.25.7 +
   GOTOOLCHAIN=auto 按 go.mod 自动使用 1.27.0。本机 GOPROXY 配置为
   `https://goproxy.cn,direct`（proxy.golang.org 经本地代理不稳定，CI 不受影响）。
2. React 19.2.8 / Vite 8.2.2 / Tailwind 4.3.3 / Storybook 10.5.10 / pnpm 11.24.0
   均落在规格 §5.2 支持线内，取精确 patch 钉入 VERSIONS.lock。
3. **TypeScript 钉 5.9.3**：npm latest 已到 7.0.2（原生化重写首个大版本），
   生态兼容未验证；按规格 §5.4"Major 独立评估"暂不采用，升级需独立评审。
4. **react-router 钉 7.18.2**：npm latest 已到 8.3.0（新 major）；Data Mode 在
   7.x 线稳定，同按"Major 独立评估"处理。
5. keycloak quay 现行最新 patch 为 26.7.2（规格 §5.3 示例一致）；本批不部署，
   digest 于 XM-0007 锁定。
6. postgres:18 digest 取自 `docker manifest inspect`（amd64 平台清单，
   sha256:7341002d…46d7）；生产首次 pull 时按 RepoDigest 复核。
7. GitHub Actions 各 tag 对应 commit SHA 已解析并钉入 workflow 与 VERSIONS.lock。
8. 分支保护已于 XM-0002 生效（PR 必须 + 4 项状态检查 + enforce_admins；
   单人阶段审批数 0，第二名人类成员加入后升为 1）。
