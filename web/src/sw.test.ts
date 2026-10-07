// Runs the service worker's activate handler against mocked Cache Storage,
// with the Workbox precache and router stubbed out. tsconfig.sw.json checks
// this file with the worker's types, like src/sw.ts itself.
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('workbox-precaching', () => ({
  PrecacheFallbackPlugin: class {},
  matchPrecache: vi.fn(),
  precacheAndRoute: vi.fn(),
}))
vi.mock('workbox-routing', () => ({ registerRoute: vi.fn() }))

describe('service worker activation', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('deletes the v1 and v2 API caches and keeps the current caches', async () => {
    // A household that ran the v1 and v2 workers: the v1 API cache held
    // dinners with the pasted recipe_url, the v2 one recipe candidates whose
    // candidate_id was the full URL. Its photos and fonts are not in the
    // precache, and the current API cache may already exist.
    const names = [
      'mealquest-precache-v1',
      'mealquest-api-v1',
      'mealquest-api-v2',
      'mealquest-api-v3',
      'mealquest-media-v1',
      'mealquest-fonts-v1',
      'other-app-cache',
    ]
    const cacheStorage = {
      keys: vi.fn(async () => names),
      delete: vi.fn(async () => true),
    }
    vi.stubGlobal('caches', cacheStorage)
    vi.stubGlobal('__WB_MANIFEST', [])
    await import('./sw')

    const pending: Promise<unknown>[] = []
    const activate = Object.assign(new Event('activate'), {
      waitUntil: (promise: Promise<unknown>) => pending.push(promise),
    })
    self.dispatchEvent(activate)
    await Promise.all(pending)

    expect(pending).toHaveLength(1)
    expect(cacheStorage.delete).toHaveBeenCalledWith('mealquest-api-v1')
    expect(cacheStorage.delete).toHaveBeenCalledWith('mealquest-api-v2')
    expect(cacheStorage.delete).toHaveBeenCalledTimes(2)
  })
})
