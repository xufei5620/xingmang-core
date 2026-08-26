import type { Decorator, Preview } from "@storybook/react-vite";
import "../src/preview.css";

const withTheme: Decorator = (Story, ctx) => {
  document.documentElement.dataset.theme = ctx.globals.theme === "dark" ? "dark" : "light";
  return Story();
};

const preview: Preview = {
  globalTypes: {
    theme: {
      description: "明暗主题",
      toolbar: { title: "Theme", items: ["light", "dark"], dynamicTitle: true },
    },
  },
  initialGlobals: { theme: "light" },
  decorators: [withTheme],
};
export default preview;
