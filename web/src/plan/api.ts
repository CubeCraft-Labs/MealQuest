import { useEffect } from 'react'
import { useMutation, useMutationState, useQuery, useQueryClient, type Mutation, type QueryClient } from '@tanstack/react-query'
import { ApiError, useApi, type ApiRead } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { METHODS, oneOf, photoId, type Method } from '../dinners/api.ts'
import { QUEST_LOG_KEY } from '../discover/api.ts'
import { noteGoogleFailure } from '../settings/calendarApi.ts'
import { supplyRunKey } from '../shopping/api.ts'

export const PLANS_PATH = '/api/v1/plans'

// planPath is the plan of the week starting on Monday weekStart.
export function planPath(weekStart: string): string {
  return `${PLANS_PATH}/${encodeURIComponent(weekStart)}`
}

// planKey is the query of the plan of the week starting on weekStart.
export function planKey(weekStart: string) {
  return ['plans', weekStart] as const
}

export const PLAN_STATUSES = ['draft', 'synced', 'changed'] as const

// PlanStatus is draft until Push to Calendar syncs the plan; an edit of a
// synced plan makes it changed.
export type PlanStatus = (typeof PLAN_STATUSES)[number]

// PlanDinner is a planned dinner: photo_id is its stored photo's id, from
// photo, minutes its minutes from prep to table and method how it is
// cooked, each when known.
export type PlanDinner = { id: string; name: string; photo_id: string | undefined; minutes: number | null; method: Method | null }

// PlanDay is one day of a plan: its household-local date, the dinner
// planned, whether that dinner is a Quest-log pick, and, for a crock-pot
// dinner of known minutes, the instant it must start by.
export type PlanDay = {
  date: string
  dinner: PlanDinner
  is_new: boolean
  start_by: string | null
}

// Plan is GET /api/v1/plans/{weekStart}: the week's seven days, Monday to
// Sunday, the AI's note on the draft, if any, and when it was last pushed to
// Google Calendar, if ever. cached says the service worker answered from its
// cache: the server may have changed the plan since.
export type Plan = {
  id: string
  week_start: string
  status: PlanStatus
  ai_note: string | null
  calendar_synced_at: Date | null
  days: PlanDay[]
  cached: boolean
}

// DRAFT_JOB_KIND is the kind of the job drafting a week's plan.
export const DRAFT_JOB_KIND = 'draft_week'

// DraftResult is the draft_week job's result: the week drafted and its plan.
export type DraftResult = { week_start: string; plan_id: string }

function parseDay(where: string, body: unknown): PlanDay {
  if (
    !isRecord(body) ||
    typeof body.date !== 'string' ||
    !isRecord(body.dinner) ||
    typeof body.dinner.id !== 'string' ||
    typeof body.dinner.name !== 'string' ||
    (body.dinner.minutes !== null && !Number.isInteger(body.dinner.minutes)) ||
    typeof body.is_new !== 'boolean' ||
    (body.start_by !== null && typeof body.start_by !== 'string')
  ) {
    throw new Error(`${where}: response has a day that is not a plan day`)
  }
  const dinner: PlanDinner = {
    id: body.dinner.id,
    name: body.dinner.name,
    photo_id: photoId(where, body.dinner.photo),
    minutes: body.dinner.minutes as number | null,
    method: body.dinner.method === null ? null : oneOf(where, 'method', METHODS, body.dinner.method),
  }
  return { date: body.date, dinner, is_new: body.is_new, start_by: body.start_by }
}

// syncedAt reads calendar_synced_at, an RFC 3339 instant or null.
function syncedAt(where: string, value: unknown): Date | null {
  if (value === null) return null
  const at = typeof value === 'string' ? new Date(value) : null
  if (at === null || Number.isNaN(at.getTime())) {
    throw new Error(`${where}: calendar_synced_at ${JSON.stringify(value)} is not an instant`)
  }
  return at
}

function parsePlan(where: string, weekStart: string, body: unknown, cached: boolean): Plan {
  if (
    !isRecord(body) ||
    typeof body.id !== 'string' ||
    body.week_start !== weekStart ||
    (body.ai_note !== null && typeof body.ai_note !== 'string') ||
    !Array.isArray(body.days) ||
    body.days.length !== 7
  ) {
    throw new Error(`${where}: response is not the plan of the week of ${weekStart}`)
  }
  return {
    id: body.id,
    week_start: weekStart,
    status: oneOf(where, 'status', PLAN_STATUSES, body.status),
    ai_note: body.ai_note,
    calendar_synced_at: syncedAt(where, body.calendar_synced_at),
    days: body.days.map((day) => parseDay(where, day)),
    cached,
  }
}

// SupplyRunError is the API error a Push to Calendar's Supply run carries
// when it was not generated.
export type SupplyRunError<C extends string> = { code: C; message: string; retryable: boolean }

// PushedSupplyRun says whether a Push to Calendar made the week's Supply
// run: generated when it did; archived when the week's Supply run is in the
// Supply archive and was left as it was; or failed when it was not made.
// A failed one's error is plan_changed when the plan was edited after it
// was pushed, and pushing again makes it, or supply_run_failed when it
// could not be made, and making the shopping list again does.
export type PushedSupplyRun =
  | { status: 'generated'; error: null }
  | { status: 'archived'; error: SupplyRunError<'archived'> }
  | { status: 'failed'; error: SupplyRunError<'plan_changed' | 'supply_run_failed'> }

// PushedPlan is the POST /api/v1/plans/{weekStart}/push-calendar answer:
// the plan as synced, and what became of the week's Supply run.
export type PushedPlan = { plan: Plan; supply_run: PushedSupplyRun }

const PUSHED_SUPPLY_RUN_STATUSES = ['generated', 'archived', 'failed'] as const
const FAILED_SUPPLY_RUN_CODES = ['plan_changed', 'supply_run_failed'] as const

function parseSupplyRunError<C extends string>(where: string, codes: readonly C[], body: unknown): SupplyRunError<C> {
  if (!isRecord(body) || typeof body.message !== 'string' || typeof body.retryable !== 'boolean') {
    throw new Error(`${where}: supply_run has an error that is not an API error`)
  }
  return { code: oneOf(where, 'supply_run.error.code', codes, body.code), message: body.message, retryable: body.retryable }
}

function parsePushedSupplyRun(where: string, body: unknown): PushedSupplyRun {
  if (!isRecord(body)) throw new Error(`${where}: response has no supply_run`)
  const status = oneOf(where, 'supply_run.status', PUSHED_SUPPLY_RUN_STATUSES, body.status)
  switch (status) {
    case 'generated':
      if (body.error !== null) throw new Error(`${where}: a generated supply_run has an error`)
      return { status, error: null }
    case 'archived':
      return { status, error: parseSupplyRunError(where, ['archived'] as const, body.error) }
    case 'failed':
      return { status, error: parseSupplyRunError(where, FAILED_SUPPLY_RUN_CODES, body.error) }
  }
}

function parsePushedPlan(where: string, weekStart: string, body: unknown): PushedPlan {
  return {
    plan: parsePlan(where, weekStart, body, false),
    supply_run: parsePushedSupplyRun(where, isRecord(body) ? body.supply_run : undefined),
  }
}

// parseDraftResult checks a draft_week job's result, which useJob passes on
// unread, is the draft of the week starting on weekStart.
export function parseDraftResult(where: string, weekStart: string, body: unknown): DraftResult {
  if (!isRecord(body) || body.week_start !== weekStart || typeof body.plan_id !== 'string') {
    throw new Error(`${where}: result is not the draft of the week of ${weekStart}`)
  }
  return { week_start: weekStart, plan_id: body.plan_id }
}

// usePlan is the plan of the week starting on Monday weekStart (GET
// /api/v1/plans/{weekStart}), or null when the week has none (404).
export function usePlan(weekStart: string) {
  const { apiRead } = useApi()
  return useQuery({
    queryKey: planKey(weekStart),
    queryFn: async (): Promise<Plan | null> => {
      const path = planPath(weekStart)
      let read: ApiRead<unknown>
      try {
        read = await apiRead<unknown>(path)
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null
        throw error
      }
      return parsePlan(`GET ${path}`, weekStart, read.body, read.cached)
    },
  })
}

// useStartDraft asks the AI to draft the week starting on weekStart (POST
// /api/v1/plans/{weekStart}/draft) and resolves with the id of the
// draft_week job, which the server answers with the draft already running
// when there is one. Drafting over a plan pushed to Calendar (synced or
// changed) needs confirm.
export function useStartDraft() {
  const { apiFetch } = useApi()
  return useMutation({
    mutationFn: async ({ weekStart, confirm }: { weekStart: string; confirm: boolean }): Promise<string> => {
      const path = `${planPath(weekStart)}/draft`
      const body = await apiFetch<unknown>(path, { method: 'POST', body: confirm ? { confirm: true } : {} })
      if (!isRecord(body) || typeof body.jobId !== 'string') throw new Error(`POST ${path}: response has no jobId`)
      return body.jobId
    },
  })
}

// Swap is one day's swap: the day, and the dinner chosen for it with whether
// it was chosen from the Quest log.
export type Swap = { date: string; dinner: PlanDinner; fromQuestLog: boolean }

// sameDay says whether two plan days are the same in every field.
function sameDay(a: PlanDay, b: PlanDay): boolean {
  return (
    a.date === b.date &&
    a.dinner.id === b.dinner.id &&
    a.dinner.name === b.dinner.name &&
    a.dinner.photo_id === b.dinner.photo_id &&
    a.dinner.minutes === b.dinner.minutes &&
    a.dinner.method === b.dinner.method &&
    a.is_new === b.is_new &&
    a.start_by === b.start_by
  )
}

// failed says whether a swap or push failed: a push whose Supply run was
// not made failed too, though Calendar has the plan.
function failed(mutation: Mutation): boolean {
  if (mutation.state.status === 'error') return true
  const data = mutation.state.data as PushedPlan | undefined
  return mutation.state.status === 'success' && weekMutation(mutation)?.kind === 'push' && data?.supply_run.status === 'failed'
}

// weekMutation is the kind and week of a swap or push of a week the mutation
// cache keeps for it, or null for any other mutation.
function weekMutation(mutation: Mutation): { kind: 'swap' | 'push'; week: string } | null {
  const key = mutation.options.mutationKey
  if (key === undefined || key.length !== 3 || key[0] !== 'plans' || (key[2] !== 'swap' && key[2] !== 'push')) return null
  return { kind: key[2], week: String(key[1]) }
}

// WeeksShown is what keepWeekMutations is told of the weeks shown: show
// marks a week shown until the function it returns is called.
type WeeksShown = { show: (weekStart: string) => () => void }

const weeksShown = new WeakMap<QueryClient, WeeksShown>()

// keepWeekMutations bounds the swaps and pushes the mutation cache of
// queryClient keeps, for as long as the query client lives, whatever is
// shown: it is installed on the client's own mutation cache once, the first
// time a week is shown, and is never removed, so a swap or push that settles
// after its Draft board, or the whole Plan, is gone is still collected.
//
// When a week is shown, the settled ones of every other week are removed,
// unless that week still has one pending: a pending one, and those an
// earlier pending one must see, stay across leaving its week and coming
// back. When one settles, the same is done, except that a week's latest,
// when it failed, stays until a week is next shown, so that coming straight
// back to it shows the failure. The cache holds at most the weeks shown's,
// those of weeks left pending, and one failure of each week whose last
// pending one settled since a week was last shown.
function keepWeekMutations(queryClient: QueryClient): WeeksShown {
  const kept = weeksShown.get(queryClient)
  if (kept !== undefined) return kept
  const mutations = queryClient.getMutationCache()
  // How many times each week is shown at once.
  const shown = new Map<string, number>()
  function sweep(settled: boolean) {
    // The mutations of each kind of each week not shown, in the order sent.
    const left = new Map<string, Mutation[]>()
    for (const mutation of mutations.getAll()) {
      const of = weekMutation(mutation)
      if (of === null || shown.has(of.week)) continue
      const id = `${of.kind} ${of.week}`
      left.set(id, [...(left.get(id) ?? []), mutation])
    }
    for (const week of left.values()) {
      if (week.some((mutation) => mutation.state.status === 'pending')) continue
      const latest = week.at(-1)
      for (const mutation of week) {
        if (settled && mutation === latest && failed(mutation)) continue
        mutations.remove(mutation)
      }
    }
  }
  mutations.subscribe((event) => {
    if (event.type === 'updated' && weekMutation(event.mutation) !== null && event.mutation.state.status !== 'pending') sweep(true)
  })
  const show = (weekStart: string) => {
    shown.set(weekStart, (shown.get(weekStart) ?? 0) + 1)
    sweep(false)
    return () => {
      const times = shown.get(weekStart)! - 1
      if (times === 0) shown.delete(weekStart)
      else shown.set(weekStart, times)
    }
  }
  const keeping = { show }
  weeksShown.set(queryClient, keeping)
  return keeping
}

// useWeekShown marks the week starting on weekStart shown while the
// component is mounted (keepWeekMutations).
function useWeekShown(weekStart: string) {
  const queryClient = useQueryClient()
  useEffect(() => keepWeekMutations(queryClient).show(weekStart), [queryClient, weekStart])
}

// swapKey keys the swaps of the week starting on weekStart in the mutation
// cache, where they outlive the Draft board that sent them.
function swapKey(weekStart: string) {
  return ['plans', weekStart, 'swap'] as const
}

// SwapDay is the swaps of one week, as the mutation cache holds them:
// whether one is pending, and the failure of the latest, if it failed.
export type SwapDay = {
  pending: boolean
  failed: { swap: Swap; error: Error } | null
  mutate: (chosen: Swap) => void
  dismiss: () => void
}

// useSwapDay swaps the dinner of one day of the week starting on weekStart
// (PUT /api/v1/plans/{weekStart}/days/{date}). The day shows the dinner at
// once, NEW when it came from the Quest log, with no start-by time until the
// server answers with the plan as swapped, which then replaces it. A refused
// swap puts the day back as it was, unless it has changed again meanwhile.
// However the swap ends, the Quest log is read again: the server leaves a
// dinner on the plan out of it.
//
// The week's swaps live in the mutation cache, not in the component, so
// leaving the week and coming back keeps them: pending and failed are read
// from there. Only the latest swap of the week speaks for it: the plan an
// earlier one is answered with is not written over a later one, and its
// failure is not the week's. dismiss leaves settled swaps behind; it is
// refused while one is pending, so a later swap is always in the cache for
// an earlier, pending one to see. Settled swaps of a week no longer shown
// are left behind too (keepWeekMutations).
export function useSwapDay(weekStart: string): SwapDay {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  useWeekShown(weekStart)
  const key = planKey(weekStart)
  const mutationKey = swapKey(weekStart)
  const mutations = queryClient.getMutationCache()
  // The week's swaps, in the order sent.
  const swaps = useMutationState({
    filters: { mutationKey },
    select: (mutation) => ({ status: mutation.state.status, swap: mutation.state.variables as Swap, error: mutation.state.error }),
  })
  const latest = swaps.at(-1)

  function mutate(chosen: Swap) {
    // isLatest says whether no swap of the week was sent after this one.
    const isLatest = () => mutations.findAll({ mutationKey }).every((other) => other.mutationId <= mutation.mutationId)
    const mutation = mutations.build<Plan, Error, Swap, { previous: PlanDay; swapped: PlanDay }>(queryClient, {
      mutationKey,
      // Kept until dismissed or settled in a week not shown
      // (keepWeekMutations): a later swap must stay in the cache while an
      // earlier one is pending.
      gcTime: Infinity,
      mutationFn: async ({ date, dinner }) => {
        const path = `${planPath(weekStart)}/days/${encodeURIComponent(date)}`
        const body = await apiFetch<unknown>(path, { method: 'PUT', body: { dinner_id: dinner.id } })
        return parsePlan(`PUT ${path}`, weekStart, body, false)
      },
      onMutate: async ({ date, dinner, fromQuestLog }) => {
        await queryClient.cancelQueries({ queryKey: key })
        const plan = queryClient.getQueryData<Plan | null>(key)
        const previous = plan?.days.find((day) => day.date === date)
        if (!plan || previous === undefined) throw new Error(`The week of ${weekStart} has no plan day ${date} to swap.`)
        const swapped: PlanDay = { date, dinner, is_new: fromQuestLog, start_by: null }
        queryClient.setQueryData<Plan>(key, { ...plan, days: plan.days.map((day) => (day.date === date ? swapped : day)) })
        return { previous, swapped }
      },
      onSuccess: (plan) => {
        if (isLatest()) queryClient.setQueryData<Plan>(key, plan)
      },
      // The day goes back only while it is still, field for field, the day
      // this swap wrote: a later swap or plan read that replaced it is left
      // alone.
      onError: (_error, _swap, context) => {
        if (context === undefined) return
        queryClient.setQueryData<Plan | null>(key, (plan) =>
          plan && { ...plan, days: plan.days.map((day) => (sameDay(day, context.swapped) ? context.previous : day)) },
        )
      },
      onSettled: () => queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY }),
    })
    // A failure is the mutation's state, which failed reports.
    mutation.execute(chosen).catch(() => undefined)
  }

  function dismiss() {
    const week = mutations.findAll({ mutationKey })
    if (week.some((mutation) => mutation.state.status === 'pending')) {
      throw new Error(`The week of ${weekStart} has a swap pending, which cannot be dismissed.`)
    }
    for (const mutation of week) mutations.remove(mutation)
  }

  return {
    pending: swaps.some((swap) => swap.status === 'pending'),
    failed: latest?.status === 'error' ? { swap: latest.swap, error: latest.error! } : null,
    mutate,
    dismiss,
  }
}

// pushKey keys the pushes of the week starting on weekStart in the mutation
// cache, where they outlive the Draft board that sent them.
function pushKey(weekStart: string) {
  return ['plans', weekStart, 'push'] as const
}

// PushToCalendar is the push of one week, as the mutation cache holds it:
// whether it is pending, why the latest failed, if it failed, and what
// became of the week's Supply run, if it succeeded.
export type PushToCalendar = {
  pending: boolean
  error: Error | null
  supplyRun: PushedSupplyRun | null
  mutate: () => void
  reset: () => void
}

// usePushToCalendar pushes the plan of the week starting on weekStart to the
// chosen Google calendar (POST /api/v1/plans/{weekStart}/push-calendar). The
// server answers with the plan as synced, which replaces the one shown, as a
// swap's answer does. A push refused because the plan changed meanwhile
// reads the plan again, whether or not the week is shown, and stays pending
// until that read lands: nothing is pushed again before the changed plan is
// the one shown. One Google refused the grant for reads the Google Calendar
// connection again, which is now reconnect required.
//
// Once pushed, the server makes the week's Supply run from the plan pushed
// and answers with its outcome, which supplyRun reports: a failed one is
// never a quiet success. The week's Supply run is read again, whatever the
// outcome, so the list shown is the server's. A Supply run not made because
// the plan was edited after it was pushed reads the plan again too, as a
// push refused for that does.
//
// The week's pushes live in the mutation cache, not in the component, as its
// swaps do, so leaving the week and coming back keeps one pending. reset
// leaves settled pushes behind; it is refused while one is pending. Settled
// pushes of a week no longer shown are left behind too
// (keepWeekMutations).
export function usePushToCalendar(weekStart: string): PushToCalendar {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  useWeekShown(weekStart)
  const key = planKey(weekStart)
  const mutationKey = pushKey(weekStart)
  const mutations = queryClient.getMutationCache()
  // The week's pushes, in the order sent.
  const pushes = useMutationState({
    filters: { mutationKey },
    select: (mutation) => ({ status: mutation.state.status, error: mutation.state.error, data: mutation.state.data as PushedPlan | undefined }),
  })
  const latest = pushes.at(-1)

  function mutate() {
    const mutation = mutations.build<PushedPlan, Error, void, unknown>(queryClient, {
      mutationKey,
      // Kept until reset or settled in a week not shown (keepWeekMutations):
      // a push pending in a week left must still be found when it is shown
      // again.
      gcTime: Infinity,
      mutationFn: async () => {
        const path = `${planPath(weekStart)}/push-calendar`
        return parsePushedPlan(`POST ${path}`, weekStart, await apiFetch<unknown>(path, { method: 'POST' }))
      },
      onSuccess: async ({ plan, supply_run }) => {
        queryClient.setQueryData<Plan>(key, plan)
        void queryClient.invalidateQueries({ queryKey: supplyRunKey(weekStart) })
        if (supply_run.error?.code === 'plan_changed') {
          await queryClient.invalidateQueries({ queryKey: key, refetchType: 'all' })
        }
      },
      onError: async (error) => {
        noteGoogleFailure(queryClient, error)
        if (error instanceof ApiError && error.code === 'plan_changed') {
          await queryClient.invalidateQueries({ queryKey: key, refetchType: 'all' })
        }
      },
    })
    // A failure is the mutation's state, which error reports.
    mutation.execute().catch(() => undefined)
  }

  function reset() {
    const week = mutations.findAll({ mutationKey })
    if (week.some((mutation) => mutation.state.status === 'pending')) {
      throw new Error(`The week of ${weekStart} has a push pending, which cannot be reset.`)
    }
    for (const mutation of week) mutations.remove(mutation)
  }

  return {
    pending: pushes.some((push) => push.status === 'pending'),
    error: latest?.status === 'error' ? latest.error! : null,
    supplyRun: latest?.status === 'success' ? latest.data!.supply_run : null,
    mutate,
    reset,
  }
}
