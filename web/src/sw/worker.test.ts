// The service worker itself needs worker globals, so these tests read its
// source: they pin the parts routes.ts cannot express — the API cache's
// strategy, the font strategy's expiration, the font route's place ahead of
// the precache route and network-only requests left to the browser.
// src/sw.test.ts runs the worker's activation against mocked Cache Storage.
import { describe, expect, it } from 'vitest'
import source from '../sw.ts?raw'

describe('service worker source', () => {
  it('serves the API cache with the strategy that marks cache fallbacks', () => {
    expect(source).toContain('[cacheNames.api]: createApiStrategy(),')
  })

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

  it('leaves network-only requests to the browser instead of answering them', () => {
    // Chrome stalls a navigation whose Clear-Site-Data response (POST
    // /auth/logout) the worker answered, so the worker must not match it.
    expect(source).toMatch(
      /function answered\(options: \{ request: Request; url: URL \}\): boolean \{\s*return routeOf\(options\)\.strategy !== 'network-only'/,
    )
    expect(source).toContain('registerRoute(answered, handle, method)')
    expect(source).not.toContain('registerRoute(() => true')
  })

  it('activates a waiting worker when the update prompt sends SKIP_WAITING', () => {
    expect(source).toMatch(
      /self\.addEventListener\('message', \(event\) => \{\s*if \(event\.data\?\.type === 'SKIP_WAITING'\) void self\.skipWaiting\(\)/,
    )
  })
})

describe('service worker push listeners source', () => {
  it('delegates push and notificationclick to src/push/notification.ts', () => {
    expect(source).toContain("import { openNotification, showPush } from './push/notification'")
    expect(source).toMatch(/self\.addEventListener\('push', \(event\) => \{\s*event\.waitUntil\(\s*showPush\(event\.data\?\.text\(\) \?\? null, self\.registration, self\.location\.origin,/)
    expect(source).toMatch(
      /self\.addEventListener\('notificationclick', \(event\) => \{\s*event\.notification\.close\(\)\s*event\.waitUntil\(openNotification\(event\.notification\.data, self\.clients, self\.location\.origin\)\)/,
    )
  })
})
