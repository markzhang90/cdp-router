import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: {
    chunkSizeWarningLimit: 1400,
    rollupOptions: {
      output: {
        manualChunks: {
          echarts: ["echarts"]
        }
      }
    }
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:5100"
    }
  }
});
