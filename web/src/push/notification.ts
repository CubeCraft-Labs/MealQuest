// Web Push notifications as the service worker shows them, and where a tap on
// one takes the household. The worker's push and notificationclick listeners
// (src/sw.ts) only adapt these to the worker's globals, so every decision runs
// under test without a real ServiceWorkerGlobalScope. This file is checked
// with both the DOM and the WebWorker libraries, so it names only the narrow
// shapes it uses.

// APP_ICON and APP_BADGE are the app icon from the web manifest.
export const APP_ICON = '/icons/icon-192.png'
export const APP_BADGE = '/icons/icon-192.png'

// NoticeDinner is the dinner whose recipe a notification offers.
export type NoticeDinner = { id: string; name: string }

// PushNotice is a notification to show: its title, body and tag, the
// same-origin path a tap opens, and the dinner whose recipe that path is,
// when it is one.
export type PushNotice = { title: string; body: string; tag?: string; url: string; dinner?: NoticeDinner }

// GENERIC_NOTICE is shown for a push whose payload is not a notice: the
// household still learns that MealQuest has something for them.
export const GENERIC_NOTICE: PushNotice = {
  title: 'MealQuest',
  body: "There's something new in MealQuest.",
  url: '/',
}

// recipePathId is the id of the dinner whose recipe the same-origin path
// opens (/dinner/{id}), or null when it opens none.
export function recipePathId(path: string): string | null {
  const pathname = new URL(path, 'https://mealquest.invalid').pathname
  const encoded = /^\/dinner\/([^/]+)$/.exec(pathname)?.[1]
  if (encoded === undefined) return null
  try {
    return decodeURIComponent(encoded)
  } catch {
    return null
  }
}

// opensRecipeOf reports whether path opens the recipe of dinner and nothing
// else.
export function opensRecipeOf(path: string, dinner: NoticeDinner): boolean {
  return path === `/dinner/${encodeURIComponent(dinner.id)}`
}

// parseNoticeDinner reads the dinner of a notification or update: an object
// with a non-empty string id and name, or null when it is not one.
export function parseNoticeDinner(value: unknown): NoticeDinner | null {
  if (!isRecord(value)) return null
  const { id, name } = value
  if (typeof id !== 'string' || id === '' || typeof name !== 'string' || name.trim() === '') return null
  return { id, name }
}

// RECIPE_PARAM and RECIPE_NAME_PARAM are the Home search params of a
// notification's recipe (recipeOpenPath): Home checks the dinner with the
// server before it opens its recipe, as every recipe button does.
export const RECIPE_PARAM = 'recipe'
export const RECIPE_NAME_PARAM = 'name'

// recipeOpenPath is the path a tap on a notification offering dinner's
// recipe opens: Home, asked to open that recipe once the server has it.
export function recipeOpenPath(dinner: NoticeDinner): string {
  return `/?${new URLSearchParams({ [RECIPE_PARAM]: dinner.id, [RECIPE_NAME_PARAM]: dinner.name }).toString()}`
}

// ParsedPush is the notice to show for a push, and why its payload was not
// used, when it was not.
export type ParsedPush = { notice: PushNotice; problem: string | null }

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

// appPath returns raw as a path on origin (path, query and fragment), or null
// when raw is not a path-absolute URL that stays on origin. A payload never
// sends the household to another site. The path is checked after the parser
// has resolved it: dot segments (also as %2e), backslashes and stripped tabs
// and newlines can all turn an innocent-looking path into one starting with
// //, which a browser would read as another host.
export function appPath(raw: string, origin: string): string | null {
  if (!raw.startsWith('/') || raw.startsWith('//')) return null
  let url: URL
  try {
    url = new URL(raw, origin)
  } catch {
    return null
  }
  if (url.origin !== origin || url.pathname.startsWith('//')) return null
  return url.pathname + url.search + url.hash
}

// parsePushPayload reads a push message's payload, which may be any bytes:
// a JSON object with a non-empty string title, and optionally a string body,
// a non-empty string tag, a url that is a same-origin path and the dinner
// (id and name) whose recipe that url is. Other fields are ignored. A url
// opening a recipe with no dinner was sent before notifications named their
// dinner: its notice offers no recipe (clickPath). Anything else — no payload, not JSON, a missing title, a
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
  const { title, body, tag, url, dinner } = payload
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
  if (dinner !== undefined) {
    const named = parseNoticeDinner(dinner)
    if (named === null) return generic('push payload dinner is not a dinner with an id and a name')
    if (!opensRecipeOf(path, named)) return generic("push payload url is not its dinner's recipe")
    notice.dinner = named
  }
  return { notice, problem: null }
}

// NoticeData is the data a shown notification carries for its click.
export type NoticeData = { url: string; dinner?: NoticeDinner }

// notificationOptions is what showNotification is given for notice: its body
// and tag, the app icon and badge, and the path to open, with the dinner
// whose recipe it is, as its data.
export function notificationOptions(notice: PushNotice): NotificationOptions & { data: NoticeData } {
  const options: NotificationOptions & { data: NoticeData } = {
    body: notice.body,
    icon: APP_ICON,
    badge: APP_BADGE,
    data: { url: notice.url },
  }
  if (notice.dinner !== undefined) options.data.dinner = notice.dinner
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
// ours (a notification shown by an older worker). A url opening a dinner's
// recipe never opens it unchecked: with the dinner it names, the tap opens
// Home asked to open that recipe (recipeOpenPath), which it does only once
// the server has it, and says so in place when the dinner was removed; a
// recipe url naming no dinner (a notification sent before notifications
// named their dinner) opens Home, whose Tonight card checks its recipe.
export function clickPath(data: unknown, origin: string): string {
  if (!isRecord(data) || typeof data.url !== 'string') return '/'
  const path = appPath(data.url, origin)
  if (path === null) return '/'
  if (recipePathId(path) === null) return path
  const dinner = parseNoticeDinner(data.dinner)
  return dinner !== null && opensRecipeOf(path, dinner) ? recipeOpenPath(dinner) : '/'
}

// AppWindow is the part of a window client a click reads.
export type AppWindow = { url: string; focused: boolean }

// ClickPlan is what a notification tap does: focus an open MealQuest window
// and navigate it to url, or open a new window at url. url is absolute.
export type ClickPlan<W> = { action: 'focus'; window: W; url: string; navigate: boolean } | { action: 'open'; url: string }

// planClick decides a tap on a notification that opens path: the focused
// MealQuest window if there is one, else the first open one, navigated to
// path unless it is already there; with none open, a new window. A path that
// resolves off origin is an error: a tap never leaves MealQuest.
export function planClick<W extends AppWindow>(windows: readonly W[], path: string, origin: string): ClickPlan<W> {
  const resolved = new URL(path, origin)
  if (resolved.origin !== origin) {
    throw new Error(`MealQuest: notification path ${JSON.stringify(path)} resolves off origin to ${resolved.origin}`)
  }
  const url = resolved.href
  const ours = windows.filter((w) => URL.canParse(w.url) && new URL(w.url).origin === origin)
  const target = ours.find((w) => w.focused) ?? ours[0]
  if (target === undefined) return { action: 'open', url }
  return { action: 'focus', window: target, url, navigate: target.url !== url }
}

export type ClickWindow = AppWindow & {
  focus(): Promise<unknown>
  navigate(url: string): Promise<unknown>
  postMessage(message: NotificationOpened): void
}

// NotificationOpened is the message a tap posts to an open window it focuses
// without navigating, because the window is already at url: nothing remounts
// there, so the screen reads afresh what the notification is about.
export type NotificationOpened = { type: 'NOTIFICATION_OPENED'; url: string }

// isNotificationOpened reports whether a message from the service worker is
// NotificationOpened.
export function isNotificationOpened(message: unknown): message is NotificationOpened {
  return isRecord(message) && message.type === 'NOTIFICATION_OPENED' && typeof message.url === 'string'
}

export type ClickClients<W extends ClickWindow> = {
  matchAll(options: { type: 'window' }): Promise<readonly W[]>
  openWindow(url: string): Promise<unknown>
}

// openNotification carries out a tap on a notification with data: focus
// first, while the tap still allows it, then navigate; a window already at
// the url is told NotificationOpened instead.
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
  else plan.window.postMessage({ type: 'NOTIFICATION_OPENED', url: plan.url })
}
