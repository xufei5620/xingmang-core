import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      // 平台 API 绑回环 127.0.0.1:8080（cmd/platform-api/config.go，规格 §21.2
      // 要求由宿主 Nginx 反代）。开发时由 Vite 转发，前端因此总用同源相对路径，
      // 不需要把后端地址编进代码，生产走反代时行为一致
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
    },
  },
});
