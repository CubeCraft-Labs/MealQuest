import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { VitePWA, type ManifestOptions } from 'vite-plugin-pwa'
import manifest from './manifest.json' with { type: 'json' }

const backend = 'http://localhost:8080'

export default defineConfig({
  plugins: [
    react(),
    // The plugin writes manifest.webmanifest and sw.js, links the manifest
    // from index.html and adds the registerSW.js script. It does both only
    // in production builds: dev options stay off, so neither `vite` nor
    // Vitest registers a service worker.
    VitePWA({
      strategies: 'injectManifest',
      srcDir: 'src',
      filename: 'sw.ts',
      injectRegister: 'script',
      // The web manifest lives in manifest.json: its theme and background
      // colours are --color-accent and --color-bg from src/styles/tokens.css,
      // and hex colours may not be written in TypeScript.
      manifest: manifest as Partial<ManifestOptions>,
    }),
  ],
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
