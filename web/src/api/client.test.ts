import { afterEach, describe, expect, it, vi } from 'vitest'
import { errorResponse, fakeLocation, jsonResponse, ME, request, stubFetch } from '../test/fetchStub.ts'
import { ApiError, createApiClient, CSRF_HEADER } from './index.ts'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('apiFetch', () => {
  it('sends same-origin JSON and the cached CSRF token on non-GET requests only', async () => {
    const fetchMock = stubFetch(jsonResponse(200, ME), jsonResponse(200, { ok: 1 }), jsonResponse(200, { ok: 2 }))
    const api = createApiClient(fakeLocation())
    await api.loadMe()

    await expect(api.apiFetch('/api/v1/settings/household')).resolves.toEqual({ ok: 1 })
    await expect(
      api.apiFetch('/api/v1/settings/household', { method: 'PATCH', body: { dinnerTime: '18:00' } }),
    ).resolves.toEqual({ ok: 2 })

    const get = request(fetchMock, 1)
    expect(get).toMatchObject({ path: '/api/v1/settings/household', method: 'GET', credentials: 'same-origin' })
    expect(get.headers.has(CSRF_HEADER)).toBe(false)

    const patch = request(fetchMock, 2)
    expect(patch).toMatchObject({ method: 'PATCH', credentials: 'same-origin', body: '{"dinnerTime":"18:00"}' })
    expect(patch.headers.get(CSRF_HEADER)).toBe('token-1')
    expect(patch.headers.get('Content-Type')).toBe('application/json')
  })

  it('refuses a non-GET request before /me has loaded', async () => {
    const fetchMock = stubFetch()
    const api = createApiClient(fakeLocation())
    await expect(api.apiFetch('/api/v1/jobs', { method: 'POST', body: {} })).rejects.toThrow(/no CSRF token/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    'https://evil.example/api/v1/me',
    '//evil.example/api/v1/me',
    // The placeholder origin an earlier guard compared against.
    '//same-origin.invalid/api/v1/me',
    'https://same-origin.invalid/api/v1/me',
    '/\\evil.example/api/v1/me',
    '/auth/logout',
    '/api/v1/../auth/logout',
    '/api/v1/%2e%2e/auth/logout',
    'api/v1/me',
  ])('refuses %s without sending it or the CSRF token', async (path) => {
    const fetchMock = stubFetch(jsonResponse(200, ME))
    const api = createApiClient(fakeLocation())
    await api.loadMe()

    await expect(api.apiFetch(path, { method: 'POST', body: {} })).rejects.toThrow(
      `POST ${path}: not a same-origin /api/v1/ path`,
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(request(fetchMock, 0)).toMatchObject({ path: '/api/v1/me', method: 'GET' })
    expect(request(fetchMock, 0).headers.has(CSRF_HEADER)).toBe(false)
  })

  it('rejects with a typed ApiError from the error body', async () => {
    stubFetch(errorResponse(503, 'unavailable', true))
    const api = createApiClient(fakeLocation())
    const error = await api.apiFetch('/api/v1/jobs/j1').catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 503, code: 'unavailable', message: 'unavailable message', retryable: true })
  })

  it('rejects a response without the API error body', async () => {
    stubFetch(new Response('<html>bad gateway</html>', { status: 502 }))
    const api = createApiClient(fakeLocation())
    await expect(api.apiFetch('/api/v1/jobs/j1')).rejects.toThrow('GET /api/v1/jobs/j1: 502 response is not JSON')
  })

  it('on 401 navigates to sign-in with the current path and search to return to', async () => {
    stubFetch(errorResponse(401, 'unauthenticated'))
    const location = fakeLocation('/plan/shopping', '?week=2026-10-05')
    const api = createApiClient(location)

    await expect(api.apiFetch('/api/v1/settings/household')).rejects.toMatchObject({ status: 401 })
    expect(location.assign).toHaveBeenCalledExactlyOnceWith(
      '/auth/login?return=%2Fplan%2Fshopping%3Fweek%3D2026-10-05',
    )
  })

  it('on a csrf 403 refreshes /me once and retries with the new token', async () => {
    const fetchMock = stubFetch(
      jsonResponse(200, ME),
      errorResponse(403, 'csrf'),
      jsonResponse(200, { ...ME, csrfToken: 'token-2' }),
      jsonResponse(201, { id: 'j1' }),
    )
    const api = createApiClient(fakeLocation())
    await api.loadMe()

    await expect(api.apiFetch('/api/v1/jobs', { method: 'POST', body: { kind: 'noop' } })).resolves.toEqual({
      id: 'j1',
    })
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(request(fetchMock, 1).headers.get(CSRF_HEADER)).toBe('token-1')
    expect(request(fetchMock, 2)).toMatchObject({ path: '/api/v1/me', method: 'GET' })
    expect(request(fetchMock, 3)).toMatchObject({ path: '/api/v1/jobs', method: 'POST', body: '{"kind":"noop"}' })
    expect(request(fetchMock, 3).headers.get(CSRF_HEADER)).toBe('token-2')
    expect(api.forbidden()).toBe(false)
  })

  it('does not loop when the retry gets a csrf 403 too', async () => {
    const fetchMock = stubFetch(
      jsonResponse(200, ME),
      errorResponse(403, 'csrf'),
      jsonResponse(200, { ...ME, csrfToken: 'token-2' }),
      errorResponse(403, 'csrf'),
    )
    const api = createApiClient(fakeLocation())
    await api.loadMe()

    await expect(api.apiFetch('/api/v1/jobs', { method: 'POST', body: {} })).rejects.toMatchObject({
      status: 403,
      code: 'csrf',
    })
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(api.forbidden()).toBe(false)
  })

  it('marks any other 403 as not in this household and notifies subscribers', async () => {
    const fetchMock = stubFetch(errorResponse(403, 'forbidden'))
    const api = createApiClient(fakeLocation())
    const listener = vi.fn()
    api.subscribe(listener)

    await expect(api.apiFetch('/api/v1/me')).rejects.toMatchObject({ status: 403, code: 'forbidden' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(api.forbidden()).toBe(true)
    expect(listener).toHaveBeenCalledTimes(1)
  })

  it('does not take a 403 without the API error body as not in this household', async () => {
    stubFetch(new Response('<html>denied by proxy</html>', { status: 403 }))
    const api = createApiClient(fakeLocation())
    await expect(api.apiFetch('/api/v1/me')).rejects.toThrow('GET /api/v1/me: 403 response is not JSON')
    expect(api.forbidden()).toBe(false)
  })

  it('does not refresh /me for a csrf 403 on a GET, which carries no token', async () => {
    const fetchMock = stubFetch(errorResponse(403, 'csrf'))
    const api = createApiClient(fakeLocation())
    await expect(api.loadMe()).rejects.toMatchObject({ status: 403, code: 'csrf' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('loadMe', () => {
  it('rejects a /me response without a CSRF token', async () => {
    stubFetch(jsonResponse(200, { ...ME, csrfToken: '' }))
    const api = createApiClient(fakeLocation())
    await expect(api.loadMe()).rejects.toThrow('response is not a member with a CSRF token')
  })
})
