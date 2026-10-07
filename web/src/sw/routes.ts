// Decides how the service worker handles each request. This module is pure —
// no Workbox, no service worker globals — so the tests can assert every URL
// class without a worker; src/sw.ts maps each route to a Workbox strategy.
//
// Security rule: /auth/*, /api/v1/settings/*, /api/v1/jobs/* and every
// non-GET request are network-only and never reach a cache. Paths are matched
// as the server sees them — percent-decoded, before and after dot segments are
// resolved — so /%61uth/login is /auth/login; a malformed encoding is
// network-only.

export type Route =
  // Network with a 3 s timeout, then the last cached response.
  | { strategy: 'network-first'; cacheName: typeof cacheNames.api }
  // Cached response when there is one, else the network; entries expire.
  | { strategy: 'cache-first'; cacheName: typeof cacheNames.media | typeof cacheNames.fonts }
  // Network only, nothing cached: the worker leaves the request to the browser.
  | { strategy: 'network-only' }
  // Network only, nothing cached; offline, the precached index.html.
  | { strategy: 'navigation-fallback' }

export interface RouteRequest {
  method: string
  url: URL
  mode: RequestMode
}

// Bumping the version renames every cache; activate then deletes the old ones.
// v2 drops v1 API responses, whose dinners still carried the pasted
// recipe_url.
export const cacheVersion = 'v2'
export const cachePrefix = 'mealquest-'

export const cacheNames = {
  api: `${cachePrefix}api-${cacheVersion}`,
  media: `${cachePrefix}media-${cacheVersion}`,
  fonts: `${cachePrefix}fonts-${cacheVersion}`,
} as const

export const networkTimeoutSeconds = 3

// cacheFallbackHeader marks a network-first response answered from the cache
// because the server could not be reached in time, so the app can show the
// offline indicator even while the browser reports that it is online.
export const cacheFallbackHeader = 'X-MealQuest-Cache-Fallback'

// markCacheFallback copies a cached response with cacheFallbackHeader set.
export function markCacheFallback(cached: Response): Response {
  const headers = new Headers(cached.headers)
  headers.set(cacheFallbackHeader, '1')
  return new Response(cached.body, { status: cached.status, statusText: cached.statusText, headers })
}

const neverCached = ['/auth', '/api/v1/settings', '/api/v1/jobs']
const networkFirst = ['/api/v1/plans', '/api/v1/shopping', '/api/v1/dinners', '/api/v1/recipes', '/api/v1/home']

// True when path is prefix itself or a path below it: /api/v1/plans matches
// /api/v1/plans and /api/v1/plans/2026-W40, never /api/v1/plansx.
function under(path: string, prefix: string): boolean {
  return path === prefix || path.startsWith(`${prefix}/`)
}

// The pathname percent-decoded, as the server's router sees it, or undefined
// when the encoding is malformed.
function decodePath(pathname: string): string | undefined {
  try {
    return decodeURIComponent(pathname)
  } catch {
    return undefined
  }
}

// Resolves "." and ".." segments the way the server's path.Clean does, so a
// decoded %2F cannot carry a request out of a reserved prefix:
// /api/v1/plans/../settings cleans to /api/v1/settings.
function cleanPath(path: string): string {
  const segments: string[] = []
  for (const segment of path.split('/')) {
    if (segment === '' || segment === '.') continue
    if (segment === '..') segments.pop()
    else segments.push(segment)
  }
  return `/${segments.join('/')}`
}

const networkOnly: Route = { strategy: 'network-only' }

export function routeFor(request: RouteRequest, origin: string): Route {
  const { method, url, mode } = request
  if (method !== 'GET') return networkOnly
  if (url.origin !== origin) return networkOnly
  const path = decodePath(url.pathname)
  if (path === undefined) return networkOnly
  const cleaned = cleanPath(path)
  const reserved = (prefix: string) => under(path, prefix) || under(cleaned, prefix)
  if (neverCached.some(reserved)) return networkOnly
  if (networkFirst.some((prefix) => under(path, prefix))) {
    return { strategy: 'network-first', cacheName: cacheNames.api }
  }
  if (under(path, '/media')) return { strategy: 'cache-first', cacheName: cacheNames.media }
  if (path.endsWith('.woff2')) return { strategy: 'cache-first', cacheName: cacheNames.fonts }
  if (reserved('/api')) return networkOnly
  if (mode === 'navigate') return { strategy: 'navigation-fallback' }
  return networkOnly
}

// The caches activate deletes: MealQuest caches that are no longer current.
// Caches without the mealquest- prefix belong to someone else and are kept.
export function staleCaches(names: readonly string[], current: readonly string[]): string[] {
  return names.filter((name) => name.startsWith(cachePrefix) && !current.includes(name))
}
