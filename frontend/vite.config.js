import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Build outputs to ../backend/web so the Go binary serves it from STATIC_DIR.
// During dev, /api is proxied to the Go server on :8080.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../backend/web',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
