/// <reference types="vite/client" />

/** 前端可注入的配置项（构建时经 Vite `VITE_` 前缀注入）。
 *  显式声明而不是靠 vite/client 的索引签名兜底，否则拼错变量名不会报错。 */
interface ImportMetaEnv {
  /** 平台 API 基地址。留空走同源 + 开发代理（见 vite.config.ts）。 */
  readonly VITE_XM_API_BASE_URL?: string;
  /** 开发期身份。仅显式 dev-header 模式可用。 */
  readonly VITE_XM_PRINCIPAL_ID?: string;
  readonly VITE_XM_PRINCIPAL_TYPE?: string;
  readonly VITE_XM_SCOPES?: string;
  /** 显式查询环境。默认不传，理由见 src/api/config.ts。 */
  readonly VITE_XM_ENVIRONMENT?: string;
  /** 鉴权方式的**构建期**回落值（XM-AUTH1）：dev-header | local。
   *  运行时的 /app-config.js（window.__XM_CONFIG__）优先于这几项，
   *  见 src/auth/runtimeConfig.ts。 */
  readonly VITE_XM_AUTH_MODE?: string;
  /** 演示数据横幅模式："demo" 强制显示 / "real" 强制关闭 / 其余按 source 自动判定。 */
  readonly VITE_XM_DATA_BADGE?: string;
  /** 已知演示实例的 source 列表（逗号分隔）；不配用 lib/demoData 里的默认值。 */
  readonly VITE_XM_DEMO_SOURCES?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
