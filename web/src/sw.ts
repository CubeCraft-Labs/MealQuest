// MealQuest service worker. vite-plugin-pwa replaces self.__WB_MANIFEST with
// the build's precache list (shell, JS, CSS and fonts, each with a revision).
// Every other request goes through the route src/sw/routes.ts decides.
// A network-only request is left to the browser: the worker never answers it,
// so it reaches the network exactly as the page sent it. That matters for
// POST /auth/logout, whose Clear-Site-Data header wipes the worker and its
// caches — Chrome stalls that navigation when the worker answers it.
// Fonts are precached too, but their route is registered ahead of the
// precache route, so a font fetch goes through the expiring CacheFirst
// strategy and falls back to the precached copy when the network fails.

import { cacheNames as workboxCacheNames, setCacheNameDetails } from 'workbox-core'
import type { RouteHandlerCallbackOptions } from 'workbox-core/types'
import { ExpirationPlugin } from 'workbox-expiration'
import { PrecacheFallbackPlugin, matchPrecache, precacheAndRoute } from 'workbox-precaching'
import { registerRoute } from 'workbox-routing'
import { CacheFirst, NetworkOnly, type Strategy } from 'workbox-strategies'
import { openNotification, showPush } from './push/notification'
import { createApiStrategy } from './sw/apiStrategy'
import {
  cacheNames,
  cacheVersions,
  routeFor,
  staleCaches,
  type Route,
} from './sw/routes'

declare const self: ServiceWorkerGlobalScope & {
  __WB_MANIFEST: Array<{ url: string; revision: string | null }>
}

// The precache becomes mealquest-precache-v1.
setCacheNameDetails({ prefix: 'mealquest', precache: 'precache', suffix: cacheVersions.precache })

const day = 24 * 60 * 60

const strategies = {
  [cacheNames.api]: createApiStrategy(),
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
      // Offline with no runtime copy yet: the font from the precache.
      { handlerDidError: async ({ request }) => matchPrecache(request.url) },
    ],
  }),
}
const navigation = new NetworkOnly({
  plugins: [new PrecacheFallbackPlugin({ fallbackURL: 'index.html' })],
})

type AnsweredRoute = Exclude<Route, { strategy: 'network-only' }>

function strategyFor(route: AnsweredRoute): Strategy {
  switch (route.strategy) {
    case 'network-first':
    case 'cache-first':
      return strategies[route.cacheName]
    case 'navigation-fallback':
      return navigation
  }
}

function routeOf({ request, url }: { request: Request; url: URL }): Route {
  return routeFor({ method: request.method, url, mode: request.mode }, self.location.origin)
}

// answered matches every request the worker answers: all but network-only.
function answered(options: { request: Request; url: URL }): boolean {
  return routeOf(options).strategy !== 'network-only'
}

function handle({ request, url, event }: RouteHandlerCallbackOptions): Promise<Response> {
  const route = routeOf({ request, url })
  if (route.strategy === 'network-only') {
    throw new Error(`the worker never answers network-only ${request.method} ${url.href}`)
  }
  return strategyFor(route).handle({ request, event })
}

function isFont(options: { request: Request; url: URL }): boolean {
  const route = routeOf(options)
  return route.strategy === 'cache-first' && route.cacheName === cacheNames.fonts
}

// Workbox uses the first route that matches, so the font route comes before
// the precache route, which would otherwise answer every precached font.
registerRoute(isFont, handle, 'GET')
precacheAndRoute(self.__WB_MANIFEST)

// Workbox routes are per method; every method goes through routeFor, which
// sends every non-GET to the network only, so only GETs are ever answered.
for (const method of ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'] as const) {
  registerRoute(answered, handle, method)
}

self.addEventListener('activate', (event) => {
  const current = [workboxCacheNames.precache, ...Object.values(cacheNames)]
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(staleCaches(names, current).map((name) => caches.delete(name)))),
  )
})

// The update prompt (src/pwa/UpdatePrompt.tsx) asks a waiting worker to take
// over when the household chooses Reload.
self.addEventListener('message', (event) => {
  if (event.data?.type === 'SKIP_WAITING') void self.skipWaiting()
})

// Web Push: src/push/notification.ts decides what to show and where a tap
// goes; these listeners only hand it the worker's globals. A payload that is
// not a MealQuest notice still shows a generic notification, and the reason
// is logged.
self.addEventListener('push', (event) => {
  event.waitUntil(
    showPush(event.data?.text() ?? null, self.registration, self.location.origin, (message) => console.warn(message)),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  event.waitUntil(openNotification(event.notification.data, self.clients, self.location.origin))
})
