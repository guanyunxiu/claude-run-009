import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 开发环境把 /api 与 /ws 代理到 Go 网关；容器内由 nginx 反代。
export default defineConfig({
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.VITE_PROXY_TARGET || "http://localhost:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  worker: {
    format: "es",
  },
});
