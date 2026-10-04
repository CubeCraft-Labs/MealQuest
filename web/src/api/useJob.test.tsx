import { act, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { errorResponse, fakeLocation, jsonResponse, request, stubFetch } from '../test/fetchStub.ts'
import { ApiProvider, createApiClient, useJob } from './index.ts'

type Draft = { dinners: string[] }

function job(status: string, result: Draft | null, error: string | null = null) {
  return jsonResponse(200, { id: 'j1', kind: 'plan.draft', status, result, error })
}

function renderJob(jobId: string | null) {
  const api = createApiClient(fakeLocation())
  const wrapper = ({ children }: { children: ReactNode }) => <ApiProvider client={api}>{children}</ApiProvider>
  return renderHook(({ id }) => useJob<Draft>(id), { wrapper, initialProps: { id: jobId } })
}

// advance runs the timers due in ms and lets the polls they start answer.
async function advance(ms: number) {
  await act(() => vi.advanceTimersByTimeAsync(ms))
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useJob', () => {
  it('exposes the partial result while running, then the terminal result, backing off 500 ms to 2 s', async () => {
    const fetchMock = stubFetch(
      job('queued', null),
      job('running', { dinners: ['Tacos'] }),
      job('running', { dinners: ['Tacos', 'Chili'] }),
      job('running', { dinners: ['Tacos', 'Chili'] }),
      job('succeeded', { dinners: ['Tacos', 'Chili', 'Soup'] }),
    )
    const { result } = renderJob('j1')
    expect(result.current).toEqual({ status: undefined, result: null, error: null })

    await advance(0)
    expect(result.current.status).toBe('queued')
    expect(request(fetchMock, 0)).toMatchObject({ path: '/api/v1/jobs/j1', method: 'GET' })

    await advance(499)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await advance(1)
    expect(result.current).toEqual({ status: 'running', result: { dinners: ['Tacos'] }, error: null })

    await advance(999)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await advance(1)
    expect(result.current.result).toEqual({ dinners: ['Tacos', 'Chili'] })

    await advance(2000)
    expect(fetchMock).toHaveBeenCalledTimes(4)
    await advance(2000)
    expect(fetchMock).toHaveBeenCalledTimes(5)
    expect(result.current).toEqual({
      status: 'succeeded',
      result: { dinners: ['Tacos', 'Chili', 'Soup'] },
      error: null,
    })

    // Terminal: no timer left, no further polls.
    expect(vi.getTimerCount()).toBe(0)
    await advance(10_000)
    expect(fetchMock).toHaveBeenCalledTimes(5)
  })

  it('exposes a failed job as its error and stops', async () => {
    const fetchMock = stubFetch(job('running', { dinners: ['Tacos'] }), job('failed', null, 'model refused'))
    const { result } = renderJob('j1')

    await advance(0)
    await advance(500)
    expect(result.current.status).toBe('failed')
    expect(result.current.error?.message).toBe('model refused')
    expect(vi.getTimerCount()).toBe(0)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('exposes a failed poll as the error and keeps polling to the terminal status', async () => {
    const fetchMock = stubFetch(
      job('running', { dinners: ['Tacos'] }),
      errorResponse(503, 'unavailable', true),
      job('succeeded', { dinners: ['Tacos', 'Chili'] }),
    )
    const { result } = renderJob('j1')

    await advance(0)
    await advance(500)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toMatchObject({ status: 'running', result: { dinners: ['Tacos'] } })
    expect(result.current.error).toMatchObject({ status: 503, code: 'unavailable' })
    expect(vi.getTimerCount()).toBe(1)

    // The backoff carries on from the failed poll: the next one is 1 s later.
    await advance(999)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await advance(1)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toEqual({ status: 'succeeded', result: { dinners: ['Tacos', 'Chili'] }, error: null })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('stops polling on unmount after a failed poll', async () => {
    const fetchMock = stubFetch(errorResponse(503, 'unavailable', true))
    const { result, unmount } = renderJob('j1')

    await advance(0)
    expect(result.current).toMatchObject({ status: undefined, result: null })
    expect(result.current.error).toMatchObject({ status: 503 })
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
    await advance(10_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('stops polling the old job when the job id changes', async () => {
    const fetchMock = stubFetch(job('running', null), job('running', null))
    const { result, rerender } = renderJob('j1')

    await advance(0)
    rerender({ id: 'j2' })
    expect(result.current).toEqual({ status: undefined, result: null, error: null })
    await advance(0)
    expect(request(fetchMock, 1).path).toBe('/api/v1/jobs/j2')
    expect(vi.getTimerCount()).toBe(1)
  })

  it('stops polling on unmount', async () => {
    const fetchMock = stubFetch(job('running', null))
    const { unmount } = renderJob('j1')

    await advance(0)
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
    await advance(10_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('ignores a poll that answers after unmount', async () => {
    let answer!: (response: Response) => void
    const fetchMock = stubFetch()
    fetchMock.mockImplementationOnce(() => new Promise((resolve) => (answer = resolve)))
    const { result, unmount } = renderJob('j1')

    unmount()
    await act(async () => answer(await Promise.resolve(job('running', null))))
    expect(vi.getTimerCount()).toBe(0)
    expect(result.current.status).toBeUndefined()
  })

  it('polls nothing without a job id', async () => {
    const fetchMock = stubFetch()
    const { result } = renderJob(null)
    await advance(5000)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(result.current).toEqual({ status: undefined, result: null, error: null })
  })
})
