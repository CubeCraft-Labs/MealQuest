// @vitest-environment node
// Builds the app with the production config and serves the result, so the
// assertions cover what ships in dist: the web manifest, the service worker,
// every icon, and the service worker registration that only builds include.
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { build, createServer, preview, type PreviewServer, type ViteDevServer } from 'vite'
import tokens from './styles/tokens.css?raw'

const outDir = 'node_modules/.tmp/pwa-test-dist'

const icons = [
  { path: '/icons/icon-192.png', size: 192 },
  { path: '/icons/icon-512.png', size: 512 },
  { path: '/icons/icon-maskable-512.png', size: 512 },
  { path: '/apple-touch-icon.png', size: 180 },
]

function token(name: string): string {
  const value = tokens.match(new RegExp(`^\\s*${name}:\\s*([^;\\s]+);`, 'm'))?.[1]
  if (!value) throw new Error(`${name} is not defined in tokens.css`)
  return value
}

// Width and height from a PNG's IHDR chunk, which follows the 8-byte
// signature, the chunk length and the chunk type.
// The precache list vite-plugin-pwa injects into sw.js: a JSON array of
// { revision, url } entries.
function precacheEntries(sw: string): Array<{ revision: string | null; url: string }> {
  const start = sw.indexOf('[{"revision":')
  expect(start, 'injected precache list').toBeGreaterThanOrEqual(0)
  return JSON.parse(sw.slice(start, sw.indexOf('}]', start) + 2))
}

function pngSize(bytes: Uint8Array): { width: number; height: number } {
  const signature = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
  expect(Array.from(bytes.subarray(0, 8))).toEqual(signature)
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  return { width: view.getUint32(16), height: view.getUint32(20) }
}

describe('production build', () => {
  let server: PreviewServer
  let origin: string

  async function get(path: string): Promise<Response> {
    const response = await fetch(new URL(path, origin))
    expect(response.status, path).toBe(200)
    return response
  }

  beforeAll(async () => {
    await build({ logLevel: 'error', build: { outDir, emptyOutDir: true } })
    server = await preview({
      logLevel: 'error',
      build: { outDir },
      preview: { port: 0, strictPort: false, host: '127.0.0.1' },
    })
    const url = server.resolvedUrls?.local[0]
    if (!url) throw new Error('preview server did not report a URL')
    origin = url
  }, 60_000)

  afterAll(async () => {
    await server?.close()
  })

  it('emits manifest.webmanifest with the MealQuest identity', async () => {
    const manifest = await (await get('/manifest.webmanifest')).json()
    expect(manifest).toMatchObject({
      name: 'MealQuest',
      short_name: 'MealQuest',
      start_url: '/',
      scope: '/',
      display: 'standalone',
      // --color-accent and --color-bg hold the manifest colours; hex colours
      // may not be written outside tokens.css.
      theme_color: token('--color-accent'),
      background_color: token('--color-bg'),
    })
    expect(manifest.icons).toEqual([
      { src: '/icons/icon-192.png', sizes: '192x192', type: 'image/png', purpose: 'any' },
      { src: '/icons/icon-512.png', sizes: '512x512', type: 'image/png', purpose: 'any' },
      {
        src: '/icons/icon-maskable-512.png',
        sizes: '512x512',
        type: 'image/png',
        purpose: 'maskable',
      },
    ])
  })

  it.each(icons)('ships $path as a $size px square PNG', async ({ path, size }) => {
    const bytes = new Uint8Array(await (await get(path)).arrayBuffer())
    expect(pngSize(bytes)).toEqual({ width: size, height: size })
  })

  it('emits sw.js with the precache list injected', async () => {
    const sw = await (await get('/sw.js')).text()
    expect(sw).not.toContain('self.__WB_MANIFEST')
    expect(sw).toContain('"url":"index.html"')
    for (const { path } of icons.slice(0, 3)) {
      expect(sw).toContain(`"url":"${path.slice(1)}"`)
    }
  })

  it('precaches the shell, JS, CSS and every self-hosted font', async () => {
    const entries = precacheEntries(await (await get('/sw.js')).text())
    const urls = entries.map((entry) => entry.url)
    // index.html keeps its URL across builds, so it carries a content
    // revision; the other assets have content hashes in their names.
    expect(entries.find((entry) => entry.url === 'index.html')?.revision).toMatch(/^[0-9a-f]{32}$/)
    expect(urls.some((url) => url.endsWith('.js'))).toBe(true)
    expect(urls.some((url) => url.endsWith('.css'))).toBe(true)
    for (const font of [
      'BagelFatOne-LatinExt',
      'BagelFatOne-Latin',
      'Figtree-Regular',
      'Figtree-SemiBold',
      'Figtree-Bold',
    ]) {
      expect(urls.some((url) => url.startsWith(`assets/${font}`) && url.endsWith('.woff2'))).toBe(true)
    }
  })

  it('links the manifest, theme colour and apple-touch-icon from index.html', async () => {
    const html = await (await get('/')).text()
    expect(html.match(/<link rel="manifest" href="\/manifest\.webmanifest">/g)).toHaveLength(1)
    expect(html).toContain(`<meta name="theme-color" content="${token('--color-accent')}" />`)
    expect(html).toContain('<link rel="apple-touch-icon" href="/apple-touch-icon.png" />')
  })

  it('registers /sw.js at scope / from index.html', async () => {
    const html = await (await get('/')).text()
    expect(html).toContain('<script id="vite-plugin-pwa:register-sw" src="/registerSW.js"></script>')
    const register = await (await get('/registerSW.js')).text()
    expect(register).toContain("navigator.serviceWorker.register('/sw.js', { scope: '/' })")
  })
})

describe('dev server', () => {
  let server: ViteDevServer

  beforeAll(async () => {
    server = await createServer({ logLevel: 'error', server: { middlewareMode: true } })
  })

  afterAll(async () => {
    await server?.close()
  })

  it('does not register a service worker', async () => {
    const html = await server.transformIndexHtml('/', '<html><head></head><body></body></html>')
    expect(html).not.toContain('registerSW')
    expect(html).not.toContain('serviceWorker')
    expect(html).not.toContain('rel="manifest"')
  })
})
