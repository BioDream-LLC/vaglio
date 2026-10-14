import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The build goes straight into the Go package that embeds it.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    // `make serve` runs the engine on 8740; `npm run dev` proxies the API to it.
    // The engine answers on https only, with its own certificate by default.
    proxy: { '/api': { target: 'https://127.0.0.1:8740', secure: false } },
  },
})
