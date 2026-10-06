import { vi, type Mock } from 'vitest'
import type { ApiLocation, Me } from '../api/index.ts'

export const ME: Me = {
  name: 'Sam',
  email: 'sam@example.com',
  household: { label: 'Our kitchen' },
  csrfToken: 'token-1',
}

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

export function errorResponse(status: number, code: string, retryable = false): Response {
  return jsonResponse(status, { error: { code, message: `${code} message`, retryable } })
}

// stubFetch replaces the global fetch with a mock answered from responses in
// order; a request beyond them fails the test. Undo with vi.unstubAllGlobals.
export function stubFetch(...responses: Response[]): Mock<typeof fetch> {
  const fetchMock = vi.fn<typeof fetch>()
  for (const response of responses) fetchMock.mockResolvedValueOnce(response)
  fetchMock.mockImplementation((input, init) => {
    throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${String(input)}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// request returns the method, path and headers of the fetch mock's nth call.
export function request(fetchMock: Mock<typeof fetch>, n: number) {
  const call = fetchMock.mock.calls[n]
  if (call === undefined) throw new Error(`fetch was called ${fetchMock.mock.calls.length} times, not ${n + 1}`)
  const [input, init] = call
  return {
    path: String(input),
    method: init?.method,
    credentials: init?.credentials,
    headers: new Headers(init?.headers),
    body: init?.body,
  }
}

// fakeLocation stands in for window.location on https://mealquest.test,
// recording navigations.
export function fakeLocation(pathname = '/', search = ''): ApiLocation & { assign: Mock<(url: string | URL) => void> } {
  return { origin: 'https://mealquest.test', pathname, search, assign: vi.fn<(url: string | URL) => void>() }
}

// stubFetchByPath replaces the global fetch with a mock that answers each
// path from responses with a fresh response from its factory, so requests
// sent in parallel need no order; a path not in responses fails the test.
// Undo with vi.unstubAllGlobals.
export function stubFetchByPath(responses: Record<string, () => Response>): Mock<typeof fetch> {
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const respond = Object.hasOwn(responses, String(input)) ? responses[String(input)] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${String(input)}`)
    return Promise.resolve(respond())
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}
