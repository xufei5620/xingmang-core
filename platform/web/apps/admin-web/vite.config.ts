import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      // 平台 API 默认绑回环 127.0.0.1:8080（cmd/platform-api/config.go，规格 §21.2
      // 要求由宿主 Nginx 反代）。本地 compose 通过 web/Nginx 暴露在 8088 时，
      // 可用 XM_DEV_API_TARGET 覆盖；前端仍只发同源相对路径，生产构建不受影响。
      "/api": {
        target: process.env.XM_DEV_API_TARGET ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
