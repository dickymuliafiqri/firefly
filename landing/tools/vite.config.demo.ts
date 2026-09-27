import path from 'path';
import { fileURLToPath } from 'url';

// Demo dev-server config for capturing landing-page screenshots.
// Points the API proxy at a scratch Firefly instance (default :8088) loaded
// with landing/tools/demo-configs, so the machine's real gateway on :8080 is
// never touched. Run from frontend/:  bunx vite --config ../landing/tools/vite.config.demo.ts
const frontendRoot = fileURLToPath(new URL('../../frontend', import.meta.url));

// Vite bundles this config as CJS, so require() resolves here; anchor the
// paths into the frontend's node_modules because the config lives elsewhere.
const requireFromFrontend = (id: string) => require(path.join(frontendRoot, 'node_modules', id));
const { defineConfig } = requireFromFrontend('vite/dist/node/index.js');
const reactMod = requireFromFrontend('@vitejs/plugin-react/dist/index.js');
const react = reactMod.default ?? reactMod;

const backend = process.env.DEMO_BACKEND ?? 'http://127.0.0.1:8088';

export default defineConfig({
  root: frontendRoot,
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(frontendRoot, './src'),
    },
  },
  server: {
    port: 3003,
    strictPort: true,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      '/healthz': { target: backend, changeOrigin: true },
      '/v1': { target: backend, changeOrigin: true },
    },
  },
});
