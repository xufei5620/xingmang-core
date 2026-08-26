import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
export default defineConfig({
  plugins: [react()],
  // globals: 让 @testing-library/react 的自动 cleanup 挂上 afterEach
  test: { environment: "jsdom", globals: true },
});
