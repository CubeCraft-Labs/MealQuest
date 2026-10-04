// Decides how the service worker handles each request. This module is pure —
// no Workbox, no service worker globals — so the tests can assert every URL
// class without a worker; src/sw.ts maps each route to a Workbox strategy.
//
// Security rule: /auth/*, /api/v1/settings/*, /api/v1/jobs/* and every
// non-GET request are network-only and never reach a cache.

export type Route =
  // Network with a 3 s timeout, then the last cached response.
  | { strategy: 'network-first'; cacheName: typeof cacheNames.api }
  // Cached response when there is one, else the network; entries expire.
  | { strategy: 'cache-first'; cacheName: typeof cacheNames.media | typeof cacheNames.fonts }
  // Network only, nothing cached.
  | { strategy: 'network-only' }
  // Network only, nothing cached; offline, the precached index.html.
  | { strategy: 'navigation-fallback' }

export interface RouteRequest {
  method: string
  url: URL
  mode: RequestMode
}

// Bumping the version renames every cache; activate then deletes the old ones.
export const cacheVersion = 'v1'
export const cachePrefix = 'mealquest-'

export const cacheNames = {
  api: `${cachePrefix}api-${cacheVersion}`,
  media: `${cachePrefix}media-${cacheVersion}`,
  fonts: `${cachePrefix}fonts-${cacheVersion}`,
} as const

export const networkTimeoutSeconds = 3

const neverCached = ['/auth', '/api/v1/settings', '/api/v1/jobs']
const networkFirst = ['/api/v1/plans', '/api/v1/shopping', '/api/v1/dinners', '/api/v1/home']

// True when path is prefix itself or a path below it: /api/v1/plans matches
// /api/v1/plans and /api/v1/plans/2026-W40, never /api/v1/plansx.
function under(path: string, prefix: string): boolean {
  return path === prefix || path.startsWith(`${prefix}/`)
}

const networkOnly: Route = { strategy: 'network-only' }

export function routeFor(request: RouteRequest, origin: string): Route {
  const { method, url, mode } = request
  if (method !== 'GET') return networkOnly
  if (url.origin !== origin) return networkOnly
  const path = url.pathname
  if (neverCached.some((prefix) => under(path, prefix))) return networkOnly
  if (networkFirst.some((prefix) => under(path, prefix))) {
    return { strategy: 'network-first', cacheName: cacheNames.api }
  }
  if (under(path, '/media')) return { strategy: 'cache-first', cacheName: cacheNames.media }
  if (path.endsWith('.woff2')) return { strategy: 'cache-first', cacheName: cacheNames.fonts }
  if (under(path, '/api')) return networkOnly
  if (mode === 'navigate') return { strategy: 'navigation-fallback' }
  return networkOnly
}

// The caches activate deletes: MealQuest caches that are no longer current.
// Caches without the mealquest- prefix belong to someone else and are kept.
export function staleCaches(names: readonly string[], current: readonly string[]): string[] {
  return names.filter((name) => name.startsWith(cachePrefix) && !current.includes(name))
}
