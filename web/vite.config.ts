import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ command, mode }) => {
  const env = loadEnv(mode, ".", "");
  if (
    command === "build" &&
    (env.VITE_API_MODE || "http").toLowerCase() === "mock"
  ) {
    throw new Error(
      "VITE_API_MODE=mock is development-only and cannot be built for production.",
    );
  }

  return {
    plugins: [react()],
    build: {
      emptyOutDir: true,
      rollupOptions: {
        treeshake: {
          // mock-api owns mutable demo fixtures at module scope. It is imported
          // for the dev-server client only and has no production side effects;
          // marking that single module lets Rollup remove every fixture from the
          // deployable bundle once the PROD client is fixed to HTTP.
          moduleSideEffects(id) {
            return !/[/\\]src[/\\]lib[/\\]mock-api\.ts$/.test(id);
          },
        },
      },
    },
    server: {
      host: "127.0.0.1",
      port: 5173,
      proxy: {
        "/api": "http://127.0.0.1:8088",
        "/healthz": "http://127.0.0.1:8088",
      },
    },
  };
});
