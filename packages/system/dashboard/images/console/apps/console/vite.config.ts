import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"
import path from "path"

/**
 * The dev server proxies `/api` and `/apis` to `kubectl proxy --port 8001`
 * (over HTTP, not HTTPS — kubectl proxy terminates TLS locally).
 * The VNC console streams over a WebSocket, so `ws: true` is set on the
 * proxies below to forward the upgrade to kubectl proxy.
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@/": path.resolve(__dirname, "./src/"),
    },
  },
  server: {
    port: 3001,
    proxy: {
      "/k8s": {
        target: "http://localhost:8001",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/k8s/, ""),
        ws: true,
      },
      "/apis": {
        target: "http://localhost:8001",
        changeOrigin: true,
        ws: true,
      },
      "/api": {
        target: "http://localhost:8001",
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
