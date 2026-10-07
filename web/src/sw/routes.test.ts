import { describe, expect, it } from 'vitest'
import { cacheFallbackHeader, cacheNames, cacheVersions, markCacheFallback, routeFor, staleCaches, type Route } from './routes'

const origin = 'https://mealquest.test'

function route(path: string, method = 'GET', mode: RequestMode = 'cors'): Route {
  return routeFor({ method, url: new URL(path, origin), mode }, origin)
}

const apiCache: Route = { strategy: 'network-first', cacheName: cacheNames.api }
const networkOnly: Route = { strategy: 'network-only' }

describe('routeFor', () => {
  it.each([
    '/api/v1/plans',
    '/api/v1/plans/2026-W40',
    '/api/v1/shopping/current',
    '/api/v1/dinners',
    '/api/v1/dinners/01J9ZK?include=recipe',
    '/api/v1/recipes',
    '/api/v1/recipes/01J9ZR',
    '/api/v1/home',
  ])('GET %s is network-first in the API cache', (path) => {
    expect(route(path)).toEqual(apiCache)
  })

  it.each(['/media/photos/abc.jpg', '/media/thumbs/abc.webp'])(
    'GET %s is cache-first in the media cache',
    (path) => {
      expect(route(path)).toEqual({ strategy: 'cache-first', cacheName: cacheNames.media })
    },
  )

  it('GET of a font file is cache-first in the fonts cache', () => {
    expect(route('/assets/Figtree-Bold-a1b2c3.woff2')).toEqual({
      strategy: 'cache-first',
      cacheName: cacheNames.fonts,
    })
  })

  it.each([
    '/auth/login',
    '/auth/callback?code=x&state=y',
    '/auth/logout',
    '/auth/me',
    '/api/v1/settings',
    '/api/v1/settings/ai',
    '/api/v1/jobs',
    '/api/v1/jobs/01J9ZK',
  ])('GET %s is network-only', (path) => {
    expect(route(path)).toEqual(networkOnly)
  })

  it.each(['/auth/login', '/auth/callback?code=x&state=y', '/api/v1/settings/ai'])(
    'a navigation to %s is network-only with no offline fallback',
    (path) => {
      expect(route(path, 'GET', 'navigate')).toEqual(networkOnly)
    },
  )

  it.each([
    '/%61uth/login',
    '/auth%2Fcallback?code=x&state=y',
    '/%61pi/v1/settings/household',
    '/api/v1/%73ettings/ai',
    '/api/v1/settings%2Fai',
    '/%61pi/v1/jobs/01J9ZK',
    '/api/v1/j%6Fbs',
    '/api/v1/plans%2F..%2Fsettings',
    '/api/v1/recipes%2F..%2Fsettings%2Fai',
    '/api/v1/recipes/..%2F..%2F..%2Fauth%2Flogin',
    '/plan%2F..%2Fauth%2Flogin',
  ])('an encoded reserved path %s is network-only, navigated or not', (path) => {
    expect(route(path)).toEqual(networkOnly)
    expect(route(path, 'GET', 'navigate')).toEqual(networkOnly)
  })

  it.each(['/%E0%A4%A', '/auth%', '/plan/%zz', '/api/v1/plans/%C3'])(
    'a malformed encoding in %s is network-only, navigated or not',
    (path) => {
      expect(route(path)).toEqual(networkOnly)
      expect(route(path, 'GET', 'navigate')).toEqual(networkOnly)
    },
  )

  it('classifies other API paths by their decoded form', () => {
    expect(route('/%61pi/v1/plans/2026-W40')).toEqual(apiCache)
    expect(route('/%61pi/v1/recipes/01J9ZR')).toEqual(apiCache)
    expect(route('/%61pi/v1/ideas', 'GET', 'navigate')).toEqual(networkOnly)
  })

  it.each(['POST', 'PUT', 'PATCH', 'DELETE', 'HEAD'])(
    '%s is network-only on every URL class',
    (method) => {
      for (const path of [
        '/api/v1/plans/2026-W40',
        '/api/v1/shopping/current',
        '/api/v1/dinners/01J9ZK',
        '/api/v1/recipes/01J9ZR',
        '/api/v1/home',
        '/media/photos/abc.jpg',
        '/assets/Figtree-Bold-a1b2c3.woff2',
        '/auth/logout',
        '/plan',
      ]) {
        expect(route(path, method), `${method} ${path}`).toEqual(networkOnly)
        expect(route(path, method, 'navigate'), `${method} ${path}`).toEqual(networkOnly)
      }
    },
  )

  it('matches whole path segments only', () => {
    expect(route('/api/v1/plansx')).toEqual(networkOnly)
    expect(route('/api/v1/homepage')).toEqual(networkOnly)
    expect(route('/api/v1/recipesx')).toEqual(networkOnly)
    expect(route('/mediax/a.jpg')).toEqual(networkOnly)
    expect(route('/authors')).toEqual(networkOnly)
    expect(route('/api/v1/settingsx')).toEqual(networkOnly)
  })

  it('leaves other API calls network-only', () => {
    expect(route('/api/v1/me')).toEqual(networkOnly)
    expect(route('/api/v1/ideas', 'GET', 'navigate')).toEqual(networkOnly)
  })

  it('leaves cross-origin requests network-only', () => {
    for (const path of ['/api/v1/plans', '/media/a.jpg', '/assets/a.woff2']) {
      const url = new URL(path, 'https://cdn.example')
      expect(routeFor({ method: 'GET', url, mode: 'no-cors' }, origin)).toEqual(networkOnly)
    }
  })

  it.each(['/', '/plan', '/supply-run', '/dinners/01J9ZK'])(
    'a navigation to the app at %s falls back to the precached index.html',
    (path) => {
      expect(route(path, 'GET', 'navigate')).toEqual({ strategy: 'navigation-fallback' })
    },
  )

  it('leaves other same-origin GETs network-only', () => {
    expect(route('/health')).toEqual(networkOnly)
  })
})

describe('cache names', () => {
  it('all start with mealquest-', () => {
    for (const name of Object.values(cacheNames)) expect(name).toMatch(/^mealquest-/)
  })

  it('retire only the v1 API cache', () => {
    // v1 API responses held dinners with the pasted recipe_url, which can
    // carry a personal token; offline, the worker would still serve them.
    // The precache, photos and fonts keep their v1 caches.
    expect(cacheNames).toEqual({
      api: 'mealquest-api-v2',
      media: 'mealquest-media-v1',
      fonts: 'mealquest-fonts-v1',
    })
    expect(cacheVersions.precache).toBe('v1')
  })
})

describe('staleCaches', () => {
  it('deletes only MealQuest caches that are not current', () => {
    const current = ['mealquest-precache-v1', ...Object.values(cacheNames)]
    const names = [
      ...current,
      'mealquest-precache',
      'mealquest-api-v0',
      'workbox-precache-v2-https://mealquest.test/',
      'other-app-cache',
      'my-mealquest-cache',
    ]
    expect(staleCaches(names, current)).toEqual(['mealquest-precache', 'mealquest-api-v0'])
  })
})

describe('markCacheFallback', () => {
  it('copies the cached response with the cache fallback header set', async () => {
    const cached = new Response('{"id":"01J9ZR"}', {
      status: 200,
      statusText: 'OK',
      headers: { 'Content-Type': 'application/json' },
    })
    const marked = markCacheFallback(cached)
    expect(marked.status).toBe(200)
    expect(marked.statusText).toBe('OK')
    expect(marked.headers.get('Content-Type')).toBe('application/json')
    expect(marked.headers.get(cacheFallbackHeader)).toBe('1')
    expect(await marked.json()).toEqual({ id: '01J9ZR' })
  })
})
