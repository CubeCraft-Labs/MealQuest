import { useCallback, useEffect } from 'react'
import {
  useMutation,
  useMutationState,
  useQuery,
  useQueryClient,
  type Mutation,
  type Query,
  type QueryClient,
  type QueryKey,
} from '@tanstack/react-query'
import { ApiError, useApi, type ApiRead } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { knownArchived, useArchived } from '../dinners/archived.ts'
import { MEAL_SOURCE_LABEL, MEAL_SOURCES, METHODS, oneOf, photoId, PLANNED_DINNER_STATUSES, type MealSource, type Method, type PlannedDinnerStatus } from '../dinners/api.ts'
import { QUEST_LOG_KEY } from '../discover/api.ts'
import { HOME_KEY, requireServerCurrentHome } from '../home/api.ts'
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

// PlanDinner is a planned dinner: status is its status now — archived once
// it was removed from Regulars after the plan was made, when it has no
// detail to open — photo_id is its stored photo's id, from photo, minutes
// its minutes from prep to table and method how it is cooked, each when
// known.
export type PlanDinner = {
  id: string
  name: string
  status: PlannedDinnerStatus
  photo_id: string | undefined
  minutes: number | null
  method: Method | null
  meal_source: MealSource
}

// PlanDay is one day of a plan: its household-local date, the dinner
// planned or a one-off manual label (never both), whether that dinner is a
// Quest-log pick, and, for a crock-pot dinner of known minutes, the instant
// it must start by. meal_source is the day's own evening: cook for a
// dinner, a manual night or a cleared night, and delivery or dining_out
// for that evening, which then has neither a dinner nor a label. A cleared
// night has neither a dinner nor a label.
export type PlanDay = {
  date: string
  dinner: PlanDinner | null
  manual_label: string | null
  meal_source: MealSource
  is_new: boolean
  start_by: string | null
}

// planDayName is the name a plan day shows: Delivery or Dining out for
// that evening, otherwise the dinner, the one-off label, or "Nothing
// planned" when the night is cleared.
export function planDayName(day: PlanDay): string {
  if (day.meal_source === 'delivery' || day.meal_source === 'dining_out') return MEAL_SOURCE_LABEL[day.meal_source]
  if (day.dinner !== null) return day.dinner.name
  if (day.manual_label !== null) return day.manual_label
  return 'Nothing planned'
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
    typeof body.is_new !== 'boolean' ||
    (body.start_by !== null && typeof body.start_by !== 'string') ||
    (body.manual_label !== undefined && body.manual_label !== null && typeof body.manual_label !== 'string')
  ) {
    throw new Error(`${where}: response has a day that is not a plan day`)
  }
  const manual = typeof body.manual_label === 'string' ? body.manual_label : null
  if (body.dinner === null || body.dinner === undefined) {
    if (body.dinner === undefined) throw new Error(`${where}: response has a day that is not a plan day`)
    if (body.is_new || body.start_by !== null) {
      throw new Error(`${where}: a night with no dinner is not a quest and has no start time`)
    }
    if (manual !== null && manual.trim() === '') throw new Error(`${where}: a manual night has an empty name`)
    const mealSource = oneOf(where, 'meal_source', MEAL_SOURCES, body.meal_source)
    if (mealSource !== 'cook') {
      if (manual !== null) throw new Error(`${where}: a ${mealSource} evening also has a manual label`)
      return { date: body.date, dinner: null, manual_label: null, meal_source: mealSource, is_new: false, start_by: null }
    }
    return { date: body.date, dinner: null, manual_label: manual, meal_source: 'cook', is_new: false, start_by: null }
  }
  if (
    !isRecord(body.dinner) ||
    typeof body.dinner.id !== 'string' ||
    typeof body.dinner.name !== 'string' ||
    (body.dinner.minutes !== null && !Number.isInteger(body.dinner.minutes))
  ) {
    throw new Error(`${where}: response has a day that is not a plan day`)
  }
  if (manual !== null) throw new Error(`${where}: a plan day has both a dinner and a manual label`)
  // Dinner status is read before the day's meal source so a plan cached
  // before dinners carried a status still fails on that, as it did.
  const dinner: PlanDinner = {
    id: body.dinner.id,
    name: body.dinner.name,
    status: oneOf(where, 'dinner status', PLANNED_DINNER_STATUSES, body.dinner.status),
    photo_id: photoId(where, body.dinner.photo),
    minutes: body.dinner.minutes as number | null,
    method: body.dinner.method === null ? null : oneOf(where, 'method', METHODS, body.dinner.method),
    meal_source: oneOf(where, 'meal_source', MEAL_SOURCES, body.dinner.meal_source),
  }
  const mealSource = oneOf(where, 'meal_source', MEAL_SOURCES, body.meal_source)
  if (mealSource !== 'cook') throw new Error(`${where}: a night with a dinner is not a ${mealSource} evening`)
  return { date: body.date, dinner, manual_label: null, meal_source: 'cook', is_new: body.is_new, start_by: body.start_by as string | null }
}

// RFC3339 is an RFC 3339 instant as the server writes it: a full date and
// time, to the second or finer, and its offset, Z or ±hh:mm. A date alone,
// or a time with no offset, which Date would read in this device's zone, is
// not one.
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/

// showArchived is plan with each dinner the server archived (archived)
// shown archived — removed from Regulars, so it has no detail to open —
// whatever the read said; a plan with none is plan itself.
export function showArchived(plan: Plan | null, archived: ReadonlySet<string>): Plan | null {
  if (plan === null || !plan.days.some((day) => day.dinner !== null && archived.has(day.dinner.id) && day.dinner.status !== 'archived')) return plan
  return {
    ...plan,
    days: plan.days.map((day) =>
      day.dinner !== null && archived.has(day.dinner.id) ? { ...day, dinner: { ...day.dinner, status: 'archived' } } : day,
    ),
  }
}

// syncedAt reads calendar_synced_at, an RFC 3339 instant or null.
function syncedAt(where: string, value: unknown): Date | null {
  if (value === null) return null
  const at = typeof value === 'string' && RFC3339.test(value) ? new Date(value) : null
  if (at === null || Number.isNaN(at.getTime())) {
    throw new Error(`${where}: calendar_synced_at ${JSON.stringify(value)} is not an instant`)
  }
  return at
}

// parsePlan checks a body the plans API sends is the plan of the week
// starting on weekStart; cached says the service worker answered it.
export function parsePlan(where: string, weekStart: string, body: unknown, cached: boolean): Plan {
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
// /api/v1/plans/{weekStart}), or null when the week has none (404), with
// each dinner removed on this device shown archived
// (archived.ts).
export function usePlan(weekStart: string) {
  const { apiRead } = useApi()
  const archived = useArchived()
  const select = useCallback((plan: Plan | null) => showArchived(plan, knownArchived(archived)), [archived])
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
    select,
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

// EveningSource is a plan day's non-cook evening. Cook is a dinner, a
// manual night or a cleared night, not one of these.
export type EveningSource = Exclude<MealSource, 'cook'>

// Swap is one day's edit: a dinner chosen from the Quest log or the
// Regulars, a one-off manual label, a Delivery or Dining-out evening, or
// clearing the night.
export type Swap =
  | { date: string; dinner: PlanDinner; fromQuestLog: boolean }
  | { date: string; manual_label: string }
  | { date: string; meal_source: EveningSource }
  | { date: string; clear: true }

function swapBody(swap: Swap): { dinner_id: string } | { manual_label: string } | { meal_source: EveningSource } | { clear: true } {
  if ('clear' in swap) return { clear: true }
  if ('manual_label' in swap) return { manual_label: swap.manual_label }
  if ('meal_source' in swap) return { meal_source: swap.meal_source }
  return { dinner_id: swap.dinner.id }
}

function swappedDay(swap: Swap): PlanDay {
  if ('clear' in swap) return { date: swap.date, dinner: null, manual_label: null, meal_source: 'cook', is_new: false, start_by: null }
  if ('manual_label' in swap) return { date: swap.date, dinner: null, manual_label: swap.manual_label, meal_source: 'cook', is_new: false, start_by: null }
  if ('meal_source' in swap) {
    return { date: swap.date, dinner: null, manual_label: null, meal_source: swap.meal_source, is_new: false, start_by: null }
  }
  return { date: swap.date, dinner: swap.dinner, manual_label: null, meal_source: 'cook', is_new: swap.fromQuestLog, start_by: null }
}

// sameDinner says whether two planned dinners are the same in every field.
function sameDinner(a: PlanDinner | null, b: PlanDinner | null): boolean {
  if (a === null || b === null) return a === b
  return (
    a.id === b.id &&
    a.name === b.name &&
    a.status === b.status &&
    a.photo_id === b.photo_id &&
    a.minutes === b.minutes &&
    a.method === b.method &&
    a.meal_source === b.meal_source
  )
}

// sameDay says whether two plan days are the same in every field.
function sameDay(a: PlanDay, b: PlanDay): boolean {
  return (
    a.date === b.date &&
    sameDinner(a.dinner, b.dinner) &&
    a.manual_label === b.manual_label &&
    a.meal_source === b.meal_source &&
    a.is_new === b.is_new &&
    a.start_by === b.start_by
  )
}

// failed says whether a swap or push failed: a push whose Supply run was
// not made failed too, though Calendar has the plan.
function failed(mutation: Mutation): boolean {
  return mutation.state.status === 'error' || unmade(mutation)
}

// unmade says whether a push succeeded but its Supply run was not made: the
// week is on Calendar with no shopping list until one is made.
function unmade(mutation: Mutation): boolean {
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
// A swap or push pending is kept, with every other of its kind and week:
// an earlier pending one must see those sent after it, across leaving its
// week and coming back. Once none of a kind and week is pending, only the
// latest is needed, and the rest are removed, whenever a week is shown or
// one settles. The latest itself stays while its week is shown. In a week
// not shown it is removed, unless:
//
//   - it is a failed swap: it stays, whatever is shown, until the week's
//     board leaves it behind (useWeekSwap, Rebuild week, Push to Calendar),
//     so coming back to the week shows the failure;
//   - it is a push whose Supply run was not made: the week has no shopping
//     list until one is made, which settles the push, or a push sent since
//     replaces it, and making it may still be under way when the week is
//     left;
//   - it is a failed push that settled after its week was left: it stays
//     until a week is next shown, so coming straight back shows it.
//
// So once none of its kind is pending, a week keeps at most one settled
// swap and one settled push, and a week left keeps none but those above:
// settled swaps and pushes never accumulate.
function keepWeekMutations(queryClient: QueryClient): WeeksShown {
  const kept = weeksShown.get(queryClient)
  if (kept !== undefined) return kept
  const mutations = queryClient.getMutationCache()
  // How many times each week is shown at once.
  const shown = new Map<string, number>()
  // keep says whether the latest of its kind and week, none of which is
  // pending, stays; settled says a mutation settling swept, not a week
  // shown.
  function keep(mutation: Mutation, of: { kind: 'swap' | 'push'; week: string }, settled: boolean): boolean {
    if (shown.has(of.week)) return true
    if (of.kind === 'swap') return failed(mutation)
    return unmade(mutation) || (settled && failed(mutation))
  }
  function sweep(settled: boolean) {
    // The mutations of each kind of each week, in the order sent.
    const sent = new Map<string, { of: { kind: 'swap' | 'push'; week: string }; week: Mutation[] }>()
    for (const mutation of mutations.getAll()) {
      const of = weekMutation(mutation)
      if (of === null) continue
      const id = `${of.kind} ${of.week}`
      sent.set(id, { of, week: [...(sent.get(id)?.week ?? []), mutation] })
    }
    for (const { of, week } of sent.values()) {
      if (week.some((mutation) => mutation.state.status === 'pending')) continue
      const latest = week.at(-1)
      for (const mutation of week) {
        if (mutation === latest && keep(mutation, of, settled)) continue
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

// swapWrites counts, for each plan query, its updates that were a swap's
// own optimistic write or rollback, not a plan read from the server.
const swapWrites = new WeakMap<Query, number>()

// serverReads is how many plans read from the server have landed in the
// plan query: its updates, less the swaps' own writes. A plan read lands
// whether or not it differs from the plan shown.
function serverReads(query: Query): number {
  return query.state.dataUpdateCount - (swapWrites.get(query) ?? 0)
}

// writeSwap updates the plan query as a swap's own write, which serverReads
// does not count.
function writeSwap(queryClient: QueryClient, query: Query, update: (plan: Plan | null | undefined) => Plan | null | undefined) {
  const before = query.state.dataUpdateCount
  queryClient.setQueryData<Plan | null>(query.queryKey, update)
  swapWrites.set(query, (swapWrites.get(query) ?? 0) + query.state.dataUpdateCount - before)
}

// planQuery is the plan query keyed key, if the query cache has it.
function planQuery(queryClient: QueryClient, key: QueryKey): Query | undefined {
  return queryClient.getQueryCache().find({ queryKey: key, exact: true })
}

// swapKey keys the swaps of the week starting on weekStart in the mutation
// cache, where they outlive the Draft board that sent them.
function swapKey(weekStart: string) {
  return ['plans', weekStart, 'swap'] as const
}

// SwapDay is the swaps of one week, as the mutation cache holds them:
// whether one is pending, the latest if it succeeded, and the failure of the
// latest, if it failed.
export type SwapDay = {
  pending: boolean
  succeeded: Swap | null
  failed: { swap: Swap; error: Error } | null
  mutate: (chosen: Swap) => void
  dismiss: () => void
}

// useSwapDay swaps the dinner of one day of the week starting on weekStart
// (PUT /api/v1/plans/{weekStart}/days/{date}). The day shows the dinner at
// once, NEW when it came from the Quest log, with no start-by time until the
// server answers with the plan as swapped, which then replaces it. A refused
// swap puts the day back as it was, unless it has changed again meanwhile:
// another swap wrote over it, or any plan read from the server landed
// since, even one with the very day the swap wrote. A swap refused because
// the dinner is already on another day of the week (409
// dinner_already_planned) reads the plan again, so the household chooses
// again from the plan as it is. However the swap ends, the Quest log is
// read again: the server leaves a dinner on the plan out of it.
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
    const mutation = mutations.build<Plan, Error, Swap, { previous: PlanDay; swapped: PlanDay; query: Query; reads: number }>(queryClient, {
      mutationKey,
      // Kept until dismissed or settled in a week not shown
      // (keepWeekMutations): a later swap must stay in the cache while an
      // earlier one is pending.
      gcTime: Infinity,
      mutationFn: async (swap) => {
        const path = `${planPath(weekStart)}/days/${encodeURIComponent(swap.date)}`
        const body = await apiFetch<unknown>(path, { method: 'PUT', body: swapBody(swap) })
        return parsePlan(`PUT ${path}`, weekStart, body, false)
      },
      onMutate: async (swap) => {
        await queryClient.cancelQueries({ queryKey: key })
        const query = planQuery(queryClient, key)
        const plan = query?.state.data as Plan | null | undefined
        const previous = plan?.days.find((day) => day.date === swap.date)
        if (query === undefined || !plan || previous === undefined) {
          throw new Error(`The week of ${weekStart} has no plan day ${swap.date} to swap.`)
        }
        const swapped = swappedDay(swap)
        writeSwap(queryClient, query, () => ({ ...plan, days: plan.days.map((day) => (day.date === swap.date ? swapped : day)) }))
        return { previous, swapped, query, reads: serverReads(query) }
      },
      onSuccess: (plan) => {
        if (!isLatest()) return
        queryClient.setQueryData<Plan>(key, plan)
        // The week's Supply run is made from the plan's dinners. A swap,
        // including a Delivery or Dining-out evening, changes which nights
        // are shopped, so the list is read again.
        void queryClient.invalidateQueries({ queryKey: supplyRunKey(weekStart) })
        // Tonight on Home is today's evening (Delivery, Dining out, another
        // dinner, or a cleared night). Reset the Home query rather than
        // removing or invalidating it. Removal drops the cache entry but
        // leaves a Home that mounted during this edit — while the PUT was
        // still pending — showing that observer's previous Tonight, and
        // starts no fresh read. Invalidation would refetch while that
        // previous Tonight stayed in the cache, and Home renders it while
        // the new read is pending. Reset clears the observer's data and
        // refetches a Home that is mounted, so the dinner this edit replaced
        // cannot stay on screen; a Home that is not mounted has no data
        // until its next read. That read has to be the server's: the service
        // worker's NetworkFirst cache of /home can answer the GET with the
        // previous Tonight when the network fails or times out after this
        // PUT. requireServerCurrentHome sends it network-only (apiCheck) and
        // rejects X-MealQuest-Cache-Fallback, so that copy never becomes
        // Tonight. The 30-second staleTime and useHome's midnight invalidate
        // are unchanged: a return that did not edit a day still shows the
        // cached Tonight, and a Home left open still refreshes at local
        // midnight without clearing the card.
        requireServerCurrentHome(queryClient)
        void queryClient.resetQueries({ queryKey: HOME_KEY })
      },
      // The day goes back only while it is still, field for field, the day
      // this swap wrote, and no plan read from the server has landed since
      // (serverReads): a later swap that replaced it, or a read that did,
      // even with the same day, is left alone.
      onError: (error, _swap, context) => {
        if (context !== undefined && planQuery(queryClient, key) === context.query && serverReads(context.query) === context.reads) {
          writeSwap(queryClient, context.query, (plan) =>
            plan && { ...plan, days: plan.days.map((day) => (sameDay(day, context.swapped) ? context.previous : day)) },
          )
        }
        if (error instanceof ApiError && error.code === 'dinner_already_planned') {
          void queryClient.invalidateQueries({ queryKey: key })
        }
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
    succeeded: latest?.status === 'success' ? latest.swap : null,
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
// leaves settled pushes behind; it is refused while one is pending. A push
// whose Supply run was not made is left behind once the list is made
// (settleUnmadePushes). Settled pushes of a week no longer shown are left
// behind too, but for the latest when its Supply run was not made
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
    select: (mutation) => ({
      mutationId: mutation.mutationId,
      status: mutation.state.status,
      error: mutation.state.error,
      data: mutation.state.data as PushedPlan | undefined,
    }),
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

// WeekSwap is the one way a day of a week is swapped, wherever the swap is
// chosen: the week's swaps and pushes, whether a swap is refused now, and
// swapIn to send one.
export type WeekSwap = {
  swap: SwapDay
  push: PushToCalendar
  refused: boolean
  swapIn: (chosen: Swap) => void
}

// useWeekSwap swaps a day of the week starting on weekStart, from the Draft
// board or a dinner's Add to this week. One swap of the week is sent at a
// time, even across leaving the week and coming back: the plan the server
// answers it with must not overwrite another still pending. Nor is one sent
// while the week is being pushed to Calendar: refused says so, and swapIn
// then throws. A swap leaves the week's settled swaps behind, and its
// pushes: what a push said of Calendar and the Supply run was said of the
// plan before the swap, not the one swapped.
export function useWeekSwap(weekStart: string): WeekSwap {
  const swap = useSwapDay(weekStart)
  const push = usePushToCalendar(weekStart)
  const refused = swap.pending || push.pending
  function swapIn(chosen: Swap) {
    if (refused) throw new Error(`The week of ${weekStart} has a swap or push pending, so ${chosen.date} cannot be swapped.`)
    swap.dismiss()
    push.reset()
    swap.mutate(chosen)
  }
  return { swap, push, refused, swapIn }
}

// settleUnmadePushes is called as the Supply run of the week starting on
// weekStart starts being made, from the Plan or the Supply run screen
// (useGenerateSupplyRun). The function it answers with is called once the
// list is made: it leaves behind the week's pushes sent before whose Supply
// run was not made, so the Plan does not say again that it is unmade, even
// if the Plan was left meanwhile. A push sent since is kept, and a list not
// made leaves them as they are.
export function settleUnmadePushes(queryClient: QueryClient, weekStart: string): () => void {
  const mutations = queryClient.getMutationCache()
  const mutationKey = pushKey(weekStart)
  const sent = new Set(mutations.findAll({ mutationKey }))
  return () => {
    for (const mutation of mutations.findAll({ mutationKey })) {
      if (sent.has(mutation) && unmade(mutation)) mutations.remove(mutation)
    }
  }
}
