import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  base: './',
  plugins: [vue()],
  server: {
    port: 5173,
    // 本机 8080/8090 常被 llama-server / mihomo 占用，联调网关跑在 :9091（见 config.yaml）
    proxy: {
      '/api': 'http://localhost:9091',
      '/v1': 'http://localhost:9091',
    },
  },
  build: {
    outDir: 'dist',
    chunkSizeWarningLimit: 1500,
  },
})
