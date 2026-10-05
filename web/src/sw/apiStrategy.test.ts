// Runs the real Workbox network-first strategy against an in-memory cache
// storage: jsdom has none, and the worker itself needs service worker globals.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RouteHandlerCallbackOptions } from 'workbox-core/types'
import { createApiStrategy } from './apiStrategy'
import { cacheFallbackHeader, cacheNames, routeFor } from './routes'

const origin = 'https://mealquest.test'

// FakeCache keys entries by URL, which is all the API cache needs.
class FakeCache {
  readonly entries = new Map<string, Response>()
  async match(request: RequestInfo | URL): Promise<Response | undefined> {
    return this.entries.get(new Request(request).url)?.clone()
  }
  async put(request: RequestInfo | URL, response: Response): Promise<void> {
    this.entries.set(new Request(request).url, response)
  }
}

// FakeExtendableEvent stands in for the worker-only ExtendableEvent,
// collecting the promises the strategy waits on.
class FakeExtendableEvent extends Event {
  readonly pending: Promise<unknown>[] = []
  waitUntil(promise: Promise<unknown>) {
    this.pending.push(promise)
  }
}

let cache: FakeCache

beforeEach(() => {
  cache = new FakeCache()
  // Workbox checks its arguments against these worker-only globals.
  vi.stubGlobal('FetchEvent', class FetchEvent {})
  vi.stubGlobal('ExtendableEvent', FakeExtendableEvent)
  const only = (name: string | undefined) => {
    if (name !== cacheNames.api) throw new Error(`unexpected cache ${name}`)
    return cache
  }
  vi.stubGlobal('caches', {
    open: async (name: string) => only(name),
    match: async (request: RequestInfo | URL, options?: MultiCacheQueryOptions) =>
      only(options?.cacheName).match(request),
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// handle sends a GET through the API strategy, as src/sw.ts does for a
// network-first route.
async function handle(path: string): Promise<Response> {
  const request = new Request(new URL(path, origin))
  expect(routeFor({ method: 'GET', url: new URL(request.url), mode: 'cors' }, origin)).toEqual({
    strategy: 'network-first',
    cacheName: cacheNames.api,
  })
  const event = new FakeExtendableEvent('fetch')
  const response = await createApiStrategy().handle({ request, event: event as unknown as RouteHandlerCallbackOptions['event'] })
  await Promise.all(event.pending)
  return response
}

function recipe(): Response {
  return new Response(JSON.stringify({ id: '01J9ZR', name: 'Chicken tikka masala' }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('API strategy', () => {
  it('answers from the network while the server is reachable, unmarked', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(recipe()))
    const response = await handle('/api/v1/recipes/01J9ZR')
    expect(response.headers.has(cacheFallbackHeader)).toBe(false)
    expect(await response.json()).toMatchObject({ id: '01J9ZR' })
    expect(cache.entries.has(`${origin}/api/v1/recipes/01J9ZR`)).toBe(true)
  })

  it('opens a previously viewed recipe offline, marked as a cache fallback', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(recipe()))
    await handle('/api/v1/recipes/01J9ZR')

    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
    const response = await handle('/api/v1/recipes/01J9ZR')
    expect(response.headers.get(cacheFallbackHeader)).toBe('1')
    expect(await response.json()).toEqual({ id: '01J9ZR', name: 'Chicken tikka masala' })
  })

  it('fails offline for a recipe never viewed', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
    await expect(handle('/api/v1/recipes/01J9ZS')).rejects.toThrow()
  })
})
