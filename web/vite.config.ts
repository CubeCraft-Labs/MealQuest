import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

const backend = 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': backend,
      '/auth': backend,
      '/media': backend,
    },
  },
  test: {
    environment: 'jsdom',
    // Inject CSS Modules into jsdom so tests can assert the sizes the
    // stylesheets actually declare (for example 44 px hit areas).
    css: true,
    setupFiles: ['./src/test/setup.ts'],
  },
})
