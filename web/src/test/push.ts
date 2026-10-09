import { vi } from 'vitest'
import { encodeBase64url, type BrowserPushSubscription } from '../push/subscribe.ts'

function bytes(length: number, first: number): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(new ArrayBuffer(length))
  for (let i = 0; i < length; i++) out[i] = (first + i) % 256
  return out
}

const vapidKey = bytes(65, 4)

// VAPID_PUBLIC_KEY is a GET /api/v1/push/vapid-public-key body whose key
// the stubbed subscriptions were made with.
export const VAPID_PUBLIC_KEY = { public_key: encodeBase64url(vapidKey.buffer) }

export const PUSH_ENDPOINT = 'https://push.example/send/abc'

type BrowserPush = {
  // The browser's notification permission before any prompt.
  permission?: NotificationPermission
  // How the permission prompt is answered.
  answer?: NotificationPermission
  // Whether this browser already holds a push subscription.
  subscribed?: boolean
}

// stubPushEnvironment gives jsdom, which has no Web Push, a browser with a
// registered service worker, PushManager, Notification and an active tap.
// Undo with restorePushEnvironment and vi.unstubAllGlobals.
export function stubPushEnvironment({ permission = 'default', answer = 'granted', subscribed = false }: BrowserPush = {}) {
  const subscription: BrowserPushSubscription = {
    endpoint: PUSH_ENDPOINT,
    options: { applicationServerKey: vapidKey.buffer },
    getKey: (name) => (name === 'p256dh' ? bytes(65, 4).buffer : bytes(16, 200).buffer),
    unsubscribe: vi.fn(async () => {
      current = null
      return true
    }),
  }
  let current: BrowserPushSubscription | null = subscribed ? subscription : null
  const pushManager = {
    getSubscription: vi.fn(async () => current),
    subscribe: vi.fn(async () => {
      current = subscription
      return subscription
    }),
  }
  const notification = {
    permission,
    requestPermission: vi.fn(async () => {
      notification.permission = answer
      return answer
    }),
  }
  Object.defineProperty(navigator, 'serviceWorker', {
    configurable: true,
    value: { getRegistration: async () => ({ pushManager }) },
  })
  Object.defineProperty(navigator, 'userActivation', { configurable: true, value: { isActive: true } })
  vi.stubGlobal('PushManager', function PushManager() {})
  vi.stubGlobal('Notification', notification)
  return { notification, pushManager }
}

export function restorePushEnvironment() {
  Reflect.deleteProperty(navigator, 'serviceWorker')
  Reflect.deleteProperty(navigator, 'userActivation')
}
