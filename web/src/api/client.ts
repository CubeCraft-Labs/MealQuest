import { ApiError, isRecord, parseApiError } from './errors.ts'

// CSRF_HEADER carries the session's CSRF token on every non-GET request.
export const CSRF_HEADER = 'X-MealQuest-CSRF'

export const ME_PATH = '/api/v1/me'

// API_PREFIX is the only path apiFetch sends to.
const API_PREFIX = '/api/v1/'

// requireApiPath refuses any path that would not resolve to a same-origin URL
// under /api/v1/, so the CSRF token is never sent anywhere else. Resolving
// against a placeholder origin catches absolute and protocol-relative URLs
// and dot segments that climb out of /api/v1/.
function requireApiPath(method: string, path: string) {
  const base = 'https://same-origin.invalid'
  const url = new URL(path, base)
  if (!path.startsWith('/') || url.origin !== base || !url.pathname.startsWith(API_PREFIX)) {
    throw new Error(`${method} ${path}: not a same-origin ${API_PREFIX} path`)
  }
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
export type ApiLocation = Pick<Location, 'pathname' | 'search' | 'assign'>

export type ApiRequest = {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  // Sent as JSON.
  body?: unknown
}

export type ApiClient = {
  // apiFetch sends a same-origin JSON request to path, which must be under
  // /api/v1/, and resolves with the decoded response body, or rejects with an
  // ApiError.
  apiFetch<T>(path: string, request?: ApiRequest): Promise<T>
  // loadMe fetches GET /api/v1/me and caches its CSRF token.
  loadMe(): Promise<Me>
  // forbidden reports whether a request was refused because the signed-in
  // account is not in this household; subscribe is notified when it changes.
  forbidden(): boolean
  subscribe(listener: () => void): () => void
}

// createApiClient builds the app's API client. location is window.location in
// the app: a 401 sends it to /auth/login with the current path to return to.
export function createApiClient(location: ApiLocation): ApiClient {
  let csrfToken: string | null = null
  let refreshing: Promise<Me> | null = null
  let forbidden = false
  const listeners = new Set<() => void>()

  async function send(method: string, path: string, body: unknown): Promise<Response> {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    if (method !== 'GET') {
      if (csrfToken === null) {
        throw new Error(`${method} ${path}: no CSRF token; ${ME_PATH} has not loaded`)
      }
      headers[CSRF_HEADER] = csrfToken
    }
    return fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: body === undefined ? undefined : JSON.stringify(body),
    })
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
    const method = request.method ?? 'GET'
    requireApiPath(method, path)
    let response = await send(method, path, request.body)
    if (response.status === 403) {
      const error = await parseForbidden(method, path, response)
      // Only a non-GET carries the token, so only it can be refused for it.
      if (error.code !== 'csrf' || method === 'GET') throw error
      // The session's token changed: refresh it once and retry once.
      await refreshMe()
      response = await send(method, path, request.body)
      if (response.status === 403) throw await parseForbidden(method, path, response)
    }
    return decode<T>(method, path, response)
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
    forbidden = true
    listeners.forEach((listener) => listener())
  }

  async function decode<T>(method: string, path: string, response: Response): Promise<T> {
    if (response.status === 401) {
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
    return me as Me
  }

  return {
    apiFetch,
    loadMe,
    forbidden: () => forbidden,
    subscribe(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
  }
}
