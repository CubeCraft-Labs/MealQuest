import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { planKey, usePlan, useSwapDay, type Plan } from './api.ts'

const WEEK = '2026-10-05'
const PLAN = `/api/v1/plans/${WEEK}`
const MONDAY = `${PLAN}/days/2026-10-05`
const FRIDAY = `${PLAN}/days/2026-10-09`
const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']

// planBody is the plan of the week of Oct 5, dinners d0 to d6, with days
// changed as given by index.
function planBody(changes: Record<number, { id: string; name: string; is_new?: boolean; start_by?: string | null }> = {}) {
  return {
    id: 'plan-1',
    week_start: WEEK,
    status: 'draft',
    ai_note: null,
    calendar_synced_at: null as string | null,
    days: DAYS.map((date, i) => {
      const change = changes[i]
      return {
        date,
        dinner: { id: change?.id ?? `d${i}`, name: change?.name ?? `Dinner ${i}`, photo: null, minutes: null, method: null as string | null, origin: 'regular' },
        is_new: change?.is_new ?? false,
        start_by: change?.start_by ?? null,
      }
    }),
  }
}

// deferred is a reply the test sends when it chooses.
function deferred() {
  let send!: (response: Response) => void
  const reply = new Promise<Response>((resolve) => (send = resolve))
  return { reply, send }
}

// stubServer answers "METHOD path" requests from responders, with /me and
// the Quest log; any other request fails the test.
function stubServer(responders: Record<string, () => Response | Promise<Response>>) {
  const all: Record<string, () => Response | Promise<Response>> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/dinners?status=quest_log': () => jsonResponse(200, { dinners: [] }),
    ...responders,
  }
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>((input, init) => {
      const key = `${init?.method ?? 'GET'} ${String(input)}`
      const respond = Object.hasOwn(all, key) ? all[key] : undefined
      if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
      return Promise.resolve(respond())
    }),
  )
}

// renderSwaps renders the week's plan and swaps over a query client.
async function renderSwaps() {
  const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  const queryClient: QueryClient = createQueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => (
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </ApiProvider>
  )
  const hook = renderHook(() => ({ plan: usePlan(WEEK), swap: useSwapDay(WEEK) }), { wrapper })
  await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
  return { ...hook, queryClient, plan: () => queryClient.getQueryData<Plan>(planKey(WEEK))! }
}

const ORZO = { date: '2026-10-09', dinner: { id: 'q1', name: 'Lemon orzo', photo_id: undefined, minutes: null, method: null }, fromQuestLog: true }
const FISH = { date: '2026-10-05', dinner: { id: 'r1', name: 'Fish tacos', photo_id: undefined, minutes: null, method: null }, fromQuestLog: false }

afterEach(() => {
  vi.unstubAllGlobals()
})

// renderPlan renders the week's plan read over a query client.
async function renderPlan() {
  const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  const wrapper = ({ children }: { children: ReactNode }) => (
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>{children}</QueryClientProvider>
    </ApiProvider>
  )
  const hook = renderHook(() => usePlan(WEEK), { wrapper })
  await waitFor(() => expect(hook.result.current.isPending).toBe(false))
  return hook.result.current
}

describe('usePlan', () => {
  it("reads each dinner's method and when the plan was pushed to Calendar", async () => {
    const body = planBody()
    body.days[2]!.dinner.method = 'crockpot'
    stubServer({ [`GET ${PLAN}`]: () => jsonResponse(200, { ...body, status: 'synced', calendar_synced_at: '2026-10-07T15:58:00Z' }) })
    const plan = await renderPlan()

    expect(plan.data?.calendar_synced_at).toEqual(new Date('2026-10-07T15:58:00Z'))
    expect(plan.data?.days.map((day) => day.dinner.method)).toEqual([null, null, 'crockpot', null, null, null, null])
  })

  it.each([
    ['a method it does not know', (body: ReturnType<typeof planBody>) => (body.days[0]!.dinner.method = 'microwave'), 'method "microwave"'],
    ['no method', (body: ReturnType<typeof planBody>) => Reflect.deleteProperty(body.days[0]!.dinner, 'method'), 'method undefined'],
    ['a sync time that is no instant', (body: ReturnType<typeof planBody>) => (body.calendar_synced_at = 'yesterday'), 'calendar_synced_at "yesterday"'],
  ])('refuses a plan with %s', async (_, change, said) => {
    const body = planBody()
    change(body)
    stubServer({ [`GET ${PLAN}`]: () => jsonResponse(200, body) })
    const plan = await renderPlan()

    expect(plan.error?.message).toContain(said)
  })
})

describe('useSwapDay', () => {
  // Friday's swap is sent first, then Monday's; the server answers Monday's
  // with both swapped, Friday's with only Friday.
  const fridayPlan = planBody({ 4: { id: 'q1', name: 'Lemon orzo', is_new: true } })
  const bothPlan = planBody({ 0: { id: 'r1', name: 'Fish tacos' }, 4: { id: 'q1', name: 'Lemon orzo', is_new: true } })

  it.each([
    ['the later swap answered first', ['monday', 'friday'] as const],
    ['the earlier swap answered first', ['friday', 'monday'] as const],
  ])('keeps the plan of the later of two swaps, %s', async (_, order) => {
    const replies = { friday: deferred(), monday: deferred() }
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`PUT ${FRIDAY}`]: () => replies.friday.reply,
      [`PUT ${MONDAY}`]: () => replies.monday.reply,
    })
    const { result, plan, queryClient } = await renderSwaps()

    act(() => result.current.swap.mutate(ORZO))
    act(() => result.current.swap.mutate(FISH))
    await waitFor(() => expect(plan().days[0]!.dinner.name).toBe('Fish tacos'))

    // Whichever reply settles, Monday stays swapped: Friday's plan, the
    // earlier one, never lands over it.
    for (const [settled, day] of order.entries()) {
      await act(async () => replies[day].send(jsonResponse(200, day === 'friday' ? fridayPlan : bothPlan)))
      await waitFor(() => expect(queryClient.isMutating()).toBe(1 - settled))
      expect(plan().days[0]!.dinner.name).toBe('Fish tacos')
    }
    expect(plan().days.map((day) => day.dinner.name)).toEqual(bothPlan.days.map((day) => day.dinner.name))
  })

  it("does not take an earlier swap's failure for the week's", async () => {
    const friday = deferred()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`PUT ${FRIDAY}`]: () => friday.reply,
      [`PUT ${MONDAY}`]: () => jsonResponse(200, planBody({ 0: { id: 'r1', name: 'Fish tacos' } })),
    })
    const { result, plan } = await renderSwaps()

    act(() => result.current.swap.mutate(ORZO))
    act(() => result.current.swap.mutate(FISH))
    await waitFor(() => expect(plan().days[0]!.dinner.name).toBe('Fish tacos'))
    await act(async () => friday.send(errorResponse(409, 'dinner_not_plannable')))

    await waitFor(() => expect(result.current.swap.pending).toBe(false))
    expect(result.current.swap.failed).toBeNull()
  })

  it('leaves a day a later plan read landed when the swap of it fails', async () => {
    const friday = deferred()
    // The read finds Friday already lemon orzo, starting by 2 pm.
    let read = planBody()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, read),
      [`PUT ${FRIDAY}`]: () => friday.reply,
    })
    const { result, plan, queryClient } = await renderSwaps()

    act(() => result.current.swap.mutate(ORZO))
    await waitFor(() => expect(plan().days[4]).toEqual({ date: '2026-10-09', dinner: ORZO.dinner, is_new: true, start_by: null }))
    read = planBody({ 4: { id: 'q1', name: 'Lemon orzo', is_new: true, start_by: '2026-10-09T14:00:00-04:00' } })
    await act(() => result.current.plan.refetch())
    await act(async () => friday.send(errorResponse(409, 'dinner_not_plannable')))

    await waitFor(() => expect(queryClient.isMutating()).toBe(0))
    expect(plan().days[4]).toEqual({ date: '2026-10-09', dinner: ORZO.dinner, is_new: true, start_by: '2026-10-09T14:00:00-04:00' })
    expect(result.current.swap.failed?.error.message).toBe('dinner_not_plannable message')
  })

  it('refuses to dismiss a pending swap', async () => {
    const friday = deferred()
    stubServer({ [`GET ${PLAN}`]: () => jsonResponse(200, planBody()), [`PUT ${FRIDAY}`]: () => friday.reply })
    const { result } = await renderSwaps()

    act(() => result.current.swap.mutate(ORZO))
    await waitFor(() => expect(result.current.swap.pending).toBe(true))
    expect(() => result.current.swap.dismiss()).toThrow('The week of 2026-10-05 has a swap pending, which cannot be dismissed.')
    await act(async () => friday.send(jsonResponse(200, planBody())))
  })
})
