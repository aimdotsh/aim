import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: resolve(__dirname, '../internal/webui/dist'),
    emptyOutDir: true,
    sourcemap: false
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8080'
    }
  }
})
