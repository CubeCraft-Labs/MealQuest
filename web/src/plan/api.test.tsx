import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { act, render, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter, Route, Routes, useNavigate, type NavigateFunction } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { planKey, usePlan, usePushToCalendar, useSwapDay, type Plan, type PushToCalendar, type SwapDay } from './api.ts'

const WEEK = '2026-10-05'
const PLAN = `/api/v1/plans/${WEEK}`

// GENERATED is a push answer's Supply run when the push made it.
const GENERATED = { supply_run: { status: 'generated', error: null } }

// SUPPLY_RUN_FAILED is a push answer's Supply run when it could not be made.
const SUPPLY_RUN_FAILED = {
  supply_run: {
    status: 'failed',
    error: {
      code: 'supply_run_failed',
      message: 'the plan is on Calendar, but its Supply run could not be made; make it again from the Supply run',
      retryable: true,
    },
  },
}
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
    // Date reads both, in this device's zone: neither says which instant.
    [
      'a sync time with no offset',
      (body: ReturnType<typeof planBody>) => (body.calendar_synced_at = '2026-10-07T15:58:00'),
      'calendar_synced_at "2026-10-07T15:58:00" is not an instant',
    ],
    [
      'a sync date with no time',
      (body: ReturnType<typeof planBody>) => (body.calendar_synced_at = '2026-10-07'),
      'calendar_synced_at "2026-10-07" is not an instant',
    ],
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

  it('leaves a day a later plan read landed, the very day the swap wrote, when the swap of it fails', async () => {
    const friday = deferred()
    let read = planBody()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, read),
      [`PUT ${FRIDAY}`]: () => friday.reply,
    })
    const { result, plan, queryClient } = await renderSwaps()

    // The read finds Friday lemon orzo, field for field the day the swap
    // wrote: the server has it, whatever this swap's PUT is answered with.
    const swapped = { date: '2026-10-09', dinner: ORZO.dinner, is_new: true, start_by: null }
    act(() => result.current.swap.mutate(ORZO))
    await waitFor(() => expect(plan().days[4]).toEqual(swapped))
    read = planBody({ 4: { id: 'q1', name: 'Lemon orzo', is_new: true } })
    await act(() => result.current.plan.refetch())
    await act(async () => friday.send(errorResponse(500, 'internal')))

    await waitFor(() => expect(queryClient.isMutating()).toBe(0))
    expect(plan().days[4]).toEqual(swapped)
    expect(result.current.swap.failed?.error.message).toBe('internal message')
  })

  it('puts the day back when the swap of it fails with no plan read since, after a later swap of another day', async () => {
    const friday = deferred()
    const monday = deferred()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`PUT ${FRIDAY}`]: () => friday.reply,
      [`PUT ${MONDAY}`]: () => monday.reply,
    })
    const { result, plan, queryClient } = await renderSwaps()

    act(() => result.current.swap.mutate(ORZO))
    act(() => result.current.swap.mutate(FISH))
    await waitFor(() => expect(plan().days[0]!.dinner.name).toBe('Fish tacos'))
    await act(async () => friday.send(errorResponse(500, 'internal')))

    await waitFor(() => expect(plan().days[4]!.dinner.name).toBe('Dinner 4'))
    expect(plan().days[0]!.dinner.name).toBe('Fish tacos')
    await act(async () => monday.send(errorResponse(500, 'internal')))
    await waitFor(() => expect(queryClient.isMutating()).toBe(0))
    expect(plan().days.map((day) => day.dinner.name)).toEqual(planBody().days.map((day) => day.dinner.name))
  })

  it('reads the plan again when the dinner is already on another day of the week, and sends the swap only once', async () => {
    let read = planBody()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, read),
      [`PUT ${FRIDAY}`]: () => errorResponse(409, 'dinner_already_planned'),
    })
    const { result, plan, queryClient } = await renderSwaps()

    // Meanwhile lemon orzo was planned on Wednesday.
    read = planBody({ 2: { id: 'q1', name: 'Lemon orzo', is_new: true } })
    act(() => result.current.swap.mutate(ORZO))

    await waitFor(() => expect(plan().days[2]!.dinner.name).toBe('Lemon orzo'))
    expect(plan().days[4]!.dinner.name).toBe('Dinner 4')
    await waitFor(() => expect(queryClient.isMutating()).toBe(0))
    expect(result.current.swap.failed?.error.message).toBe('dinner_already_planned message')
    const sent = vi.mocked(fetch).mock.calls.map(([input, init]) => `${init?.method ?? 'GET'} ${String(input)}`)
    expect(sent.filter((call) => call === `PUT ${FRIDAY}`)).toHaveLength(1)
    expect(sent.filter((call) => call === `GET ${PLAN}`)).toHaveLength(2)
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

describe('week mutations', () => {
  const NEXT = '2026-10-12'
  const LATER = '2026-10-19'

  // weekMutations lists the swaps and pushes the mutation cache holds for
  // the week starting on weekStart, with their status.
  function weekMutations(queryClient: QueryClient, weekStart: string) {
    return queryClient
      .getMutationCache()
      .findAll({ mutationKey: ['plans', weekStart] })
      .map((mutation) => `${String(mutation.options.mutationKey![2])} ${mutation.state.status}`)
  }

  it('keeps settled swaps and pushes while their week is shown, and leaves them behind once it is not, unless one is pending', async () => {
    const nextPush = deferred()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`PUT ${FRIDAY}`]: () => jsonResponse(200, planBody({ 4: { id: 'q1', name: 'Lemon orzo', is_new: true } })),
      [`POST ${PLAN}/push-calendar`]: () => errorResponse(500, 'internal'),
      [`GET /api/v1/plans/${NEXT}`]: () => errorResponse(404, 'not_found'),
      [`POST /api/v1/plans/${NEXT}/push-calendar`]: () => nextPush.reply,
      [`GET /api/v1/plans/${LATER}`]: () => errorResponse(404, 'not_found'),
    })
    const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    const queryClient = createQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <ApiProvider client={api}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </ApiProvider>
    )
    const hook = renderHook(
      ({ week }) => ({ plan: usePlan(week), swap: useSwapDay(week), push: usePushToCalendar(week) }),
      { wrapper, initialProps: { week: WEEK } },
    )
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))

    // A swap and a push of the week settle: both stay while it is shown,
    // the push's failure speaking for the week.
    act(() => hook.result.current.swap.mutate(ORZO))
    await waitFor(() => expect(hook.result.current.swap.pending).toBe(false))
    act(() => hook.result.current.push.mutate())
    await waitFor(() => expect(hook.result.current.push.error).not.toBeNull())
    expect(weekMutations(queryClient, WEEK)).toEqual(['swap success', 'push error'])

    // Another week shown leaves them behind; a push sent there stays
    // pending across showing a third week.
    hook.rerender({ week: NEXT })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
    expect(weekMutations(queryClient, WEEK)).toEqual([])
    act(() => hook.result.current.push.mutate())
    hook.rerender({ week: LATER })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
    expect(weekMutations(queryClient, NEXT)).toEqual(['push pending'])
    hook.rerender({ week: NEXT })
    expect(hook.result.current.push.pending).toBe(true)

    // Once it settles, it stays while its week is shown and is left behind
    // once another is.
    await act(async () => nextPush.send(errorResponse(500, 'internal')))
    await waitFor(() => expect(hook.result.current.push.error).not.toBeNull())
    expect(weekMutations(queryClient, NEXT)).toEqual(['push error'])
    hook.rerender({ week: LATER })
    expect(weekMutations(queryClient, NEXT)).toEqual([])
  })

  it('leaves behind what settles in a week no longer shown, unless that week still has one pending or it is the failure that speaks for it', async () => {
    const friday = deferred()
    const monday = deferred()
    const nextPush = deferred()
    const laterPush = deferred()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`PUT ${FRIDAY}`]: () => friday.reply,
      [`PUT ${MONDAY}`]: () => monday.reply,
      [`GET /api/v1/plans/${NEXT}`]: () => errorResponse(404, 'not_found'),
      [`POST /api/v1/plans/${NEXT}/push-calendar`]: () => nextPush.reply,
      [`GET /api/v1/plans/${LATER}`]: () => errorResponse(404, 'not_found'),
      [`POST /api/v1/plans/${LATER}/push-calendar`]: () => laterPush.reply,
    })
    const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    const queryClient = createQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <ApiProvider client={api}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </ApiProvider>
    )
    const hook = renderHook(
      ({ week }) => ({ plan: usePlan(week), swap: useSwapDay(week), push: usePushToCalendar(week) }),
      { wrapper, initialProps: { week: WEEK } },
    )
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))

    // Two swaps of the week and a push of the next are left pending, and a
    // third week is shown.
    act(() => hook.result.current.swap.mutate(ORZO))
    act(() => hook.result.current.swap.mutate(FISH))
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual(['swap pending', 'swap pending']))
    hook.rerender({ week: NEXT })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
    act(() => hook.result.current.push.mutate())
    hook.rerender({ week: LATER })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
    expect(weekMutations(queryClient, NEXT)).toEqual(['push pending'])

    // The later swap settles first: the earlier, still pending, must see it,
    // so both stay.
    await act(async () => monday.send(jsonResponse(200, planBody({ 0: { id: 'r1', name: 'Fish tacos' } }))))
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual(['swap pending', 'swap success']))

    // The push succeeds while the third week is shown: it is left behind,
    // and the week's pending swap is not.
    await act(async () => nextPush.send(jsonResponse(200, { ...planBody(), week_start: NEXT, ...GENERATED })))
    await waitFor(() => expect(weekMutations(queryClient, NEXT)).toEqual([]))
    expect(weekMutations(queryClient, WEEK)).toEqual(['swap pending', 'swap success'])

    // Once the earlier swap fails, the week has none pending: both are left
    // behind, the failure not being the week's latest.
    await act(async () => friday.send(errorResponse(409, 'dinner_not_plannable')))
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual([]))

    // A push of the third week fails once another is shown: it stays, to be
    // shown on coming back, until yet another week is.
    act(() => hook.result.current.push.mutate())
    hook.rerender({ week: NEXT })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
    await act(async () => laterPush.send(errorResponse(500, 'internal')))
    await waitFor(() => expect(weekMutations(queryClient, LATER)).toEqual(['push error']))
    hook.rerender({ week: WEEK })
    expect(weekMutations(queryClient, LATER)).toEqual([])
  })

  it("leaves behind a week's settled swaps but for the failure that speaks for it, which stays until its week leaves it behind", async () => {
    let refuse = true
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`PUT ${FRIDAY}`]: () => (refuse ? errorResponse(409, 'dinner_not_plannable') : jsonResponse(200, planBody())),
      [`PUT ${MONDAY}`]: () => jsonResponse(200, planBody({ 0: { id: 'r1', name: 'Fish tacos' } })),
      [`GET /api/v1/plans/${NEXT}`]: () => errorResponse(404, 'not_found'),
      [`GET /api/v1/plans/${LATER}`]: () => errorResponse(404, 'not_found'),
    })
    const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    const queryClient = createQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <ApiProvider client={api}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </ApiProvider>
    )
    const hook = renderHook(({ week }) => ({ plan: usePlan(week), swap: useSwapDay(week) }), {
      wrapper,
      initialProps: { week: WEEK },
    })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))

    // Two swaps settle while the week is shown: the earlier is needed for
    // ordering no longer, and only the latest stays.
    act(() => hook.result.current.swap.mutate(FISH))
    act(() => hook.result.current.swap.mutate(ORZO))
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual(['swap error']))

    // Leaving the week, through two others, keeps the failure alone.
    for (const week of [NEXT, LATER, NEXT]) {
      hook.rerender({ week })
      await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
      expect(weekMutations(queryClient, WEEK)).toEqual(['swap error'])
    }

    // Coming back shows it; it fails again, and again one stays.
    hook.rerender({ week: WEEK })
    expect(hook.result.current.swap.failed?.error.message).toBe('dinner_not_plannable message')
    act(() => hook.result.current.swap.dismiss())
    act(() => hook.result.current.swap.mutate(ORZO))
    await waitFor(() => expect(hook.result.current.swap.failed).not.toBeNull())
    expect(weekMutations(queryClient, WEEK)).toEqual(['swap error'])

    // A swap that succeeds is left behind once the week is.
    refuse = false
    act(() => hook.result.current.swap.dismiss())
    act(() => hook.result.current.swap.mutate(ORZO))
    await waitFor(() => expect(hook.result.current.swap.succeeded).not.toBeNull())
    expect(weekMutations(queryClient, WEEK)).toEqual(['swap success'])
    hook.rerender({ week: NEXT })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
    expect(weekMutations(queryClient, WEEK)).toEqual([])
  })

  it('keeps a push whose Supply run was not made, settled in a week no longer shown, to be shown on coming back', async () => {
    const nextPush = deferred()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`GET /api/v1/plans/${NEXT}`]: () => jsonResponse(200, { ...planBody(), week_start: NEXT }),
      [`POST /api/v1/plans/${NEXT}/push-calendar`]: () => nextPush.reply,
    })
    const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    const queryClient = createQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <ApiProvider client={api}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </ApiProvider>
    )
    const hook = renderHook(({ week }) => ({ plan: usePlan(week), push: usePushToCalendar(week) }), {
      wrapper,
      initialProps: { week: NEXT },
    })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))

    // The push is pushed to Calendar while another week is shown, but its
    // Supply run is not made: it stays, though it succeeded.
    act(() => hook.result.current.push.mutate())
    hook.rerender({ week: WEEK })
    await waitFor(() => expect(hook.result.current.plan.isSuccess).toBe(true))
    const synced = { ...planBody(), week_start: NEXT, status: 'synced', calendar_synced_at: '2026-10-08T06:00:00Z' }
    await act(async () => nextPush.send(jsonResponse(200, { ...synced, ...SUPPLY_RUN_FAILED })))
    await waitFor(() => expect(weekMutations(queryClient, NEXT)).toEqual(['push success']))

    // Coming back mounts the week's board again, as the Plan's does.
    hook.unmount()
    const back = renderHook(() => usePushToCalendar(NEXT), { wrapper })
    expect(back.result.current.error).toBeNull()
    expect(back.result.current.supplyRun).toEqual(SUPPLY_RUN_FAILED.supply_run)
  })

  it('leaves behind what settles once Plan is left for another route, and keeps what is pending', async () => {
    const friday = deferred()
    const monday = deferred()
    const push = deferred()
    stubServer({
      [`GET ${PLAN}`]: () => jsonResponse(200, planBody()),
      [`PUT ${FRIDAY}`]: () => friday.reply,
      [`PUT ${MONDAY}`]: () => monday.reply,
      [`POST ${PLAN}/push-calendar`]: () => push.reply,
    })
    const api = createApiClient(fakeLocation('/plan'), createNetworkStatus(window), createVerifiedSession(localStorage))
    await api.loadMe()
    const queryClient = createQueryClient()
    let week!: { plan: ReturnType<typeof usePlan>; swap: SwapDay; push: PushToCalendar }
    let navigate!: NavigateFunction
    function Week() {
      week = { plan: usePlan(WEEK), swap: useSwapDay(WEEK), push: usePushToCalendar(WEEK) }
      return null
    }
    function Navigator() {
      navigate = useNavigate()
      return null
    }
    render(
      <ApiProvider client={api}>
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={['/plan']}>
            <Navigator />
            <Routes>
              <Route path="/plan" element={<Week />} />
              <Route path="/settings" element={null} />
            </Routes>
          </MemoryRouter>
        </QueryClientProvider>
      </ApiProvider>,
    )
    await waitFor(() => expect(week.plan.isSuccess).toBe(true))

    // Two swaps and a push of the week are left pending, and Settings is
    // shown.
    act(() => week.swap.mutate(ORZO))
    act(() => week.swap.mutate(FISH))
    act(() => week.push.mutate())
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual(['swap pending', 'swap pending', 'push pending']))
    act(() => void navigate('/settings'))

    // The swaps settle: once neither is pending they are left behind, and
    // the push still pending is not.
    await act(async () => monday.send(jsonResponse(200, planBody({ 0: { id: 'r1', name: 'Fish tacos' } }))))
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual(['swap pending', 'swap success', 'push pending']))
    await act(async () => friday.send(jsonResponse(200, planBody({ 0: { id: 'r1', name: 'Fish tacos' }, 4: { id: 'q1', name: 'Lemon orzo', is_new: true } }))))
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual(['push pending']))

    // The push succeeds: the plan as synced is recorded, and the push is
    // left behind.
    const synced = { ...planBody(), status: 'synced', calendar_synced_at: '2026-10-08T06:00:00Z' }
    await act(async () => push.send(jsonResponse(200, { ...synced, ...GENERATED })))
    await waitFor(() => expect(weekMutations(queryClient, WEEK)).toEqual([]))
    expect(queryClient.getQueryData<Plan>(planKey(WEEK))).toMatchObject({ status: 'synced', calendar_synced_at: new Date('2026-10-08T06:00:00Z') })
  })
})
