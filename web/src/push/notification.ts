// Web Push notifications as the service worker shows them, and where a tap on
// one takes the household. The worker's push and notificationclick listeners
// (src/sw.ts) only adapt these to the worker's globals, so every decision runs
// under test without a real ServiceWorkerGlobalScope. This file is checked
// with both the DOM and the WebWorker libraries, so it names only the narrow
// shapes it uses.

// APP_ICON and APP_BADGE are the app icon from the web manifest.
export const APP_ICON = '/icons/icon-192.png'
export const APP_BADGE = '/icons/icon-192.png'

// PushNotice is a notification to show: its title, body and tag, and the
// same-origin path a tap opens.
export type PushNotice = { title: string; body: string; tag?: string; url: string }

// GENERIC_NOTICE is shown for a push whose payload is not a notice: the
// household still learns that MealQuest has something for them.
export const GENERIC_NOTICE: PushNotice = {
  title: 'MealQuest',
  body: "There's something new in MealQuest.",
  url: '/',
}

// ParsedPush is the notice to show for a push, and why its payload was not
// used, when it was not.
export type ParsedPush = { notice: PushNotice; problem: string | null }

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

// appPath returns raw as a path on origin (path, query and fragment), or null
// when raw is not a path-absolute URL that stays on origin. A payload never
// sends the household to another site.
export function appPath(raw: string, origin: string): string | null {
  if (!raw.startsWith('/') || raw.startsWith('//')) return null
  let url: URL
  try {
    url = new URL(raw, origin)
  } catch {
    return null
  }
  if (url.origin !== origin) return null
  return url.pathname + url.search + url.hash
}

// parsePushPayload reads a push message's payload, which may be any bytes:
// a JSON object with a non-empty string title, and optionally a string body,
// a non-empty string tag and a url that is a same-origin path. Other fields
// are ignored. Anything else — no payload, not JSON, a missing title, a
// field of the wrong type, a url off origin — is shown as GENERIC_NOTICE,
// with the problem for the worker to log. It never throws.
export function parsePushPayload(text: string | null, origin: string): ParsedPush {
  const generic = (problem: string): ParsedPush => ({ notice: GENERIC_NOTICE, problem })
  if (text === null || text === '') return generic('push message has no payload')
  let payload: unknown
  try {
    payload = JSON.parse(text)
  } catch {
    return generic('push payload is not JSON')
  }
  if (!isRecord(payload)) return generic('push payload is not a JSON object')
  const { title, body, tag, url } = payload
  if (typeof title !== 'string' || title.trim() === '') return generic('push payload has no title')
  if (body !== undefined && typeof body !== 'string') return generic('push payload body is not a string')
  if (tag !== undefined && (typeof tag !== 'string' || tag === '')) {
    return generic('push payload tag is not a non-empty string')
  }
  let path = '/'
  if (url !== undefined) {
    const safe = typeof url === 'string' ? appPath(url, origin) : null
    if (safe === null) return generic('push payload url is not a same-origin path')
    path = safe
  }
  const notice: PushNotice = { title, body: body ?? '', url: path }
  if (tag !== undefined) notice.tag = tag
  return { notice, problem: null }
}

// NoticeData is the data a shown notification carries for its click.
export type NoticeData = { url: string }

// notificationOptions is what showNotification is given for notice: its body
// and tag, the app icon and badge, and the path to open as its data.
export function notificationOptions(notice: PushNotice): NotificationOptions & { data: NoticeData } {
  const options: NotificationOptions & { data: NoticeData } = {
    body: notice.body,
    icon: APP_ICON,
    badge: APP_BADGE,
    data: { url: notice.url },
  }
  if (notice.tag !== undefined) options.tag = notice.tag
  return options
}

export type NotificationShower = {
  showNotification(title: string, options: NotificationOptions): Promise<void>
}

// showPush shows the notification for a push message's payload text,
// reporting through warn why a payload was not used.
export function showPush(
  text: string | null,
  registration: NotificationShower,
  origin: string,
  warn: (message: string) => void,
): Promise<void> {
  const { notice, problem } = parsePushPayload(text, origin)
  if (problem !== null) warn(`MealQuest: ${problem}; showing the generic notification`)
  return registration.showNotification(notice.title, notificationOptions(notice))
}

// clickPath is the same-origin path a tapped notification opens: the url in
// its data, which showPush put there, or the app's home when the data is not
// ours (a notification shown by an older worker).
export function clickPath(data: unknown, origin: string): string {
  if (isRecord(data) && typeof data.url === 'string') {
    const path = appPath(data.url, origin)
    if (path !== null) return path
  }
  return '/'
}

// AppWindow is the part of a window client a click reads.
export type AppWindow = { url: string; focused: boolean }

// ClickPlan is what a notification tap does: focus an open MealQuest window
// and navigate it to url, or open a new window at url. url is absolute.
export type ClickPlan<W> = { action: 'focus'; window: W; url: string; navigate: boolean } | { action: 'open'; url: string }

// planClick decides a tap on a notification that opens path: the focused
// MealQuest window if there is one, else the first open one, navigated to
// path unless it is already there; with none open, a new window.
export function planClick<W extends AppWindow>(windows: readonly W[], path: string, origin: string): ClickPlan<W> {
  const url = new URL(path, origin).href
  const ours = windows.filter((w) => URL.canParse(w.url) && new URL(w.url).origin === origin)
  const target = ours.find((w) => w.focused) ?? ours[0]
  if (target === undefined) return { action: 'open', url }
  return { action: 'focus', window: target, url, navigate: target.url !== url }
}

export type ClickWindow = AppWindow & {
  focus(): Promise<unknown>
  navigate(url: string): Promise<unknown>
}

export type ClickClients<W extends ClickWindow> = {
  matchAll(options: { type: 'window' }): Promise<readonly W[]>
  openWindow(url: string): Promise<unknown>
}

// openNotification carries out a tap on a notification with data: focus
// first, while the tap still allows it, then navigate.
export async function openNotification<W extends ClickWindow>(
  data: unknown,
  clients: ClickClients<W>,
  origin: string,
): Promise<void> {
  const plan = planClick(await clients.matchAll({ type: 'window' }), clickPath(data, origin), origin)
  if (plan.action === 'open') {
    await clients.openWindow(plan.url)
    return
  }
  await plan.window.focus()
  if (plan.navigate) await plan.window.navigate(plan.url)
}
