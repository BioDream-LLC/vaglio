// Separate from vite.config.ts because vitest 2 ships its own vite 5 types, which do not accept a vite 6 config.
// tsconfig.json covers src only, so neither config file is typechecked against the other's vite.
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: { environment: 'happy-dom' },
})
