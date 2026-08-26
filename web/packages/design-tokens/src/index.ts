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
    accent: "var(--xm-color-accent)",
    accentStrong: "var(--xm-color-accent-strong)",
    accentFg: "var(--xm-color-accent-fg)",
    danger: "var(--xm-color-danger)",
    dangerFg: "var(--xm-color-danger-fg)",
    success: "var(--xm-color-success)",
    warning: "var(--xm-color-warning)",
  },
  font: { sans: "var(--xm-font-sans)", mono: "var(--xm-font-mono)" },
  radius: { sm: "var(--xm-radius-sm)", md: "var(--xm-radius-md)", lg: "var(--xm-radius-lg)" },
  shadow: { sm: "var(--xm-shadow-sm)", md: "var(--xm-shadow-md)" },
} as const;

export type Tokens = typeof tokens;
