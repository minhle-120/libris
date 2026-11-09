import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

const backend = 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [svelte()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: backend },
      '/mcp': { target: backend }
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true
  }
});
