import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发期走 Vite dev server；生产期由 Go 控制面直接托管 dist 产物
// （Caddy 已砍，静态资源走 Go 内置 HTTP）。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      // 后端 API 与控制面同机，开发期代理过去
      '/api': {
        target: 'https://127.0.0.1:8443',
        changeOrigin: true,
        // 控制面用自签证书，开发期忽略校验
        secure: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
    // noVNC 使用 top-level await，需要 esnext 目标
    target: 'esnext',
  },
})
