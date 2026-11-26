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
      // The pages of the web app stay here, /oauth/consent among them.
      ...(mode === 'mock'
        ? {}
        : {
            proxy: {
              '/api': { target: backend, changeOrigin: true },
              '^/oauth/(authorize|token|register)$': { target: backend, changeOrigin: true },
              '/.well-known': { target: backend, changeOrigin: true },
            },
          }),
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
      // The contract suite needs a real server (make contract) and has its own mode.
      include: mode === 'contract' ? ['src/api/contract.test.ts'] : ['src/**/*.test.ts', 'src/**/*.test.tsx'],
      exclude: mode === 'contract' ? [] : ['**/node_modules/**', 'src/api/contract.test.ts'],
      // Worker threads start faster and use less memory than forked processes, which
      // matters most on a runner with two cores. Every file still gets a fresh jsdom
      // (isolate stays on: some files keep state in modules and in the window).
      pool: 'threads',
      // Vitest leaves one core free by default, which on a two-core runner means one
      // worker and an idle core. VITEST_MAX_WORKERS (make's SHARE) says how many.
      maxWorkers: Number(process.env.VITEST_MAX_WORKERS) || Math.max(2, availableParallelism() - 1),
      // Markdown conversion of the corpus under jsdom is CPU-heavy on slow machines.
      testTimeout: 30_000,
    },
  }
})
