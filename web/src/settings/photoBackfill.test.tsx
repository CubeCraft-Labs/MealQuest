import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { HOME_KEY } from '../home/api.ts'
import { planKey } from '../plan/api.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { fakeLocation, jsonResponse, ME, stubFetch } from '../test/fetchStub.ts'
import { parseBackfillResult, usePhotoBackfill } from './photoBackfill.ts'

afterEach(() => {
  vi.unstubAllGlobals()
})

const RESULT = { dinners: [{ dinner_id: 'd3', outcome: 'grounded', reason: null }] }

function backfillJob(status: string, result: unknown) {
  return jsonResponse(200, { id: 'j9', kind: 'backfill_dinner_photos', status, result, error: null })
}

describe('usePhotoBackfill', () => {
  it('reads the plans, home and dinners again once the backfill ends, so a found photo shows everywhere', async () => {
    const fetchMock = stubFetch(
      jsonResponse(200, ME),
      jsonResponse(202, { id: 'j9' }),
      backfillJob('running', null),
      backfillJob('succeeded', RESULT),
    )
    const api = createApiClient(fakeLocation(), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    const queryClient = createQueryClient()
    const plan = planKey('2026-10-05')
    for (const key of [plan, HOME_KEY, ['dinners', 'quest_log']]) queryClient.setQueryData(key, {})
    const wrapper = ({ children }: { children: ReactNode }) => (
      <ApiProvider client={api}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </ApiProvider>
    )
    const { result } = renderHook(() => usePhotoBackfill(), { wrapper })

    act(() => result.current.start())
    await waitFor(() => expect(result.current.status).toBe('running'))
    expect(result.current.active).toBe(true)
    expect(queryClient.getQueryState(plan)?.isInvalidated).toBe(false)

    await waitFor(() => expect(result.current.status).toBe('succeeded'), { timeout: 3000 })
    expect(result.current).toMatchObject({ active: false, error: null, result: RESULT })
    expect(fetchMock).toHaveBeenCalledTimes(4)
    for (const key of [plan, HOME_KEY, ['dinners', 'quest_log']]) {
      expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true)
    }
  })
})

describe('parseBackfillResult', () => {
  it('refuses a result that is not a photo backfill', () => {
    expect(() => parseBackfillResult('job', { dinners: [{ dinner_id: 'd3', outcome: 'lost', reason: null }] })).toThrow(
      'job: outcome "lost" is not one of grounded, failed, skipped',
    )
    expect(() => parseBackfillResult('job', { stage: 'x' })).toThrow('job: result is not a photo backfill')
  })
})
