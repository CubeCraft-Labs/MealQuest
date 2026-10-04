import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, type ApiClient } from '../api/index.ts'
import { fakeLocation, jsonResponse, stubFetch } from '../test/fetchStub.ts'
import { createNetworkStatus } from './networkStatus.ts'
import { OfflinePill } from './OfflinePill.tsx'

const copy = 'Offline — showing saved data'

let onLine: boolean

beforeEach(() => {
  onLine = true
  vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => onLine)
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function goOffline() {
  onLine = false
  window.dispatchEvent(new Event('offline'))
}

function goOnline() {
  onLine = true
  window.dispatchEvent(new Event('online'))
}

function renderPill(): ApiClient {
  const api = createApiClient(fakeLocation(), createNetworkStatus(window))
  render(
    <ApiProvider client={api}>
      <OfflinePill />
    </ApiProvider>,
  )
  return api
}

describe('OfflinePill', () => {
  it('is hidden while online', () => {
    renderPill()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('shows while the browser is offline when it renders', () => {
    onLine = false
    renderPill()
    expect(screen.getByRole('status')).toHaveTextContent(copy)
  })

  it('appears on the offline event and disappears on the online event', () => {
    renderPill()
    act(goOffline)
    expect(screen.getByRole('status')).toHaveTextContent(copy)
    act(goOnline)
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('appears when a request fails with a network error', async () => {
    const api = renderPill()
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
    await act(() => expect(api.apiFetch('/api/v1/plan')).rejects.toThrow(TypeError))
    expect(screen.getByRole('status')).toHaveTextContent(copy)

    act(goOnline)
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('disappears when a later request gets a response', async () => {
    const api = renderPill()
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Failed to fetch')))
    await act(() => expect(api.apiFetch('/api/v1/plan')).rejects.toThrow(TypeError))
    expect(screen.getByRole('status')).toBeInTheDocument()

    stubFetch(jsonResponse(200, {}))
    await act(() => api.apiFetch('/api/v1/plan'))
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('ignores an aborted request', async () => {
    const api = renderPill()
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new DOMException('aborted', 'AbortError')))
    await act(() => expect(api.apiFetch('/api/v1/plan')).rejects.toThrow('aborted'))
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('removes its online and offline listeners on unmount', () => {
    const add = vi.spyOn(window, 'addEventListener')
    const remove = vi.spyOn(window, 'removeEventListener')
    const api = createApiClient(fakeLocation(), createNetworkStatus(window))
    const { unmount } = render(
      <ApiProvider client={api}>
        <OfflinePill />
      </ApiProvider>,
    )
    const added = add.mock.calls.filter(([type]) => type === 'online' || type === 'offline')
    expect(added.map(([type]) => type).sort()).toEqual(['offline', 'online'])

    unmount()
    for (const [type, listener] of added) expect(remove).toHaveBeenCalledWith(type, listener)
  })
})
