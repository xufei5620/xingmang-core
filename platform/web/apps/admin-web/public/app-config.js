// 前端运行时配置的占位文件（XM-AUTH1）。
//
// 容器里这个文件会在 nginx 启动前被 deploy/docker/web-app-config.sh 按环境变量
// **整个覆盖**（XM_WEB_AUTH_MODE / XM_WEB_OIDC_ISSUER / XM_WEB_OIDC_CLIENT_ID）。
// 本地 `vite dev` 与裸构建产物用的就是这份空壳：不定义 window.__XM_CONFIG__，
// 于是 src/auth/runtimeConfig.ts 回落到 VITE_XM_* 变量，再回落到 dev-header。
//
// 留一个空壳而不是让 index.html 引用一个不存在的文件，是因为 SPA 兜底会把
// 404 的 /app-config.js 回成 index.html——浏览器拿 HTML 当脚本解析，控制台
// 就多一条与本意无关的语法错误。
//
// 手动覆盖的话形状是：
//   window.__XM_CONFIG__ = { authMode: "oidc", oidcIssuer: "https://…/realms/solov-staff",
//                            oidcClientId: "xingmang-admin-web" };
