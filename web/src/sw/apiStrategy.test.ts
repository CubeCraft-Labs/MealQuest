// Runs the real Workbox network-first strategy against an in-memory cache
// storage: jsdom has none, and the worker itself needs service worker globals.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RouteHandlerCallbackOptions } from 'workbox-core/types'
import { createApiClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { fakeLocation, jsonResponse } from '../test/fetchStub.ts'
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
  return answer(new Request(new URL(path, origin)))
}

async function answer(request: Request): Promise<Response> {
  expect(routeFor({ method: request.method, url: new URL(request.url), mode: 'cors' }, origin)).toEqual({
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

describe('previously viewed plan and recipe reads', () => {
  const plan = { week: '2026-W40', days: [{ date: '2026-10-05', dinner: 'Chicken tikka masala' }] }
  const recipe = { id: '01J9ZR', name: 'Chicken tikka masala', steps: ['Marinate', 'Simmer'] }
  const server: Record<string, unknown> = { '/api/v1/plans/2026-W40': plan, '/api/v1/recipes/01J9ZR': recipe }
  let reachable: boolean

  // The page's fetch goes through the worker's API strategy; the strategy's
  // own fetch (always a Request) is the network. The API client fetches a
  // path string, so the two are told apart by the argument's type. As in a
  // browser, a worker that cannot answer is a network error to the page.
  beforeEach(() => {
    reachable = true
    vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => reachable)
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      if (!(input instanceof Request)) {
        return answer(new Request(new URL(String(input), origin), init)).catch((error: unknown) => {
          throw new TypeError('Failed to fetch', { cause: error })
        })
      }
      if (!reachable) throw new TypeError('Failed to fetch')
      const body = server[new URL(input.url).pathname]
      if (body === undefined) throw new Error(`unexpected network fetch: ${input.url}`)
      return jsonResponse(200, body)
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  function client() {
    return createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
  }

  it('serves the plan and recipe fetched online once the network is gone', async () => {
    const online = client()
    expect(await online.apiFetch('/api/v1/plans/2026-W40')).toEqual(plan)
    expect(await online.apiFetch('/api/v1/recipes/01J9ZR')).toEqual(recipe)
    expect(online.network.offline()).toBe(false)

    reachable = false
    const offline = client()
    expect(await offline.apiFetch('/api/v1/plans/2026-W40')).toEqual(plan)
    expect(await offline.apiFetch('/api/v1/recipes/01J9ZR')).toEqual(recipe)
    expect(offline.network.offline()).toBe(true)
  })

  it('shows the offline indicator when the cached copy answers while the browser reports online', async () => {
    await client().apiFetch('/api/v1/plans/2026-W40')

    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(true)
    reachable = false
    const api = client()
    expect(await api.apiFetch('/api/v1/plans/2026-W40')).toEqual(plan)
    expect(api.network.offline()).toBe(true)
  })

  it('fails offline for a plan never fetched', async () => {
    reachable = false
    await expect(client().apiFetch('/api/v1/plans/2026-W41')).rejects.toThrow(TypeError)
  })
})
