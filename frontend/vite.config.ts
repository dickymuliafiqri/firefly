import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

/**
 * Dev proxy ke Go gateway (default 127.0.0.1:8080).
 * Saat gateway tidak jalan, hooks jatuh ke typed mock (lihat services/api.ts).
 */
const GO_GATEWAY = process.env.FIREFLY_GATEWAY ?? 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': '/src',
    },
  },
  server: {
    proxy: {
      '/api': { target: GO_GATEWAY, changeOrigin: true },
      '/healthz': { target: GO_GATEWAY, changeOrigin: true },
      '/v1': { target: GO_GATEWAY, changeOrigin: true },
    },
  },
});
