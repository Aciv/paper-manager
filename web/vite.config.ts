import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// Development-only proxy. The production Nginx image proxies /api/ to the Go
// API itself; Vite needs this equivalent rule so relative API paths do not
// fall through to index.html during `npm run dev`.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  return {
    plugins: [react()],
    server: {
      proxy: {
        '/api': {
          target: env.VITE_API_PROXY_TARGET || 'http://localhost:8080',
          changeOrigin: true,
        },
      },
    },
  };
});
