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

  it('deletes the v1 API cache and keeps the precache, photo and font caches', async () => {
    // A household on the v1 worker: its API cache held dinners with the
    // pasted recipe_url; its photos and fonts are not in the precache.
    const names = [
      'mealquest-precache-v1',
      'mealquest-api-v1',
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
    expect(cacheStorage.delete).toHaveBeenCalledTimes(1)
  })
})
