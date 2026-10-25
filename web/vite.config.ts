import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const backendTarget = env.VITE_DEV_BACKEND_TARGET || 'http://localhost:8080'

  return {
    plugins: [react()],
    server: {
      host: '0.0.0.0',
      port: 5173,
      proxy: {
        '/health': {
          target: backendTarget,
          changeOrigin: true,
        },
        '/api': {
          target: backendTarget,
          changeOrigin: true,
        },
      },
    },
  }
})

