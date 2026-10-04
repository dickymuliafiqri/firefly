import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

/**
 * `vite build` empties the output directory (emptyOutDir defaults to true),
 * which strips the tracked `frontend/dist/.gitkeep` sentinel. That sentinel is
 * what makes `//go:embed all:dist` (frontend/embed.go) compile on a clean
 * clone, or right after `make clean`, when no bundled assets exist yet.
 * Re-emit an empty `.gitkeep` on every production build so it cannot be lost.
 */
function keepDistGitkeep(): Plugin {
  return {
    name: 'firefly:keep-dist-gitkeep',
    apply: 'build',
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: '.gitkeep', source: '' });
    },
  };
}

/**
 * Dev proxy to Go gateway (default 127.0.0.1:8080).
 * When gateway is not running, hooks fall back to typed mocks (see services/api.ts).
 */
const GO_GATEWAY = process.env.FIREFLY_GATEWAY ?? 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [react(), keepDistGitkeep()],
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
