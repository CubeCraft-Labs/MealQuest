import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { jsonResponse } from '../test/fetchStub.ts'
import { origin, stubWorkerCaches, type FakeCache } from '../test/serviceWorker.ts'
import { forgetCachedDinner } from './apiCache'

let cache: FakeCache

beforeEach(() => {
  cache = stubWorkerCaches()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

async function save(path: string, body: unknown) {
  await cache.put(new URL(path, origin), jsonResponse(200, body))
}

describe('forgetCachedDinner', () => {
  it('deletes every saved read that has the dinner and keeps the rest', async () => {
    await save('/api/v1/plans/2026-10-05', { days: [{ dinner: { id: 'd1', status: 'regular' } }, { dinner: { id: 'd4' } }] })
    await save('/api/v1/home', { tonight: { id: 'd1', status: 'regular' } })
    await save('/api/v1/shopping/2026-10-05', { groups: [{ dinner: { id: 'd1', status: 'regular' } }] })
    await save('/api/v1/dinners?status=regular', { dinners: [{ id: 'd1' }, { id: 'd4' }] })
    await save('/api/v1/dinners/d1', { id: 'd1', status: 'regular' })
    // Another week, another dinner and one whose id only starts with d1.
    await save('/api/v1/plans/2026-10-12', { days: [{ dinner: { id: 'd4', status: 'regular' } }] })
    await save('/api/v1/dinners/d4', { id: 'd4', status: 'regular' })
    await save('/api/v1/dinners/d10', { id: 'd10', status: 'regular' })

    await forgetCachedDinner('d1')

    expect([...cache.entries.keys()].map((url) => url.slice(origin.length)).sort()).toEqual([
      '/api/v1/dinners/d10',
      '/api/v1/dinners/d4',
      '/api/v1/plans/2026-10-12',
    ])
    expect(await (await cache.match(new URL('/api/v1/plans/2026-10-12', origin)))?.json()).toEqual({
      days: [{ dinner: { id: 'd4', status: 'regular' } }],
    })
  })

  it('has nothing to forget where there is no Cache Storage, so no service worker', async () => {
    vi.stubGlobal('caches', undefined)
    await expect(forgetCachedDinner('d1')).resolves.toBeUndefined()
  })
})
