import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
export default defineConfig({
  resolve: { dedupe: ["react", "react-dom", "react-router"] },
  plugins: [react()],
  // globals: 让 @testing-library/react 的自动 cleanup 挂上 afterEach
  // setupFiles: 补 jsdom 缺的 Pointer Capture，Radix Dialog/Select 才能在测试里打开
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"] },
});
