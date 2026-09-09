import type { Decorator, Preview } from "@storybook/react-vite";
import "../src/preview.css";

/** 主题三态与 tokens.css 的三个守卫一一对应：
 *  light → `[data-theme="light"]`，dark → `[data-theme="dark"]`，
 *  system → **不写属性**，交给 `prefers-color-scheme` 那条守卫接管。
 *
 *  system 这一档必须在：它是三态里唯一没法靠切换属性看到的一态，
 *  而「跟随系统」恰恰是绝大多数人实际会用的那一档。 */
const withTheme: Decorator = (Story, ctx) => {
  const theme = ctx.globals["theme"];
  if (theme === "system") delete document.documentElement.dataset["theme"];
  else document.documentElement.dataset["theme"] = theme === "dark" ? "dark" : "light";
  return Story();
};

const preview: Preview = {
  globalTypes: {
    theme: {
      description: "明暗主题（system = 跟随 prefers-color-scheme）",
      toolbar: { title: "Theme", items: ["light", "dark", "system"], dynamicTitle: true },
    },
  },
  initialGlobals: { theme: "light" },
  decorators: [withTheme],
};
export default preview;
