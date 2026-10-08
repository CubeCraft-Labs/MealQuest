import {
  MutationObserver,
  useMutation,
  useMutationState,
  useQuery,
  useQueryClient,
  type MutationObserverOptions,
  type QueryClient,
} from '@tanstack/react-query'
import { useEffect, useReducer, useSyncExternalStore } from 'react'
import { ApiError, useApi, type ApiClient } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { meQueryKey } from '../AuthBootstrap.tsx'
import { oneOf } from '../dinners/api.ts'
import type { NetworkStatus } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS_KEY, type HouseholdSettings } from '../settings/api.ts'
import {
  dequeue,
  enqueue,
  keep,
  markWillSync,
  queued,
  SAVED_KEY,
  saveRun,
  savedRun,
  saveSchedule,
  synced,
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
// day, household-local YYYY-MM-DD.
export type SupplyRunGroup = { dinner: { id: string; name: string }; date: string; items: SupplyRunItem[] }

// Counts is how many of a Supply run's items are still to buy (needed) of
// all of them (total).
export type Counts = { needed_count: number; total_count: number }

// SupplyRun is GET /api/v1/shopping/{weekStart}: label is the week label,
// "Week 41 · Oct 5 – 11", title the AI's title once written, and
// sent_to_chat_at when it was sent to Google Chat, if it was.
export type SupplyRun = Counts & {
  weekStart: string
  label: string
  title: string | null
  status: SupplyRunStatus
  sent_to_chat_at: Date | null
  groups: SupplyRunGroup[]
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

function parseGroup(where: string, body: unknown): SupplyRunGroup {
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
    dinner: { id: body.dinner.id, name: body.dinner.name },
    date: body.date,
    items: body.items.map((item) => parseItem(where, item)),
  }
}

// sentAt reads sent_to_chat_at, an RFC 3339 instant or null.
function sentAt(where: string, value: unknown): Date | null {
  if (value === null) return null
  const at = typeof value === 'string' ? new Date(value) : null
  if (at === null || Number.isNaN(at.getTime())) {
    throw new Error(`${where}: sent_to_chat_at ${JSON.stringify(value)} is not an instant`)
  }
  return at
}

export function parseSupplyRun(where: string, weekStart: string, body: unknown): SupplyRun {
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
    sent_to_chat_at: sentAt(where, body.sent_to_chat_at),
    ...parseCounts(where, body),
    groups: body.groups.map((group) => parseGroup(where, group)),
  }
}

// savedSupplyRun is the week's Supply run saved on this device, or undefined.
function savedSupplyRun(weekStart: string): SupplyRun | undefined {
  const saved = savedRun(weekStart)
  return saved === undefined ? undefined : parseSupplyRun(`Saved ${SAVED_KEY}`, weekStart, saved)
}

// useSupplyRun is the Supply run of the week starting on Monday weekStart
// (GET /api/v1/shopping/{weekStart}), or null when the week has none (404).
// It starts from the Supply run saved on this device, if there is one, so
// the list opens offline; the saved one is always read again.
export function useSupplyRun(weekStart: string) {
  const { apiRead } = useApi()
  return useQuery({
    queryKey: supplyRunKey(weekStart),
    initialData: () => keep(() => savedSupplyRun(weekStart)),
    initialDataUpdatedAt: 0,
    queryFn: async (): Promise<SupplyRun | null> => {
      const path = supplyRunPath(weekStart)
      let body: unknown
      try {
        body = (await apiRead<unknown>(path)).body
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null
        throw error
      }
      return parseSupplyRun(`GET ${path}`, weekStart, body)
    },
  })
}

// useGenerateSupplyRun makes the week's Supply run from its plan (POST
// /api/v1/shopping/{weekStart}/generate); the Supply run it answers with is
// the one shown.
export function useGenerateSupplyRun(weekStart: string) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (): Promise<SupplyRun> => {
      const path = `${supplyRunPath(weekStart)}/generate`
      return parseSupplyRun(`POST ${path}`, weekStart, await apiFetch<unknown>(path, { method: 'POST' }))
    },
    onSuccess: (run) => queryClient.setQueryData<SupplyRun | null>(supplyRunKey(weekStart), run),
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
// GET /api/v1/me and its PATCH — may take before it counts as the server not
// answering. Only toggles have a deadline: the API client sets none.
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

// beforeDeadline is promise, rejected with signal's reason once it aborts.
function beforeDeadline<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    signal.addEventListener('abort', () => reject(signal.reason as Error), { once: true })
    promise.then(resolve, reject)
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
          const me = queryClient.ensureQueryData({ queryKey: meQueryKey, queryFn: api.loadMe, networkMode: 'always' })
          await beforeDeadline(me, deadline.signal)
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
      keep(() => dequeue(settled))
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
// useMutationState render again on every later change.
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
  const run = queryClient.getQueryData<SupplyRun | null>(key)
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
      keep(() => enqueue(queuedToggle))
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
export function useSupplyRunSync() {
  const api = useApi()
  const queryClient = useQueryClient()
  useEffect(() => {
    const stop = queryClient.getQueryCache().subscribe((event) => {
      if (event.type !== 'updated' || event.action.type !== 'success') return
      const [scope, week] = event.query.queryKey
      const data: unknown = event.query.state.data
      if (scope === 'shopping' && typeof week === 'string' && event.query.queryKey.length === 2) {
        keep(() => saveRun(week, data as SupplyRun | null))
      } else if (scope === HOUSEHOLD_SETTINGS_KEY[0] && week === HOUSEHOLD_SETTINGS_KEY[1]) {
        keep(() => saveSchedule(data as HouseholdSettings))
      }
    })
    const pending = new Set(
      queryClient
        .getMutationCache()
        .findAll({ mutationKey: ['shopping'], predicate: (m) => m.state.status === 'pending' })
        .map((m) => (m.state.variables as KeyedToggle).key),
    )
    for (const { key, weekStart, id, needed } of keep(queued) ?? []) {
      if (pending.has(key)) continue
      const runKey = supplyRunKey(weekStart)
      if (queryClient.getQueryData(runKey) === undefined) {
        const saved = keep(() => savedSupplyRun(weekStart))
        if (saved !== undefined) queryClient.setQueryData(runKey, saved, { updatedAt: 0 })
      }
      // A refusal is the mutation's state, which the Supply run screen shows.
      new MutationObserver(queryClient, toggleOptions(weekStart, api, queryClient)).mutate({ key, id, needed }).catch(() => {})
    }
    return stop
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
