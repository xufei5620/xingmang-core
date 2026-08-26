/// <reference types="vite/client" />

/** 前端可注入的配置项（构建时经 Vite `VITE_` 前缀注入）。
 *  显式声明而不是靠 vite/client 的索引签名兜底，否则拼错变量名不会报错。 */
interface ImportMetaEnv {
  /** 平台 API 基地址。留空走同源 + 开发代理（见 vite.config.ts）。 */
  readonly VITE_XM_API_BASE_URL?: string;
  /** 开发期身份。TODO(XM-0008): 换成 OIDC 后这三项由 Token 取代。 */
  readonly VITE_XM_PRINCIPAL_ID?: string;
  readonly VITE_XM_PRINCIPAL_TYPE?: string;
  readonly VITE_XM_SCOPES?: string;
  /** 显式查询环境。默认不传，理由见 src/api/config.ts。 */
  readonly VITE_XM_ENVIRONMENT?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
