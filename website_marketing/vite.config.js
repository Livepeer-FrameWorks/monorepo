import { defineConfig } from 'vite'
import path from 'path'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    host: '0.0.0.0',
    port: 9004,
    fs: {
      // allow importing markdown and local library from repo root
      allow: [path.resolve(__dirname, '..')]
    }
  },
  build: {
    outDir: 'dist',
    sourcemap: false
  }
}) 
