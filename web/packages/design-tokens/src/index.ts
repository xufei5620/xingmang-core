/** 语义令牌名 → CSS 变量引用。JS 侧（如 ECharts 主题）从这里取值，
 *  禁止在业务代码内写死色值。 */
export const tokens = {
  color: {
    canvas: "var(--xm-color-canvas)",
    surface: "var(--xm-color-surface)",
    surfaceMuted: "var(--xm-color-surface-muted)",
    fg: "var(--xm-color-fg)",
    fgMuted: "var(--xm-color-fg-muted)",
    edge: "var(--xm-color-edge)",
    edgeStrong: "var(--xm-color-edge-strong)",
    accent: "var(--xm-color-accent)",
    accentStrong: "var(--xm-color-accent-strong)",
    accentFg: "var(--xm-color-accent-fg)",
    accentSoft: "var(--xm-color-accent-soft)",
    danger: "var(--xm-color-danger)",
    dangerFg: "var(--xm-color-danger-fg)",
    success: "var(--xm-color-success)",
    warning: "var(--xm-color-warning)",
    overlay: "var(--xm-color-overlay)",
  },
  /** 深色左侧导航专用。与 color.* 分开是因为它**不随主题翻转**——
   *  浅色运营台配深色导航是这套设计的固定结构（UI 交接文档 §11.1）。 */
  nav: {
    surface: "var(--xm-color-nav-surface)",
    fg: "var(--xm-color-nav-fg)",
    fgMuted: "var(--xm-color-nav-fg-muted)",
    edge: "var(--xm-color-nav-edge)",
    hover: "var(--xm-color-nav-hover)",
    activeBg: "var(--xm-color-nav-active-bg)",
    activeFg: "var(--xm-color-nav-active-fg)",
    accent: "var(--xm-color-nav-accent)",
  },
  font: { sans: "var(--xm-font-sans)", mono: "var(--xm-font-mono)" },
  radius: {
    sm: "var(--xm-radius-sm)",
    md: "var(--xm-radius-md)",
    lg: "var(--xm-radius-lg)",
    xl: "var(--xm-radius-xl)",
  },
  shadow: { sm: "var(--xm-shadow-sm)", md: "var(--xm-shadow-md)" },
} as const;

export type Tokens = typeof tokens;
