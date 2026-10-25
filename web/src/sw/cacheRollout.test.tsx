// F2 (feature review round 3): plan, Home and Supply run responses now carry
// each dinner's status, which their reads require. A device updated from a
// worker whose API cache (v3) holds responses from before must not have them
// served as current: the v4 worker reads only its own cache, and its
// activation deletes the v3 one (sw.test.ts). Offline, such a read fails as
// one never made does — the screen says it could not load and offers Retry,
// with the offline indicator — never as a broken response.
import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { resetArchived } from '../dinners/archived.ts'
import { parseHome } from '../home/api.ts'
import { HomePage } from '../home/HomePage.tsx'
import { parsePlan } from '../plan/api.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { OfflinePill } from '../pwa/OfflinePill.tsx'
import { parseSupplyRun } from '../shopping/api.ts'
import { HOME } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { fakeLocation, jsonResponse } from '../test/fetchStub.ts'
import { answer, FakeCache, origin, stubFetchThroughWorker, stubWorkerCaches } from '../test/serviceWorker.ts'
import { cacheFallbackHeader, cacheNames } from './routes'

const WEEK = '2026-10-05'
const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']

// The plan, Home and Supply run as a server from before dinner status sent
// them, as the v3 worker cached them.
const LEGACY_PLAN = {
  id: 'plan-1',
  week_start: WEEK,
  status: 'synced',
  ai_note: null,
  calendar_synced_at: '2026-10-04T15:00:00Z',
  days: DAYS.map((date, i) => ({
    date,
    dinner: { id: `d${i}`, name: `Dinner ${i}`, photo: null, minutes: 30, method: 'stovetop' },
    is_new: false,
    start_by: null,
  })),
}
const { status: _, ...legacyTonight } = HOME.tonight
const LEGACY_HOME = { ...HOME, tonight: legacyTonight }
const LEGACY_RUN = {
  weekStart: WEEK,
  label: 'Week 41 · Oct 5 – 11',
  title: null,
  status: 'current',
  sent_to_chat_at: null,
  chat_send_unconfirmed_at: null,
  needed_count: 1,
  total_count: 1,
  groups: [{ dinner: { id: 'd1', name: 'Beef tacos' }, date: WEEK, items: [{ id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true }] }],
}
const LEGACY: Record<string, unknown> = {
  [`/api/v1/plans/${WEEK}`]: LEGACY_PLAN,
  '/api/v1/home': LEGACY_HOME,
  [`/api/v1/shopping/${WEEK}`]: LEGACY_RUN,
}

let current: FakeCache
let legacy: FakeCache
let reachable: boolean

beforeEach(() => {
  resetArchived()
  stubWorkerCaches()
  // Cache storage as an updated device has it before the v4 worker
  // activates: the v3 cache full of pre-update responses, the v4 one empty.
  current = new FakeCache()
  legacy = new FakeCache()
  for (const [path, body] of Object.entries(LEGACY)) void legacy.put(new URL(path, origin), jsonResponse(200, body))
  const named = new Map([
    [cacheNames.api, current],
    ['mealquest-api-v3', legacy],
  ])
  const cache = (name: string | undefined) => {
    const found = name === undefined ? undefined : named.get(name)
    if (found === undefined) throw new Error(`unexpected cache ${name}`)
    return found
  }
  vi.stubGlobal('caches', {
    open: async (name: string) => cache(name),
    match: async (request: RequestInfo | URL, options?: MultiCacheQueryOptions) => cache(options?.cacheName).match(request),
  })
  reachable = false
  vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => reachable)
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('a response the v3 worker cached before dinner status', () => {
  it('is one the reads now refuse, so it must not be served', () => {
    expect(() => parsePlan('GET plan', WEEK, LEGACY_PLAN, true)).toThrow('dinner status undefined is not one of')
    expect(() => parseHome('GET home', LEGACY_HOME, true)).toThrow('status undefined is not one of')
    expect(() => parseSupplyRun('GET run', WEEK, LEGACY_RUN, true)).toThrow('dinner status undefined is not one of')
  })

  it('is not served offline by the current worker', async () => {
    expect(cacheNames.api).not.toBe('mealquest-api-v3')
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
    for (const path of Object.keys(LEGACY)) {
      await expect(answer(new Request(new URL(path, origin)))).rejects.toThrow()
    }
    expect(current.entries.size).toBe(0)
  })

  it('is replaced by the server’s answer once read online, which is served offline after', async () => {
    const fresh = { ...LEGACY_HOME, tonight: HOME.tonight }
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(200, fresh)))
    await answer(new Request(new URL('/api/v1/home', origin)))

    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
    const response = await answer(new Request(new URL('/api/v1/home', origin)))
    expect(response.headers.get(cacheFallbackHeader)).toBe('1')
    expect(parseHome('GET home', await response.json(), true).tonight?.status).toBe('regular')
  })

  it('leaves Home, offline, saying it could not load, with Retry and the offline indicator', async () => {
    stubFetchThroughWorker(async (request) => {
      if (!reachable) throw new TypeError('Failed to fetch')
      throw new Error(`unexpected network fetch: ${request.url}`)
    })
    const api = createApiClient(fakeLocation('/'), createNetworkStatus(window), createVerifiedSession(localStorage))
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={createQueryClient()}>
          <InDataRouter path="/">
            <HomePage />
          </InDataRouter>
          <OfflinePill />
        </QueryClientProvider>
      </ApiProvider>,
    )
    // The Rotation check, never cached, says it could not load too.
    const alert = await screen.findByText(/^Couldn't load tonight's quest\./, {}, { timeout: 5000 }).then((text) => text.closest('[role="alert"]'))
    if (!(alert instanceof HTMLElement)) throw new Error("Home's failure is not an alert")
    expect(alert).toHaveTextContent("Couldn't load tonight's quest. Failed to fetch")
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Tonight' })).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Offline')
  })
})
