// Runs the real Workbox network-first strategy against an in-memory cache
// storage: jsdom has none, and the worker itself needs service worker globals.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { fakeLocation, jsonResponse } from '../test/fetchStub.ts'
import { answer, origin, stubFetchThroughWorker, stubWorkerCaches, type FakeCache } from '../test/serviceWorker.ts'
import { cacheFallbackHeader } from './routes'

let cache: FakeCache

beforeEach(() => {
  cache = stubWorkerCaches()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// handle sends a GET through the API strategy, as src/sw.ts does for a
// network-first route.
async function handle(path: string): Promise<Response> {
  return answer(new Request(new URL(path, origin)))
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

  beforeEach(() => {
    reachable = true
    vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => reachable)
    stubFetchThroughWorker(async (request) => {
      if (!reachable) throw new TypeError('Failed to fetch')
      const body = server[new URL(request.url).pathname]
      if (body === undefined) throw new Error(`unexpected network fetch: ${request.url}`)
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
