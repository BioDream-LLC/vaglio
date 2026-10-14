import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The build goes straight into the Go package that embeds it.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    // `make serve` runs the engine on 8740; `npm run dev` proxies the API to it.
    proxy: { '/api': 'http://127.0.0.1:8740' },
  },
})
