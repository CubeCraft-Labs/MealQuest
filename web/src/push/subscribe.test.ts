import { describe, expect, it, vi } from 'vitest'
import { ApiError, type ApiRequest } from '../api/index.ts'
import {
  SUBSCRIPTIONS_PATH,
  VAPID_PUBLIC_KEY_PATH,
  decodeVapidKey,
  encodeBase64url,
  PermissionGestureError,
  subscribePush,
  subscriptionBody,
  unsubscribePush,
  type BrowserPushSubscription,
  type PushEnvironment,
} from './subscribe.ts'

function bytes(length: number, first: number): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(new ArrayBuffer(length))
  for (let i = 0; i < length; i++) out[i] = (first + i) % 256
  return out
}

const vapidKey = bytes(65, 4)
const vapidKeyText = encodeBase64url(vapidKey.buffer)
const p256dh = bytes(65, 4).buffer
const auth = bytes(16, 200).buffer
const endpoint = 'https://push.example/send/abc'

function subscription(key: ArrayBuffer | null = vapidKey.buffer, at = endpoint) {
  return {
    endpoint: at,
    options: { applicationServerKey: key },
    getKey: (name: 'p256dh' | 'auth') => (name === 'p256dh' ? p256dh : auth),
    unsubscribe: vi.fn(async () => true),
  } satisfies BrowserPushSubscription
}

type Setup = {
  permission?: NotificationPermission
  answer?: NotificationPermission
  active?: boolean
  current?: BrowserPushSubscription | null
  registered?: boolean
  subscribeError?: unknown
}

function setup({
  permission = 'default',
  answer = 'granted',
  active = true,
  current = null,
  registered = true,
  subscribeError,
}: Setup = {}) {
  const created = subscription()
  const pushManager = {
    getSubscription: vi.fn(async () => current),
    subscribe: vi.fn(async () => {
      if (subscribeError !== undefined) throw subscribeError
      return created
    }),
  }
  const notification = { permission, requestPermission: vi.fn(async () => answer) }
  const env: PushEnvironment = {
    serviceWorker: { getRegistration: vi.fn(async () => (registered ? { pushManager } : undefined)) },
    pushManager: true,
    notification,
    userActivation: { isActive: active },
    userAgent: 'Mozilla/5.0 Test',
  }
  const calls: { path: string; request?: ApiRequest }[] = []
  const api = {
    apiFetch: vi.fn(async (path: string, request?: ApiRequest) => {
      calls.push({ path, request })
      if (path === VAPID_PUBLIC_KEY_PATH) return { public_key: vapidKeyText }
      return undefined
    }) as <T>(path: string, request?: ApiRequest) => Promise<T>,
  }
  return { env, api, calls, pushManager, notification, created }
}

describe('decodeVapidKey', () => {
  it('decodes the unpadded base64url point', () => {
    expect(decodeVapidKey(vapidKeyText)).toEqual(vapidKey)
  })

  it.each(['', 'abc=', 'not base64!', encodeBase64url(bytes(64, 4).buffer), encodeBase64url(bytes(65, 2).buffer)])(
    'refuses %j',
    (key) => {
      expect(() => decodeVapidKey(key)).toThrow(/65-byte uncompressed P-256 point/)
    },
  )
})

describe('subscriptionBody', () => {
  it('sends exactly the endpoint, the keys in unpadded base64url and the user agent', () => {
    const body = subscriptionBody(subscription(), 'Mozilla/5.0 Test')
    expect(body).toEqual({
      endpoint,
      keys: { p256dh: encodeBase64url(p256dh), auth: encodeBase64url(auth) },
      user_agent: 'Mozilla/5.0 Test',
    })
    expect(body.keys.auth).toMatch(/^[A-Za-z0-9_-]{22}$/)
    expect(JSON.stringify(body)).not.toContain('expirationTime')
  })

  it.each(['', '   ', 'x'.repeat(513)])('leaves out a blank or too-long user agent', (userAgent) => {
    expect(subscriptionBody(subscription(), userAgent)).not.toHaveProperty('user_agent')
  })

  it('refuses a subscription without keys', () => {
    const keyless = { ...subscription(), getKey: () => null }
    expect(() => subscriptionBody(keyless, 'ua')).toThrow(/without its p256dh and auth keys/)
  })
})

describe('subscribePush', () => {
  it.each([
    ['serviceWorker', { serviceWorker: undefined }],
    ['PushManager', { pushManager: false }],
    ['Notification', { notification: undefined }],
  ])('answers unsupported without %s, asking nothing', async (_, missing) => {
    const { env, api, notification } = setup()
    expect(await subscribePush(api, { ...env, ...missing })).toBe('unsupported')
    expect(notification.requestPermission).not.toHaveBeenCalled()
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it('asks for permission during a tap, subscribes with the server key and stores the subscription', async () => {
    const { env, api, calls, pushManager, notification } = setup()
    expect(await subscribePush(api, env)).toBe('subscribed')
    expect(notification.requestPermission).toHaveBeenCalledTimes(1)
    expect(pushManager.subscribe).toHaveBeenCalledWith({ userVisibleOnly: true, applicationServerKey: vapidKey })
    expect(calls).toEqual([
      { path: VAPID_PUBLIC_KEY_PATH, request: undefined },
      {
        path: SUBSCRIPTIONS_PATH,
        request: {
          method: 'POST',
          body: {
            endpoint,
            keys: { p256dh: encodeBase64url(p256dh), auth: encodeBase64url(auth) },
            user_agent: 'Mozilla/5.0 Test',
          },
        },
      },
    ])
  })

  it('asks for permission before awaiting anything, so the tap still counts', async () => {
    const { env, api, notification } = setup()
    const pending = subscribePush(api, env)
    expect(notification.requestPermission).toHaveBeenCalledTimes(1)
    expect(api.apiFetch).not.toHaveBeenCalled()
    await pending
  })

  it('refuses to ask for permission outside a tap', async () => {
    const { env, api, notification } = setup({ active: false })
    const error = await subscribePush(api, env).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(PermissionGestureError)
    expect(error).toMatchObject({ reason: 'inactive', message: expect.stringMatching(/only be asked for from a tap/) })
    expect(notification.requestPermission).not.toHaveBeenCalled()
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it('refuses to ask for permission where the browser cannot say whether a tap is active', async () => {
    const { env, api, notification } = setup()
    const error = await subscribePush(api, { ...env, userActivation: undefined }).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(PermissionGestureError)
    expect(error).toMatchObject({ reason: 'unobservable', message: expect.stringMatching(/no navigator.userActivation/) })
    expect(notification.requestPermission).not.toHaveBeenCalled()
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it('does not need to observe a tap once permission is granted', async () => {
    const { env, api, notification } = setup({ permission: 'granted' })
    expect(await subscribePush(api, { ...env, userActivation: undefined })).toBe('subscribed')
    expect(notification.requestPermission).not.toHaveBeenCalled()
  })

  it('does not ask again once permission is granted, even outside a tap', async () => {
    const { env, api, notification } = setup({ permission: 'granted', active: false })
    expect(await subscribePush(api, env)).toBe('subscribed')
    expect(notification.requestPermission).not.toHaveBeenCalled()
  })

  it('reports a permission denied earlier without asking or subscribing', async () => {
    const { env, api, notification, pushManager } = setup({ permission: 'denied' })
    expect(await subscribePush(api, env)).toBe('denied')
    expect(notification.requestPermission).not.toHaveBeenCalled()
    expect(pushManager.subscribe).not.toHaveBeenCalled()
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it('reports a permission denied when asked, without subscribing', async () => {
    const { env, api, pushManager } = setup({ answer: 'denied' })
    expect(await subscribePush(api, env)).toBe('denied')
    expect(pushManager.subscribe).not.toHaveBeenCalled()
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it('reports a dismissed prompt', async () => {
    const { env, api, pushManager } = setup({ answer: 'default' })
    expect(await subscribePush(api, env)).toBe('dismissed')
    expect(pushManager.subscribe).not.toHaveBeenCalled()
  })

  it('reports denied when the browser refuses the subscription for permission', async () => {
    const { env, api, calls } = setup({ subscribeError: new DOMException('Registration failed - permission denied', 'NotAllowedError') })
    expect(await subscribePush(api, env)).toBe('denied')
    expect(calls.map((c) => c.path)).toEqual([VAPID_PUBLIC_KEY_PATH])
  })

  it('rejects with an Error, not a raw DOMException, when the browser cannot subscribe', async () => {
    const cause = new DOMException('push service not available', 'AbortError')
    const { env, api } = setup({ subscribeError: cause })
    const error = await subscribePush(api, env).catch((e: unknown) => e)
    expect(error).not.toBeInstanceOf(DOMException)
    expect(error).toBeInstanceOf(Error)
    expect((error as Error).message).toBe('Web Push: the browser could not subscribe: push service not available')
    expect((error as Error).cause).toBe(cause)
  })

  it('rejects when no service worker is registered', async () => {
    const { env, api } = setup({ registered: false })
    await expect(subscribePush(api, env)).rejects.toThrow(/no service worker is registered/)
  })

  it('stores an existing subscription made with the same key again, without resubscribing', async () => {
    const current = subscription()
    const { env, api, calls, pushManager } = setup({ permission: 'granted', current })
    expect(await subscribePush(api, env)).toBe('subscribed')
    expect(pushManager.subscribe).not.toHaveBeenCalled()
    expect(current.unsubscribe).not.toHaveBeenCalled()
    expect(calls.map((c) => c.request?.method ?? 'GET')).toEqual(['GET', 'POST'])
  })

  it('replaces a subscription made with an older key', async () => {
    const old = subscription(bytes(65, 9).buffer, 'https://push.example/send/old')
    const { env, api, calls, pushManager } = setup({ permission: 'granted', current: old })
    expect(await subscribePush(api, env)).toBe('subscribed')
    expect(old.unsubscribe).toHaveBeenCalled()
    expect(pushManager.subscribe).toHaveBeenCalled()
    expect(calls.map((c) => [c.request?.method ?? 'GET', c.request?.body])).toEqual([
      ['GET', undefined],
      ['DELETE', { endpoint: 'https://push.example/send/old' }],
      ['POST', expect.objectContaining({ endpoint })],
    ])
  })

  it('passes on an API failure storing the subscription', async () => {
    const { env, api } = setup()
    const refused = new ApiError(400, 'invalid_subscription', 'endpoint: want an absolute https:// URL', false)
    vi.mocked(api.apiFetch).mockImplementation(async (path: string) => {
      if (path === VAPID_PUBLIC_KEY_PATH) return { public_key: vapidKeyText } as never
      throw refused
    })
    await expect(subscribePush(api, env)).rejects.toBe(refused)
  })

  it('rejects a VAPID key response without a public_key', async () => {
    const { env, api } = setup()
    vi.mocked(api.apiFetch).mockResolvedValue({} as never)
    await expect(subscribePush(api, env)).rejects.toThrow(`GET ${VAPID_PUBLIC_KEY_PATH}: response has no public_key`)
  })
})

describe('unsubscribePush', () => {
  it('deletes the subscription on the server, then in the browser', async () => {
    const current = subscription()
    const { env, api, calls } = setup({ current })
    expect(await unsubscribePush(api, env)).toBe('unsubscribed')
    expect(calls).toEqual([{ path: SUBSCRIPTIONS_PATH, request: { method: 'DELETE', body: { endpoint } } }])
    expect(current.unsubscribe).toHaveBeenCalled()
  })

  it('answers not-subscribed without calling the server when there is no subscription', async () => {
    const { env, api } = setup({ current: null })
    expect(await unsubscribePush(api, env)).toBe('not-subscribed')
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it('answers not-subscribed when no service worker is registered', async () => {
    const { env, api } = setup({ registered: false })
    expect(await unsubscribePush(api, env)).toBe('not-subscribed')
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it('answers unsupported without Web Push', async () => {
    const { env, api } = setup()
    expect(await unsubscribePush(api, { ...env, pushManager: false })).toBe('unsupported')
  })

  it('keeps the browser subscribed when the server delete fails, so trying again works', async () => {
    const current = subscription()
    const { env, api } = setup({ current })
    vi.mocked(api.apiFetch).mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(unsubscribePush(api, env)).rejects.toThrow('Failed to fetch')
    expect(current.unsubscribe).not.toHaveBeenCalled()
  })

  it('rejects when the browser does not remove the subscription', async () => {
    const current = subscription()
    current.unsubscribe.mockResolvedValue(false)
    const { env, api } = setup({ current })
    await expect(unsubscribePush(api, env)).rejects.toThrow(/did not remove its push subscription/)
  })
})
