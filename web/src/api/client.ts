import type { NetworkStatus } from '../pwa/networkStatus.ts'
import { cacheFallbackHeader } from '../sw/routes.ts'
import { ApiError, isRecord, parseApiError } from './errors.ts'
import type { VerifiedSession } from './verifiedSession.ts'

// CSRF_HEADER carries the session's CSRF token on every non-GET request.
export const CSRF_HEADER = 'X-MealQuest-CSRF'

export const ME_PATH = '/api/v1/me'

// API_PREFIX is the only path apiFetch sends to.
const API_PREFIX = '/api/v1/'

// apiURL resolves path against the page origin and returns it as the
// path-absolute URL to fetch. It refuses any path that would not resolve to a
// same-origin URL under /api/v1/, so the CSRF token is never sent anywhere
// else: absolute and protocol-relative URLs on another origin, and dot
// segments that climb out of /api/v1/.
function apiURL(origin: string, method: string, path: string): string {
  const url = new URL(path, origin)
  if (!path.startsWith('/') || url.origin !== origin || !url.pathname.startsWith(API_PREFIX)) {
    throw new Error(`${method} ${path}: not a same-origin ${API_PREFIX} path`)
  }
  return url.pathname + url.search
}

// Me is the GET /api/v1/me response: the signed-in member, the household and
// the session's CSRF token.
export type Me = {
  name: string
  email: string
  household: { label: string }
  csrfToken: string
}

// ApiLocation is the part of window.location the client reads and navigates.
// origin is the page origin every request must stay on.
export type ApiLocation = Pick<Location, 'origin' | 'pathname' | 'search' | 'assign'>

export type ApiRequest = {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  // Sent as JSON. A GET must not have one.
  body?: unknown
  // Aborts the request; fetch then rejects with the signal's reason. No
  // request has a deadline unless its caller aborts it.
  signal?: AbortSignal
}

// ApiRead is a GET's decoded response body, and whether the service worker
// answered it from its cache because the server could not be reached in time.
export type ApiRead<T> = { body: T; cached: boolean }

export type ApiClient = {
  // apiFetch sends a same-origin JSON request to path, which must be under
  // /api/v1/, and resolves with the decoded response body, or rejects with an
  // ApiError.
  apiFetch<T>(path: string, request?: ApiRequest): Promise<T>
  // apiRead is apiFetch's GET that also says whether the body is the service
  // worker's cached copy rather than the server's answer.
  apiRead<T>(path: string): Promise<ApiRead<T>>
  // loadMe fetches GET /api/v1/me and caches its CSRF token.
  loadMe(): Promise<Me>
  // forbidden reports whether a request was refused because the signed-in
  // account is not in this household; subscribe is notified when it changes.
  forbidden(): boolean
  subscribe(listener: () => void): () => void
  // network learns of every request that failed with a network error or was
  // answered from the service worker's cache, and of every request the
  // server answered.
  network: NetworkStatus
  // session remembers, on this device, whether /me last answered with a
  // member; offline, AuthBootstrap opens the saved pages only if it did.
  session: VerifiedSession
}

// createApiClient builds the app's API client. location is window.location in
// the app: a 401 sends it to /auth/login with the current path to return to.
export function createApiClient(location: ApiLocation, network: NetworkStatus, session: VerifiedSession): ApiClient {
  let csrfToken: string | null = null
  let refreshing: Promise<Me> | null = null
  let forbidden = false
  const listeners = new Set<() => void>()

  async function send(method: string, path: string, body: unknown, signal: AbortSignal | undefined): Promise<Response> {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    if (method !== 'GET') {
      if (csrfToken === null) {
        throw new Error(`${method} ${path}: no CSRF token; ${ME_PATH} has not loaded`)
      }
      headers[CSRF_HEADER] = csrfToken
    }
    // Serialize before fetching so a body that cannot be sent is its own error
    // and never counts as a network failure.
    let json: string | undefined
    try {
      json = body === undefined ? undefined : JSON.stringify(body)
    } catch (error) {
      throw new Error(`${method} ${path}: request body is not serializable as JSON`, { cause: error })
    }
    let response: Response
    try {
      response = await fetch(path, { method, headers, credentials: 'same-origin', body: json, signal })
    } catch (error) {
      // fetch rejects with a TypeError when the network fails; an abort is a
      // DOMException and says nothing about the network.
      if (error instanceof TypeError) network.requestFailed()
      throw error
    }
    // The service worker marks a response it answered from its cache because
    // the server could not be reached in time.
    if (response.headers.has(cacheFallbackHeader)) network.requestFailed()
    else network.requestSucceeded()
    return response
  }

  // refreshMe shares one in-flight /me request between every request that
  // got a csrf 403 at the same time.
  function refreshMe(): Promise<Me> {
    refreshing ??= loadMe().finally(() => {
      refreshing = null
    })
    return refreshing
  }

  async function apiFetch<T>(path: string, request: ApiRequest = {}): Promise<T> {
    return (await exchange<T>(path, request)).body
  }

  function apiRead<T>(path: string): Promise<ApiRead<T>> {
    return exchange<T>(path, {})
  }

  // exchange sends request to path and decodes the response, noting whether
  // it is the service worker's cached copy.
  async function exchange<T>(path: string, request: ApiRequest): Promise<ApiRead<T>> {
    const method = request.method ?? 'GET'
    const url = apiURL(location.origin, method, path)
    // fetch refuses a GET with a body by rejecting with a TypeError, the same
    // error as a network failure, so refuse it here before anything is sent.
    if (method === 'GET' && request.body !== undefined) {
      throw new Error(`GET ${path}: a GET request cannot carry a body`)
    }
    let response = await send(method, url, request.body, request.signal)
    if (response.status === 403) {
      const error = await parseForbidden(method, path, response)
      // Only a non-GET carries the token, so only it can be refused for it.
      if (error.code !== 'csrf' || method === 'GET') throw error
      // The session's token changed: refresh it once and retry once.
      await refreshMe()
      response = await send(method, url, request.body, request.signal)
      if (response.status === 403) throw await parseForbidden(method, path, response)
    }
    return { body: await decode<T>(method, path, response), cached: response.headers.has(cacheFallbackHeader) }
  }

  // parseForbidden reads a 403. Any API error other than csrf means the
  // account is not in this household.
  async function parseForbidden(method: string, path: string, response: Response): Promise<ApiError> {
    const error = await parseApiError(method, path, response)
    if (error.code !== 'csrf') markForbidden()
    return error
  }

  function markForbidden() {
    if (forbidden) return
    session.forget()
    forbidden = true
    listeners.forEach((listener) => listener())
  }

  async function decode<T>(method: string, path: string, response: Response): Promise<T> {
    if (response.status === 401) {
      session.forget()
      location.assign(`/auth/login?return=${encodeURIComponent(location.pathname + location.search)}`)
    }
    if (!response.ok) throw await parseApiError(method, path, response)
    if (response.status === 204) return undefined as T
    return (await response.json()) as T
  }

  async function loadMe(): Promise<Me> {
    const me = await apiFetch<unknown>(ME_PATH)
    if (
      !isRecord(me) ||
      typeof me.name !== 'string' ||
      typeof me.email !== 'string' ||
      !isRecord(me.household) ||
      typeof me.household.label !== 'string' ||
      typeof me.csrfToken !== 'string' ||
      me.csrfToken === ''
    ) {
      throw new Error(`GET ${ME_PATH}: response is not a member with a CSRF token`)
    }
    csrfToken = me.csrfToken
    session.remember()
    return me as Me
  }

  return {
    apiFetch,
    apiRead,
    loadMe,
    forbidden: () => forbidden,
    subscribe(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    network,
    session,
  }
}
