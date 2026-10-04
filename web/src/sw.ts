// MealQuest service worker. vite-plugin-pwa replaces self.__WB_MANIFEST with
// the build's precache list (shell, JS, CSS and fonts, each with a revision).
// Every other request goes through the route src/sw/routes.ts decides.

import { cacheNames as workboxCacheNames, setCacheNameDetails } from 'workbox-core'
import type { RouteHandlerCallbackOptions } from 'workbox-core/types'
import { ExpirationPlugin } from 'workbox-expiration'
import { PrecacheFallbackPlugin, precacheAndRoute } from 'workbox-precaching'
import { registerRoute } from 'workbox-routing'
import { CacheFirst, NetworkFirst, NetworkOnly, type Strategy } from 'workbox-strategies'
import {
  cacheNames,
  cacheVersion,
  networkTimeoutSeconds,
  routeFor,
  staleCaches,
  type Route,
} from './sw/routes'

declare const self: ServiceWorkerGlobalScope & {
  __WB_MANIFEST: Array<{ url: string; revision: string | null }>
}

// The precache becomes mealquest-precache-v1.
setCacheNameDetails({ prefix: 'mealquest', precache: 'precache', suffix: cacheVersion })
precacheAndRoute(self.__WB_MANIFEST)

const day = 24 * 60 * 60

const strategies = {
  [cacheNames.api]: new NetworkFirst({ cacheName: cacheNames.api, networkTimeoutSeconds }),
  [cacheNames.media]: new CacheFirst({
    cacheName: cacheNames.media,
    plugins: [
      new ExpirationPlugin({ maxEntries: 200, maxAgeSeconds: 30 * day, purgeOnQuotaError: true }),
    ],
  }),
  [cacheNames.fonts]: new CacheFirst({
    cacheName: cacheNames.fonts,
    plugins: [
      new ExpirationPlugin({ maxEntries: 20, maxAgeSeconds: 365 * day, purgeOnQuotaError: true }),
    ],
  }),
}
const networkOnly = new NetworkOnly()
const navigation = new NetworkOnly({
  plugins: [new PrecacheFallbackPlugin({ fallbackURL: 'index.html' })],
})

function strategyFor(route: Route): Strategy {
  switch (route.strategy) {
    case 'network-first':
    case 'cache-first':
      return strategies[route.cacheName]
    case 'network-only':
      return networkOnly
    case 'navigation-fallback':
      return navigation
  }
}

function handle({ request, url, event }: RouteHandlerCallbackOptions): Promise<Response> {
  const route = routeFor({ method: request.method, url, mode: request.mode }, self.location.origin)
  return strategyFor(route).handle({ request, event })
}

// Workbox routes are per method; every method goes through routeFor, which
// sends every non-GET to the network only.
for (const method of ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'] as const) {
  registerRoute(() => true, handle, method)
}

self.addEventListener('activate', (event) => {
  const current = [workboxCacheNames.precache, ...Object.values(cacheNames)]
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(staleCaches(names, current).map((name) => caches.delete(name)))),
  )
})
