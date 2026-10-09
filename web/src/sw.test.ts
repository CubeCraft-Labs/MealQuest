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

// The push listeners only hand src/push/notification.ts the worker's globals;
// jsdom has no ServiceWorkerGlobalScope, so registration and clients are
// stubbed and the events are plain Events with the worker event's fields.
describe('service worker push listeners', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function dispatch(type: string, fields: object): Promise<void> {
    vi.stubGlobal('caches', { keys: vi.fn(async () => []), delete: vi.fn() })
    vi.stubGlobal('__WB_MANIFEST', [])
    await import('./sw')
    const pending: Promise<unknown>[] = []
    self.dispatchEvent(
      Object.assign(new Event(type), { waitUntil: (promise: Promise<unknown>) => pending.push(promise) }, fields),
    )
    expect(pending).toHaveLength(1)
    await Promise.all(pending)
  }

  it('shows a push message as a notification', async () => {
    const registration = { showNotification: vi.fn(async () => {}) }
    vi.stubGlobal('registration', registration)
    const payload = JSON.stringify({ title: 'Plan ready', body: 'Adjust the Draft board.', tag: 'plan_ready', url: '/plan' })
    await dispatch('push', { data: { text: () => payload } })
    expect(registration.showNotification).toHaveBeenCalledWith('Plan ready', {
      body: 'Adjust the Draft board.',
      tag: 'plan_ready',
      icon: '/icons/icon-192.png',
      badge: '/icons/icon-192.png',
      data: { url: '/plan' },
    })
  })

  it('shows the generic notification for a push without a payload', async () => {
    const registration = { showNotification: vi.fn(async () => {}) }
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    vi.stubGlobal('registration', registration)
    await dispatch('push', { data: null })
    expect(registration.showNotification).toHaveBeenCalledWith('MealQuest', expect.objectContaining({ data: { url: '/' } }))
    expect(warn).toHaveBeenCalledWith('MealQuest: push message has no payload; showing the generic notification')
    warn.mockRestore()
  })

  it('closes a tapped notification and opens its url', async () => {
    const clients = { matchAll: vi.fn(async () => []), openWindow: vi.fn(async () => null) }
    vi.stubGlobal('clients', clients)
    const notification = { data: { url: '/discover' }, close: vi.fn() }
    await dispatch('notificationclick', { notification })
    expect(notification.close).toHaveBeenCalled()
    expect(clients.openWindow).toHaveBeenCalledWith(`${self.location.origin}/discover`)
  })
})
