import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

const backend = 'http://127.0.0.1:8080';

/**
 * KaTeX's stylesheet names three font formats per family. Every browser in use takes the
 * woff2 and never asks for the other two, but a stylesheet that mentions a file is enough
 * for it to be copied into `dist/` — and `dist/` is committed and baked into the binary.
 * This drops the two formats nobody downloads: 876 KB and 40 files off every release.
 *
 * It runs before Vite resolves the urls, so the files are never emitted rather than
 * emitted and deleted afterwards, and it refuses to succeed if the rewrite ever eats the
 * woff2s along with them — a build that ships unstyled math is a build that says nothing.
 */
function woff2Only(): Plugin {
  return {
    name: 'woff2-only',
    enforce: 'pre',
    transform(code, id) {
      if (!id.includes('katex') || !id.endsWith('.css')) return;
      const out = code.replace(/,?url\([^)]*\.(?:woff|ttf)\) format\("[^"]*"\)/g, '');
      const kept = (out.match(/\.woff2/g) ?? []).length;
      if (kept < 20) throw new Error(`woff2-only: KaTeX's stylesheet lost its fonts (${kept} left)`);
      return { code: out, map: null };
    }
  };
}

export default defineConfig({
  plugins: [svelte(), woff2Only()],
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
