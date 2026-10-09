import type { ApiClient } from '../api/client.ts'
import { isRecord } from '../api/errors.ts'

export const VAPID_PUBLIC_KEY_PATH = '/api/v1/push/vapid-public-key'
export const SUBSCRIPTIONS_PATH = '/api/v1/push/subscriptions'

// MAX_USER_AGENT_BYTES is the server's limit on a subscription's user_agent
// (internal/push MaxUserAgentBytes).
export const MAX_USER_AGENT_BYTES = 512

// SubscribePushResult is how turning on Web Push on this device ended:
// subscribed, the browser's permission denied (now or earlier), the
// permission prompt dismissed without an answer, or a browser without Web
// Push. Any other failure rejects with an Error (an ApiError from the API).
export type SubscribePushResult = 'subscribed' | 'denied' | 'dismissed' | 'unsupported'

// UnsubscribePushResult is how turning off Web Push on this device ended:
// its subscription removed from the browser and the server, no subscription
// to remove, or a browser without Web Push. Any other failure rejects.
export type UnsubscribePushResult = 'unsubscribed' | 'not-subscribed' | 'unsupported'

// PushStatus is whether Web Push is on for this device: subscribed with
// permission granted, off (not subscribed, or permission not yet given),
// blocked by the browser's permission, or a browser without Web Push.
export type PushStatus = 'on' | 'off' | 'denied' | 'unsupported'

// The parts of the browser's Push API this module uses.
export type BrowserPushSubscription = {
  readonly endpoint: string
  readonly options: { readonly applicationServerKey: ArrayBuffer | null }
  getKey(name: 'p256dh' | 'auth'): ArrayBuffer | null
  unsubscribe(): Promise<boolean>
}

export type BrowserPushManager = {
  getSubscription(): Promise<BrowserPushSubscription | null>
  subscribe(options: { userVisibleOnly: boolean; applicationServerKey: Uint8Array<ArrayBuffer> }): Promise<BrowserPushSubscription>
}

// PushEnvironment is what the browser offers for Web Push. A field is
// undefined (or pushManager false) where the browser lacks it.
export type PushEnvironment = {
  serviceWorker: { getRegistration(): Promise<{ readonly pushManager: BrowserPushManager } | undefined> } | undefined
  pushManager: boolean
  notification:
    | { readonly permission: NotificationPermission; requestPermission(): Promise<NotificationPermission> }
    | undefined
  // Whether the page is handling a user gesture right now.
  userActivation: { readonly isActive: boolean } | undefined
  userAgent: string
}

// browserPushEnvironment reads the PushEnvironment from this window.
export function browserPushEnvironment(): PushEnvironment {
  return {
    serviceWorker: 'serviceWorker' in navigator ? navigator.serviceWorker : undefined,
    pushManager: 'PushManager' in window,
    notification: 'Notification' in window ? Notification : undefined,
    userActivation: 'userActivation' in navigator ? navigator.userActivation : undefined,
    userAgent: navigator.userAgent,
  }
}

type PushApi = Pick<ApiClient, 'apiFetch'>

// PermissionGestureError is why subscribePush refused to ask for notification
// permission: no tap is active right now ('inactive'), or the browser cannot
// say whether one is ('unobservable', no navigator.userActivation).
export class PermissionGestureError extends Error {
  readonly reason: 'inactive' | 'unobservable'

  constructor(reason: 'inactive' | 'unobservable') {
    super(
      reason === 'inactive'
        ? 'subscribePush: notification permission can only be asked for from a tap on a button'
        : 'subscribePush: this browser cannot report whether a tap is active (no navigator.userActivation), so notification permission is not asked for',
    )
    this.name = 'PermissionGestureError'
    this.reason = reason
  }
}

// encodeBase64url encodes bytes as unpadded base64url, as the server wants
// subscription keys.
export function encodeBase64url(bytes: ArrayBuffer): string {
  let binary = ''
  for (const byte of new Uint8Array(bytes)) binary += String.fromCharCode(byte)
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
}

// decodeVapidKey decodes the server's VAPID public key, unpadded base64url
// of the 65-byte uncompressed P-256 point, into an applicationServerKey.
export function decodeVapidKey(key: string): Uint8Array<ArrayBuffer> {
  const problem = `GET ${VAPID_PUBLIC_KEY_PATH}: public_key is not the unpadded base64url of a 65-byte uncompressed P-256 point`
  if (!/^[A-Za-z0-9_-]+$/.test(key) || key.length % 4 === 1) throw new Error(problem)
  const binary = atob(key.replaceAll('-', '+').replaceAll('_', '/'))
  const bytes = new Uint8Array(new ArrayBuffer(binary.length))
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  if (bytes.length !== 65 || bytes[0] !== 0x04) throw new Error(problem)
  return bytes
}

function sameKey(current: ArrayBuffer | null, key: Uint8Array): boolean {
  if (current === null || current.byteLength !== key.length) return false
  const bytes = new Uint8Array(current)
  return bytes.every((byte, i) => byte === key[i])
}

// SubscriptionBody is the POST /api/v1/push/subscriptions body. The server
// refuses unknown fields and nulls, so it is built field by field rather
// than from PushSubscription.toJSON(), whose expirationTime may be null.
export type SubscriptionBody = { endpoint: string; keys: { p256dh: string; auth: string }; user_agent?: string }

// subscriptionBody is the body that stores subscription. The user agent only
// labels the device, so one that is blank or over the server's limit is left
// out rather than sent to be refused.
export function subscriptionBody(subscription: BrowserPushSubscription, userAgent: string): SubscriptionBody {
  const p256dh = subscription.getKey('p256dh')
  const auth = subscription.getKey('auth')
  if (p256dh === null || auth === null) {
    throw new Error('Web Push: the browser gave a subscription without its p256dh and auth keys')
  }
  const body: SubscriptionBody = {
    endpoint: subscription.endpoint,
    keys: { p256dh: encodeBase64url(p256dh), auth: encodeBase64url(auth) },
  }
  if (userAgent.trim() !== '' && new TextEncoder().encode(userAgent).length <= MAX_USER_AGENT_BYTES) {
    body.user_agent = userAgent
  }
  return body
}

async function fetchVapidKey(api: PushApi): Promise<Uint8Array<ArrayBuffer>> {
  const body = await api.apiFetch<unknown>(VAPID_PUBLIC_KEY_PATH)
  if (!isRecord(body) || typeof body.public_key !== 'string') {
    throw new Error(`GET ${VAPID_PUBLIC_KEY_PATH}: response has no public_key`)
  }
  return decodeVapidKey(body.public_key)
}

// removeSubscription deletes subscription on the server, then in the
// browser. Deleting on the server first means a failure leaves both still
// subscribed, so trying again finds the subscription and finishes the job.
async function removeSubscription(api: PushApi, subscription: BrowserPushSubscription): Promise<void> {
  await api.apiFetch<void>(SUBSCRIPTIONS_PATH, { method: 'DELETE', body: { endpoint: subscription.endpoint } })
  if (!(await subscription.unsubscribe())) {
    throw new Error('Web Push: the browser did not remove its push subscription')
  }
}

function errorName(error: unknown): string | undefined {
  return isRecord(error) && typeof error.name === 'string' ? error.name : undefined
}

// subscribePush turns on Web Push on this device. Call it only from the
// household's tap on a button: when the browser has not yet been asked, it
// asks for notification permission, which browsers allow only during a user
// gesture, and it refuses to ask outside one — or where the browser cannot
// say whether a gesture is active — with a PermissionGestureError. Permission
// is asked before
// anything is awaited so the gesture still counts. With permission, it
// subscribes the browser with the server's VAPID public key — replacing a
// subscription made with an older key — and stores the subscription on the
// server. Subscribing again when already subscribed stores it again.
export async function subscribePush(api: PushApi, env: PushEnvironment): Promise<SubscribePushResult> {
  const { serviceWorker, notification } = env
  if (serviceWorker === undefined || !env.pushManager || notification === undefined) return 'unsupported'

  let permission = notification.permission
  if (permission === 'default') {
    if (env.userActivation === undefined) throw new PermissionGestureError('unobservable')
    if (!env.userActivation.isActive) throw new PermissionGestureError('inactive')
    permission = await notification.requestPermission()
  }
  if (permission === 'denied') return 'denied'
  if (permission === 'default') return 'dismissed'

  const found = await serviceWorker.getRegistration()
  if (found === undefined) {
    throw new Error('Web Push: no service worker is registered for this page, so it cannot receive notifications')
  }
  const { pushManager } = found
  const key = await fetchVapidKey(api)
  let subscription = await pushManager.getSubscription()
  if (subscription !== null && !sameKey(subscription.options.applicationServerKey, key)) {
    await removeSubscription(api, subscription)
    subscription = null
  }
  if (subscription === null) {
    try {
      subscription = await pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
    } catch (error) {
      if (errorName(error) === 'NotAllowedError') return 'denied'
      throw new Error(`Web Push: the browser could not subscribe: ${(error as Error).message}`, { cause: error })
    }
  }
  await api.apiFetch(SUBSCRIPTIONS_PATH, { method: 'POST', body: subscriptionBody(subscription, env.userAgent) })
  return 'subscribed'
}

// unsubscribePush turns off Web Push on this device: it removes the
// browser's subscription on the server and in the browser. With no
// subscription there is nothing to remove, and it answers 'not-subscribed'
// without calling the server.
export async function unsubscribePush(api: PushApi, env: PushEnvironment): Promise<UnsubscribePushResult> {
  const { serviceWorker } = env
  if (serviceWorker === undefined || !env.pushManager) return 'unsupported'
  const found = await serviceWorker.getRegistration()
  if (found === undefined) return 'not-subscribed'
  const subscription = await found.pushManager.getSubscription()
  if (subscription === null) return 'not-subscribed'
  await removeSubscription(api, subscription)
  return 'unsubscribed'
}

// pushStatus reads whether Web Push is on for this device, without asking for
// permission or calling the server. A page with no service worker registered
// is off: subscribePush then says why it cannot turn on.
export async function pushStatus(env: PushEnvironment): Promise<PushStatus> {
  const { serviceWorker, notification } = env
  if (serviceWorker === undefined || !env.pushManager || notification === undefined) return 'unsupported'
  if (notification.permission === 'denied') return 'denied'
  const found = await serviceWorker.getRegistration()
  if (found === undefined) return 'off'
  const subscription = await found.pushManager.getSubscription()
  return subscription !== null && notification.permission === 'granted' ? 'on' : 'off'
}
