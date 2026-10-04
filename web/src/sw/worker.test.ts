// The service worker itself needs worker globals, so these tests read its
// source: they pin the parts routes.ts cannot express — the font strategy's
// expiration and the font route's place ahead of the precache route.
import { describe, expect, it } from 'vitest'
import source from '../sw.ts?raw'

describe('service worker source', () => {
  it('gives the fonts cache a CacheFirst strategy with an expiration plugin', () => {
    expect(source).toMatch(
      /\[cacheNames\.fonts\]: new CacheFirst\(\{\s*cacheName: cacheNames\.fonts,\s*plugins: \[\s*new ExpirationPlugin\(\{ maxEntries: \d+, maxAgeSeconds: [^}]+\}\)/,
    )
  })

  it('falls back to the precached font when the network fails', () => {
    expect(source).toContain('handlerDidError: async ({ request }) => matchPrecache(request.url)')
  })

  it('registers the font route before the precache route', () => {
    const fontRoute = source.indexOf("registerRoute(isFont, handle, 'GET')")
    const precache = source.indexOf('precacheAndRoute(self.__WB_MANIFEST)')
    expect(fontRoute).toBeGreaterThanOrEqual(0)
    expect(precache).toBeGreaterThan(fontRoute)
  })
})
