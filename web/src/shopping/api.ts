import {
  MutationObserver,
  useMutation,
  useMutationState,
  useQuery,
  useQueryClient,
  type MutationObserverOptions,
  type QueryClient,
} from '@tanstack/react-query'
import { useCallback, useEffect, useReducer, useState, useSyncExternalStore } from 'react'
import { ApiError, useApi, type ApiClient, type ApiRead } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { meQueryKey } from '../AuthBootstrap.tsx'
import { archivedNow, knownArchived, onArchived, useArchived } from '../dinners/archived.ts'
import { MEAL_SOURCES, oneOf, PLANNED_DINNER_STATUSES, type MealSource, type PlannedDinnerStatus } from '../dinners/api.ts'
import { settleUnmadePushes } from '../plan/api.ts'
import type { NetworkStatus } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS_KEY, type HouseholdSettings } from '../settings/api.ts'
import {
  dequeue,
  enqueue,
  keep,
  markWillSync,
  queued,
  QUEUE_CAUSE,
  runCause,
  SAVED_KEY,
  saveRun,
  savedRun,
  savedWeeks,
  saveSchedule,
  SCHEDULE_CAUSE,
  synced,
  toggleCause,
  WEEKS_CAUSE,
  willSync,
} from './offline.ts'

export const SHOPPING_PATH = '/api/v1/shopping'

// WEEK_PARAM names the search parameter choosing the week the Supply run
// screen shows, its Monday as YYYY-MM-DD: /plan/shopping?week=2026-09-28.
export const WEEK_PARAM = 'week'

// supplyRunScreen is the Supply run screen on the week starting on Monday
// weekStart.
export function supplyRunScreen(weekStart: string): string {
  return `/plan/shopping?${new URLSearchParams({ [WEEK_PARAM]: weekStart })}`
}

// supplyRunPath is the Supply run of the week starting on Monday weekStart.
export function supplyRunPath(weekStart: string): string {
  return `${SHOPPING_PATH}/${encodeURIComponent(weekStart)}`
}

// supplyRunKey is the query of the Supply run of the week starting on
// weekStart.
export function supplyRunKey(weekStart: string) {
  return ['shopping', weekStart] as const
}

export const SUPPLY_RUN_STATUSES = ['current', 'archived'] as const

// SupplyRunStatus is current until the week ends and the Supply run is
// frozen into the Supply archive: archived, after which nothing on it
// changes.
export type SupplyRunStatus = (typeof SUPPLY_RUN_STATUSES)[number]

// SupplyRunItem is one ingredient: qty is null when the recipe gives none,
// and needed is false once it is marked "have it".
export type SupplyRunItem = { id: string; name: string; qty: string | null; needed: boolean }

// SupplyRunGroup is the items of one plan day's dinner: date is the plan
// day, household-local YYYY-MM-DD. The dinner's status is null while it is
// not known: the Supply run was saved on this device by a build from before
// dinners carried their status (see savedDinnerStatus), and has not been read
// again since.
export type SupplyRunGroup = {
  dinner: { id: string; name: string; status: PlannedDinnerStatus | null; meal_source: MealSource | null }
  date: string
  items: SupplyRunItem[]
}

// Counts is how many of a Supply run's items are still to buy (needed) of
// all of them (total).
export type Counts = { needed_count: number; total_count: number }

// SupplyRun is GET /api/v1/shopping/{weekStart}: label is the week label,
// "Week 41 · Oct 5 – 11", title the AI's title once written,
// sent_to_chat_at when it was sent to Google Chat, if it was, and
// chat_send_unconfirmed_at when a send whose outcome is not known began,
// if one did: it may or may not have reached Google Chat. cached says it is
// not the server's answer just now — the service worker answered from its
// cache, or it is the one saved on this device — so the server may have
// changed it since.
export type SupplyRun = Counts & {
  weekStart: string
  label: string
  title: string | null
  status: SupplyRunStatus
  sent_to_chat_at: Date | null
  chat_send_unconfirmed_at: Date | null
  groups: SupplyRunGroup[]
  cached: boolean
}

function count(where: string, field: string, value: unknown): number {
  if (!Number.isInteger(value) || (value as number) < 0) throw new Error(`${where}: ${field} ${JSON.stringify(value)} is not a count`)
  return value as number
}

function parseCounts(where: string, body: Record<string, unknown>): Counts {
  const counts = { needed_count: count(where, 'needed_count', body.needed_count), total_count: count(where, 'total_count', body.total_count) }
  if (counts.needed_count > counts.total_count) throw new Error(`${where}: needed_count is more than total_count`)
  return counts
}

function parseItem(where: string, body: unknown): SupplyRunItem {
  if (
    !isRecord(body) ||
    typeof body.id !== 'string' ||
    typeof body.name !== 'string' ||
    (body.qty !== null && typeof body.qty !== 'string') ||
    typeof body.needed !== 'boolean'
  ) {
    throw new Error(`${where}: response has an item that is not a Supply run item`)
  }
  return { id: body.id, name: body.name, qty: body.qty, needed: body.needed }
}

// DinnerStatus reads a group's dinner's status.
type DinnerStatus = (where: string, value: unknown) => PlannedDinnerStatus | null

// serverDinnerStatus is a dinner's status as the server sends it: always one
// of PLANNED_DINNER_STATUSES.
const serverDinnerStatus: DinnerStatus = (where, value) => oneOf(where, 'dinner status', PLANNED_DINNER_STATUSES, value)

// savedDinnerStatus is a dinner's status in a Supply run saved on this
// device. A Supply run saved by a build from before dinners carried their
// status has none (undefined): its status is not known (null), and the
// Supply run, saved again before it is read again — a toggle's answer is
// written over it — keeps it null. Any other status is the server's.
const savedDinnerStatus: DinnerStatus = (where, value) =>
  value === undefined || value === null ? null : serverDinnerStatus(where, value)

// A Supply run saved by an earlier build may not name a meal source. One
// not known offers no recipe. A read from the server must name one.
function mealSource(where: string, value: unknown, allowMissing: boolean): MealSource | null {
  if ((value === undefined || value === null) && allowMissing) return null
  return oneOf(where, 'meal_source', MEAL_SOURCES, value)
}

function parseGroup(where: string, body: unknown, dinnerStatus: DinnerStatus, allowMissingSource: boolean): SupplyRunGroup {
  if (
    !isRecord(body) ||
    !isRecord(body.dinner) ||
    typeof body.dinner.id !== 'string' ||
    typeof body.dinner.name !== 'string' ||
    typeof body.date !== 'string' ||
    !Array.isArray(body.items)
  ) {
    throw new Error(`${where}: response has a group that is not a dinner's items`)
  }
  return {
    dinner: {
      id: body.dinner.id,
      name: body.dinner.name,
      status: dinnerStatus(where, body.dinner.status),
      meal_source: mealSource(where, body.dinner.meal_source, allowMissingSource),
    },
    date: body.date,
    items: body.items.map((item) => parseItem(where, item)),
  }
}

// instant reads field, an RFC 3339 instant or null.
function instant(where: string, field: string, value: unknown): Date | null {
  if (value === null) return null
  const at = typeof value === 'string' ? new Date(value) : null
  if (at === null || Number.isNaN(at.getTime())) {
    throw new Error(`${where}: ${field} ${JSON.stringify(value)} is not an instant`)
  }
  return at
}

// parseSupplyRun reads a Supply run the server sent; cached says the service
// worker answered it.
export function parseSupplyRun(where: string, weekStart: string, body: unknown, cached: boolean): SupplyRun {
  return parseRun(where, weekStart, body, serverDinnerStatus, false, cached)
}

function parseRun(
  where: string,
  weekStart: string,
  body: unknown,
  dinnerStatus: DinnerStatus,
  allowMissingSource: boolean,
  cached: boolean,
): SupplyRun {
  if (
    !isRecord(body) ||
    body.weekStart !== weekStart ||
    typeof body.label !== 'string' ||
    (body.title !== null && typeof body.title !== 'string') ||
    !Array.isArray(body.groups)
  ) {
    throw new Error(`${where}: response is not the Supply run of the week of ${weekStart}`)
  }
  return {
    weekStart,
    label: body.label,
    title: body.title,
    status: oneOf(where, 'status', SUPPLY_RUN_STATUSES, body.status),
    sent_to_chat_at: instant(where, 'sent_to_chat_at', body.sent_to_chat_at),
    chat_send_unconfirmed_at: instant(where, 'chat_send_unconfirmed_at', body.chat_send_unconfirmed_at),
    ...parseCounts(where, body),
    groups: body.groups.map((group) => parseGroup(where, group, dinnerStatus, allowMissingSource)),
    cached,
  }
}

// savedSupplyRun is the week's Supply run saved on this device, or undefined:
// always cached, whatever the cached it was saved with, as it is not the
// server's answer now. One saved by an earlier build may not know its
// dinners' statuses (savedDinnerStatus).
function savedSupplyRun(weekStart: string): SupplyRun | undefined {
  const saved = savedRun(weekStart)
  return saved === undefined ? undefined : parseRun(`Saved ${SAVED_KEY}`, weekStart, saved, savedDinnerStatus, true, true)
}

// showArchived is run with each dinner the server archived (archived) shown
// archived — removed from Regulars, so it has no detail to open — whatever
// the read, or the Supply run saved on this device, said; a Supply run with
// none is run itself.
export function showArchived(run: SupplyRun | null, archived: ReadonlySet<string>): SupplyRun | null {
  if (run === null || !run.groups.some((group) => archived.has(group.dinner.id) && group.dinner.status !== 'archived')) return run
  return {
    ...run,
    groups: run.groups.map((group) => (archived.has(group.dinner.id) ? { ...group, dinner: { ...group.dinner, status: 'archived' } } : group)),
  }
}

// useSupplyRun is the Supply run of the week starting on Monday weekStart
// (GET /api/v1/shopping/{weekStart}), or null when the week has none (404).
// It starts from the Supply run saved on this device, if there is one, so
// the list opens offline; the saved one is always read again. It is read
// from this device once, not on every render: while the query has no data
// its initial data is asked for again on each render, and a failed read
// reports a problem, which renders this again. Each dinner removed on this
// device shows archived (archived.ts), on the saved one as on every read.
export function useSupplyRun(weekStart: string) {
  const { apiRead } = useApi()
  const [saved] = useState(() => keep(runCause(weekStart), () => savedSupplyRun(weekStart)))
  const archived = useArchived()
  return useQuery({
    queryKey: supplyRunKey(weekStart),
    initialData: saved,
    initialDataUpdatedAt: 0,
    queryFn: async (): Promise<SupplyRun | null> => {
      const path = supplyRunPath(weekStart)
      let read: ApiRead<unknown>
      try {
        read = await apiRead<unknown>(path)
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null
        throw error
      }
      return parseSupplyRun(`GET ${path}`, weekStart, read.body, read.cached)
    },
    select: useCallback((run: SupplyRun | null) => showArchived(run, knownArchived(archived)), [archived]),
  })
}

// useGenerateSupplyRun makes the week's Supply run from its plan (POST
// /api/v1/shopping/{weekStart}/generate); the Supply run it answers with is
// the one shown. Once made, from whichever screen, a push of the week whose
// Supply run was not made is settled (settleUnmadePushes).
export function useGenerateSupplyRun(weekStart: string) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    onMutate: () => settleUnmadePushes(queryClient, weekStart),
    mutationFn: async (): Promise<SupplyRun> => {
      const path = `${supplyRunPath(weekStart)}/generate`
      return parseSupplyRun(`POST ${path}`, weekStart, await apiFetch<unknown>(path, { method: 'POST' }), false)
    },
    onSuccess: (run, _, settle) => {
      queryClient.setQueryData<SupplyRun | null>(supplyRunKey(weekStart), run)
      settle()
    },
  })
}

// useSendToChat posts the week's Supply run to Google Chat (POST
// /api/v1/shopping/{weekStart}/send-chat); the Supply run it answers with,
// sent_to_chat_at recorded, is the one shown. A week already sent, or with
// a send unconfirmed, is posted again only with resend: without it the
// server answers with the Supply run as it is. A send that fails re-reads
// the Supply run, so a send left unconfirmed shows as such.
export function useSendToChat(weekStart: string) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (resend: boolean): Promise<SupplyRun> => {
      const path = `${supplyRunPath(weekStart)}/send-chat`
      return parseSupplyRun(`POST ${path}`, weekStart, await apiFetch<unknown>(path, { method: 'POST', body: { resend } }), false)
    },
    onSuccess: (run) => {
      queryClient.setQueryData<SupplyRun | null>(supplyRunKey(weekStart), run)
    },
    onError: () => queryClient.invalidateQueries({ queryKey: supplyRunKey(weekStart) }),
  })
}

// Toggle is one item marked to buy (needed) or "have it" (not needed).
export type Toggle = { id: string; needed: boolean }

// KeyedToggle is a toggle as sent: key tells it apart from any other toggle
// of the same item, here and in the queue saved on this device.
type KeyedToggle = Toggle & { key: string }

// toggleKey keys the toggles of the week starting on weekStart.
function toggleKey(weekStart: string) {
  return ['shopping', weekStart, 'toggle'] as const
}

// TOGGLE_SCOPE is the one mutation scope of every toggle of every week, made
// here or replayed from the queue saved on this device.
const TOGGLE_SCOPE = 'shopping toggle'

// Toggles is the toggles of one week's Supply run: shown, the Supply run
// with those still pending laid over it, the items whose toggles will sync
// once the server can be reached, the latest failed toggle no later toggle
// of its item has superseded, and toggle to make another.
export type Toggles = {
  shown: SupplyRun
  willSync: ReadonlySet<string>
  failed: { toggle: Toggle; error: Error } | null
  toggle: (toggle: Toggle) => void
}

// TOGGLE_DEADLINE is how long, in milliseconds, one try of a toggle — its
// GET /api/v1/me, its PATCH and the /me refresh a csrf 403 to it makes — may
// take before it counts as the server not answering. Only toggles have a
// deadline: the API client sets none.
export const TOGGLE_DEADLINE = 10_000

// RETRY_FIRST and RETRY_CEILING bound, in milliseconds, how long a toggle
// waits while the server cannot be reached before it is tried again anyway:
// the first wait, doubled after each failed try up to the ceiling.
export const RETRY_FIRST = 1_000
export const RETRY_CEILING = 30_000

// reachable resolves once network says the server can be reached — at once
// when it says so now — or after delay milliseconds, whichever is first. The
// server coming back says nothing by itself, and the browser sends no online
// event when it never thought it was offline, so the toggle is tried again
// after delay whatever network says.
function reachable(network: NetworkStatus, delay: number): Promise<void> {
  if (!network.offline()) return Promise.resolve()
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(timer)
      stop()
      resolve()
    }
    const timer = setTimeout(done, delay)
    const stop = network.subscribe(() => {
      if (!network.offline()) done()
    })
  })
}

// toggleOptions is the mutation every toggle of the week's Supply run is,
// made on the Supply run screen (useToggles) or replayed from the queue saved
// on this device (useSupplyRunSync).
//
// Toggles are sent one at a time, in the order made, whatever their week
// (one mutation scope, TOGGLE_SCOPE), so the counts each is answered with
// include every toggle before it and none after, and a replayed toggle never
// overlaps another replayed or made here. While the server cannot be
// reached — the device is offline, or the toggle's PATCH failed with a
// network error (fetch rejected with a TypeError), or a try ran past its
// deadline (TOGGLE_DEADLINE) — a toggle waits, pending, and is tried again
// once network says the server can be reached, or after a wait that doubles
// from RETRY_FIRST up to RETRY_CEILING, whichever is first: those after it
// wait behind it, in order. A try past its deadline is aborted and marks the
// network failed, as a network error does, so the item says it will sync; the
// server may have applied the aborted PATCH, and sending it again is safe for
// the reason a replay is (see useSupplyRunSync). Any answer from the server
// settles it: a refusal (a 400, 404, 409 or any other error response) or a
// response that is not the item as toggled fails it, and it is not sent again. Before
// a toggle is sent, GET /api/v1/me has answered, so it carries the session's
// CSRF token even when the app opened offline.
//
// Every toggle is in the queue saved on this device (enqueue, by useToggles)
// until it settles, so a reload replays those the server never answered.
function toggleOptions(
  weekStart: string,
  api: ApiClient,
  queryClient: QueryClient,
): MutationObserverOptions<Counts, Error, KeyedToggle> {
  const key = supplyRunKey(weekStart)
  const mutationKey = toggleKey(weekStart)
  return {
    mutationKey,
    scope: { id: TOGGLE_SCOPE },
    networkMode: 'always',
    retry: false,
    gcTime: Infinity,
    mutationFn: async ({ key, id, needed }: KeyedToggle): Promise<Counts> => {
      const path = `${SHOPPING_PATH}/items/${encodeURIComponent(id)}`
      const where = `PATCH ${path}`
      let body: unknown
      for (let failures = 0; ; failures++) {
        if (api.network.offline()) markWillSync(key)
        await reachable(api.network, Math.min(RETRY_FIRST * 2 ** failures, RETRY_CEILING))
        const deadline = new AbortController()
        const timer = setTimeout(() => deadline.abort(new DOMException(`${where}: no answer in time`, 'TimeoutError')), TOGGLE_DEADLINE)
        try {
          // Each try loads /me itself, under its deadline, rather than share
          // a /me already in flight that may never answer.
          if (queryClient.getQueryData(meQueryKey) === undefined) {
            queryClient.setQueryData(meQueryKey, await api.loadMe(deadline.signal))
          }
          body = await api.apiFetch<unknown>(path, { method: 'PATCH', body: { needed }, signal: deadline.signal })
          break
        } catch (error) {
          if (deadline.signal.aborted) {
            api.network.requestFailed()
            continue
          }
          // The API client marks the network offline as fetch rejects; a
          // TypeError it did not mark is not a network failure.
          if (!(error instanceof TypeError) || !api.network.offline()) throw error
        } finally {
          clearTimeout(timer)
        }
      }
      if (!isRecord(body) || body.id !== id || body.needed !== needed) throw new Error(`${where}: response is not item ${id} as toggled`)
      return parseCounts(where, body)
    },
    // A read of the Supply run sent before the toggle must not land after
    // its answer, over it.
    onMutate: () => queryClient.cancelQueries({ queryKey: key }),
    onSuccess: (counts, { id, needed }) => {
      queryClient.setQueryData<SupplyRun | null>(
        key,
        (run) =>
          run && {
            ...run,
            ...counts,
            groups: run.groups.map((group) => ({
              ...group,
              items: group.items.map((item) => (item.id === id ? { ...item, needed } : item)),
            })),
          },
      )
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'archived') void queryClient.invalidateQueries({ queryKey: key })
    },
    // This toggle is still pending as it settles, and those after it wait
    // for it: the settled toggles of its item are the earlier ones. Settled,
    // it leaves the saved queue.
    onSettled: (_counts, _error, { key: settled, id }) => {
      const cache = queryClient.getMutationCache()
      for (const earlier of cache.findAll({ mutationKey, predicate: (m) => m.state.status !== 'pending' })) {
        if ((earlier.state.variables as KeyedToggle).id === id) cache.remove(earlier)
      }
      keep(toggleCause(settled), () => dequeue(settled))
      synced(settled)
    },
  }
}

// useToggles marks items of the week's Supply run, already read, to buy or
// "have it" (PATCH /api/v1/shopping/items/{id}; see toggleOptions). The
// Supply run cached is the server's: a toggle's answer writes the item and
// the counts it answers with there. Until then the toggle is pending and is
// laid over the Supply run shown, so the item and the needed count change at
// once; a refused toggle just stops being pending, which puts both back.
// An item whose pending toggle was made, or waited, while the server could
// not be reached will sync: it says so until that toggle settles, even once
// the server can be reached again, and even when the screen is left and
// opened again meanwhile (markWillSync keeps it with the page, not here).
//
// The Supply run and the toggles are read straight from the query and
// mutation caches as each render begins, never from the caches' batched
// notices, which can arrive apart: a toggle is in the mutation cache as
// pending as soon as it is made, and its answer is in the query cache
// before it stops being pending. Making a toggle renders again at once, so
// the checkbox clicked never shows its old state; useSupplyRun and
// useMutationState render again on every later change. Like useSupplyRun's,
// the Supply run shows each dinner removed on this device archived
// (showArchived).
//
// A failed toggle stays failed, and shown, until a later toggle of its item
// supersedes it, such as its Retry: a toggle of another item succeeding
// does not hide it. So that a failure is never collected unseen, toggles are
// kept (gcTime Infinity), and a settled toggle removes the earlier toggles
// of its item it supersedes. A toggle refused because the week was frozen
// into the Supply archive (409 archived), even one replayed after the device
// was offline, reads the Supply run again, which then renders read-only.
export function useToggles(weekStart: string): Toggles {
  const api = useApi()
  const queryClient = useQueryClient()
  const key = supplyRunKey(weekStart)
  const mutationKey = toggleKey(weekStart)
  useSupplyRun(weekStart)
  useMutationState({ filters: { mutationKey } })
  const offline = useSyncExternalStore(api.network.subscribe, api.network.offline)
  const [, renderAgain] = useReducer((n: number) => n + 1, 0)
  const archived = useArchived()
  const run = showArchived(queryClient.getQueryData<SupplyRun | null>(key) ?? null, knownArchived(archived))
  if (!run) throw new Error(`The week of ${weekStart} has no Supply run read to toggle items of.`)
  const toggles = queryClient
    .getMutationCache()
    .findAll({ mutationKey })
    .map((mutation) => ({ status: mutation.state.status, toggle: mutation.state.variables as KeyedToggle, error: mutation.state.error }))
  const pending = toggles.filter((t) => t.status === 'pending').map((t) => t.toggle)
  // Every change of the network status renders again, so each pending
  // toggle seen while the server cannot be reached is marked.
  if (offline) pending.forEach((t) => markWillSync(t.key))
  const failed = toggles
    .filter((t, i) => t.status === 'error' && !toggles.slice(i + 1).some((later) => later.toggle.id === t.toggle.id))
    .at(-1)
  const { mutate } = useMutation(toggleOptions(weekStart, api, queryClient))
  return {
    shown: shown(run, pending),
    willSync: new Set(pending.filter((t) => willSync(t.key)).map((t) => t.id)),
    failed: failed === undefined ? null : { toggle: { id: failed.toggle.id, needed: failed.toggle.needed }, error: failed.error! },
    toggle: ({ id, needed }) => {
      const queuedToggle = { key: crypto.randomUUID(), weekStart, id, needed }
      keep(toggleCause(queuedToggle.key), () => enqueue(queuedToggle))
      mutate({ key: queuedToggle.key, id, needed })
      renderAgain()
    },
  }
}

// useSupplyRunSync keeps, on this device, what the Supply run screen needs
// to open offline — the household's schedule and each Supply run the server
// sends (saveRun) — and replays, in the order made, the toggles saved in the
// queue that are not already pending here: those of a page reloaded before
// the server answered them.
//
// Replay is at-least-once, not exactly-once: a toggle leaves the saved queue
// only once its answer settles here, so one the server applied just before a
// reload is sent again. That duplicate is safe because a toggle sets needed
// to a fixed value — the same PATCH applied twice leaves the item and the
// counts as applying it once does — so no key is sent for the server to
// deduplicate by.
//
// A replayed toggle's answer is written over the Supply run saved, so the
// week's Supply run is cached from it first; it is still read again
// (updatedAt 0) when the Supply run screen opens.
//
// Each Supply run is saved with the dinners removed on this device shown
// archived (showArchived), and once the server archives one, every Supply
// run saved and cached is saved again with it archived (onArchived).
export function useSupplyRunSync() {
  const api = useApi()
  const queryClient = useQueryClient()
  useEffect(() => {
    const stop = queryClient.getQueryCache().subscribe((event) => {
      if (event.type !== 'updated' || event.action.type !== 'success') return
      const [scope, week] = event.query.queryKey
      const data: unknown = event.query.state.data
      if (scope === 'shopping' && typeof week === 'string' && event.query.queryKey.length === 2) {
        keep(runCause(week), () => saveRun(week, showArchived(data as SupplyRun | null, knownArchived(archivedNow()))))
      } else if (scope === HOUSEHOLD_SETTINGS_KEY[0] && week === HOUSEHOLD_SETTINGS_KEY[1]) {
        keep(SCHEDULE_CAUSE, () => saveSchedule(data as HouseholdSettings))
      }
    })
    // A dinner the server archived, here or in another tab, shows archived
    // on each Supply run saved on this device and each one cached, so what
    // is saved says so too.
    const stopArchived = onArchived((id) => {
      const ids = new Set([id])
      for (const week of keep(WEEKS_CAUSE, savedWeeks) ?? []) {
        keep(runCause(week), () => {
          const run = savedSupplyRun(week)
          if (run !== undefined) saveRun(week, showArchived(run, ids))
        })
      }
      for (const query of queryClient.getQueryCache().findAll({ queryKey: ['shopping'] })) {
        const [, week] = query.queryKey
        if (typeof week !== 'string' || query.queryKey.length !== 2 || query.state.data === undefined) continue
        queryClient.setQueryData<SupplyRun | null>(query.queryKey, (run) => showArchived(run ?? null, ids))
      }
    })
    const pending = new Set(
      queryClient
        .getMutationCache()
        .findAll({ mutationKey: ['shopping'], predicate: (m) => m.state.status === 'pending' })
        .map((m) => (m.state.variables as KeyedToggle).key),
    )
    for (const { key, weekStart, id, needed } of keep(QUEUE_CAUSE, queued) ?? []) {
      if (pending.has(key)) continue
      const runKey = supplyRunKey(weekStart)
      if (queryClient.getQueryData(runKey) === undefined) {
        const saved = keep(runCause(weekStart), () => savedSupplyRun(weekStart))
        if (saved !== undefined) queryClient.setQueryData(runKey, saved, { updatedAt: 0 })
      }
      // A refusal is the mutation's state, which the Supply run screen shows.
      new MutationObserver(queryClient, toggleOptions(weekStart, api, queryClient)).mutate({ key, id, needed }).catch(() => {})
    }
    return () => {
      stop()
      stopArchived()
    }
  }, [api, queryClient])
}

// SupplyRunSync runs useSupplyRunSync for the whole app.
export function SupplyRunSync(): null {
  useSupplyRunSync()
  return null
}

// shown is run as the household sees it with the pending toggles laid over
// it: each item as its latest pending toggle marks it, and the needed count
// moved to match.
function shown(run: SupplyRun, pending: Toggle[]): SupplyRun {
  const marked = new Map(pending.map((toggle) => [toggle.id, toggle.needed]))
  let needed = run.needed_count
  const groups = run.groups.map((group) => ({
    ...group,
    items: group.items.map((item) => {
      const mark = marked.get(item.id)
      if (mark === undefined || mark === item.needed) return item
      needed += mark ? 1 : -1
      return { ...item, needed: mark }
    }),
  }))
  return { ...run, needed_count: needed, groups }
}

export const ARCHIVE_PERIODS = ['week', 'month', 'year'] as const

// ArchivePeriod is what the Supply archive is grouped by.
export type ArchivePeriod = (typeof ARCHIVE_PERIODS)[number]

// ArchivePost is one week's Supply run in the Supply archive: label is the
// week label, "Week 41 · Oct 5 – 11", which stands in for title while it is
// null; onHandCount counts the items marked "have it", and dinners names the
// dinners in day order. current is true for the household's current week.
export type ArchivePost = {
  weekStart: string
  label: string
  title: string | null
  dinnerCount: number
  itemCount: number
  onHandCount: number
  dinners: string[]
  current: boolean
}

// ArchiveGroup is one period of the Supply archive — a week, a month
// ("October 2026") or a year ("2026") — and its posts, newest first.
export type ArchiveGroup = { key: string; heading: string; posts: ArchivePost[] }

function parsePost(where: string, body: unknown): ArchivePost {
  if (
    !isRecord(body) ||
    typeof body.weekStart !== 'string' ||
    typeof body.label !== 'string' ||
    (body.title !== null && typeof body.title !== 'string') ||
    !Array.isArray(body.dinners) ||
    !body.dinners.every((name) => typeof name === 'string') ||
    typeof body.current !== 'boolean'
  ) {
    throw new Error(`${where}: response has a post that is not a Supply archive post`)
  }
  return {
    weekStart: body.weekStart,
    label: body.label,
    title: body.title,
    dinnerCount: count(where, 'dinnerCount', body.dinnerCount),
    itemCount: count(where, 'itemCount', body.itemCount),
    onHandCount: count(where, 'onHandCount', body.onHandCount),
    dinners: body.dinners as string[],
    current: body.current,
  }
}

function parseArchiveGroup(where: string, body: unknown): ArchiveGroup {
  if (!isRecord(body) || typeof body.key !== 'string' || typeof body.heading !== 'string' || !Array.isArray(body.posts)) {
    throw new Error(`${where}: response has a period that is not a Supply archive period`)
  }
  return { key: body.key, heading: body.heading, posts: body.posts.map((post) => parsePost(where, post)) }
}

// useSupplyArchive is the Supply archive grouped by by, newest first (GET
// /api/v1/shopping/archive?by=week|month|year): the server groups it in the
// household's zone, so it is shown as answered, never grouped again here.
export function useSupplyArchive(by: ArchivePeriod) {
  const { apiRead } = useApi()
  return useQuery({
    queryKey: ['supply archive', by] as const,
    queryFn: async (): Promise<ArchiveGroup[]> => {
      const path = `${SHOPPING_PATH}/archive?${new URLSearchParams({ by })}`
      const where = `GET ${path}`
      const { body } = await apiRead<unknown>(path)
      if (!isRecord(body) || body.by !== by || !Array.isArray(body.periods)) {
        throw new Error(`${where}: response is not the Supply archive by ${by}`)
      }
      return body.periods.map((period) => parseArchiveGroup(where, period))
    },
  })
}
