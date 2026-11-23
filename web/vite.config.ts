import { availableParallelism } from 'node:os'
import path from 'node:path'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig, type PluginOption } from 'vite'

const backend = 'http://127.0.0.1:8080'

export default defineConfig(async ({ mode }) => {
  const plugins: PluginOption[] = [react(), tailwindcss()]
  // The in-memory API only exists in `npm run dev:mock`; a build never imports it.
  if (mode === 'mock') {
    const { mockApi } = await import('./dev/mock.ts')
    plugins.push(mockApi())
  }
  return {
    plugins,
    resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
    // The Go server only accepts browser requests from the origin it was told
    // about (--allow-origin http://localhost:5173), so the port is fixed.
    server: {
      port: 5173,
      strictPort: true,
      ...(mode === 'mock' ? {} : { proxy: { '/api': { target: backend, changeOrigin: true } } }),
    },
    build: {
      outDir: 'dist',
      emptyOutDir: true,
      chunkSizeWarningLimit: 700,
    },
    test: {
      environment: 'jsdom',
      setupFiles: ['src/testing/setup.ts'],
      // A waited-for state (a menu closing, a handle appearing) comes a moment
      // later on a busy runner than on a laptop; one second was too little there.
      // A test that passes waits no longer: polling stops as soon as it holds.
      expect: { poll: { timeout: 5_000 } },
      include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
      // Worker threads start faster and use less memory than forked processes, which
      // matters most on a runner with two cores. Every file still gets a fresh jsdom
      // (isolate stays on: some files keep state in modules and in the window).
      pool: 'threads',
      // Vitest leaves one core free by default, which on a two-core runner means one
      // worker and an idle core.
      maxWorkers: Math.max(2, availableParallelism() - 1),
      // Markdown conversion of the corpus under jsdom is CPU-heavy on slow machines.
      testTimeout: 30_000,
    },
  }
})
