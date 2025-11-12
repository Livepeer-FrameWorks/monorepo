import { defineConfig, loadEnv } from 'vite'
import path from 'path'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')

  const host = env.HOST ?? '0.0.0.0'
  const port = Number(env.PORT ?? 9004)

  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    server: {
      host,
      port,
      fs: {
        // allow importing markdown and local library from repo root
        allow: [path.resolve(__dirname, '..')]
      }
    },
    build: {
      outDir: 'dist',
      sourcemap: false
    }
  }
})
