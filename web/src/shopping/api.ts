import { useMutation, useMutationState, useQuery, useQueryClient } from '@tanstack/react-query'
import { useReducer } from 'react'
import { ApiError, useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { oneOf } from '../dinners/api.ts'

export const SHOPPING_PATH = '/api/v1/shopping'

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

// useSupplyRun is the Supply run of the week starting on Monday weekStart
// (GET /api/v1/shopping/{weekStart}), or null when the week has none (404).
export function useSupplyRun(weekStart: string) {
  const { apiRead } = useApi()
  return useQuery({
    queryKey: supplyRunKey(weekStart),
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

// toggleKey keys the toggles of the week starting on weekStart.
function toggleKey(weekStart: string) {
  return ['shopping', weekStart, 'toggle'] as const
}

// Toggles is the toggles of one week's Supply run: shown, the Supply run
// with those still pending laid over it, the latest toggle if it failed,
// and toggle to make another.
export type Toggles = {
  shown: SupplyRun
  failed: { toggle: Toggle; error: Error } | null
  toggle: (toggle: Toggle) => void
}

// useToggles marks items of the week's Supply run, already read, to buy or
// "have it" (PATCH /api/v1/shopping/items/{id}). The Supply run cached is
// the server's: a toggle's answer writes the item and the counts it answers
// with there. Until then the toggle is pending and is laid over the Supply
// run shown, so the item and the needed count change at once; a refused
// toggle just stops being pending, which puts both back.
//
// The Supply run and the toggles are read straight from the query and
// mutation caches as each render begins, never from the caches' batched
// notices, which can arrive apart: a toggle is in the mutation cache as
// pending as soon as it is made, and its answer is in the query cache
// before it stops being pending. Making a toggle renders again at once, so
// the checkbox clicked never shows its old state; useSupplyRun and
// useMutationState render again on every later change.
//
// The week's toggles are sent one at a time, in the order made (one
// mutation scope), so the counts each is answered with include every toggle
// before it and none after. Each is sent once, and not held back while the
// device is offline (networkMode always): an item never shows a state for
// longer than it takes the server to refuse it.
export function useToggles(weekStart: string): Toggles {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  const key = supplyRunKey(weekStart)
  const mutationKey = toggleKey(weekStart)
  useSupplyRun(weekStart)
  useMutationState({ filters: { mutationKey } })
  const [, renderAgain] = useReducer((n: number) => n + 1, 0)
  const run = queryClient.getQueryData<SupplyRun | null>(key)
  if (!run) throw new Error(`The week of ${weekStart} has no Supply run read to toggle items of.`)
  const toggles = queryClient
    .getMutationCache()
    .findAll({ mutationKey })
    .map((mutation) => ({ status: mutation.state.status, toggle: mutation.state.variables as Toggle, error: mutation.state.error }))
  const latest = toggles.at(-1)
  const { mutate } = useMutation({
    mutationKey,
    scope: { id: mutationKey.join(' ') },
    networkMode: 'always',
    retry: false,
    mutationFn: async ({ id, needed }: Toggle): Promise<Counts> => {
      const path = `${SHOPPING_PATH}/items/${encodeURIComponent(id)}`
      const where = `PATCH ${path}`
      const body = await apiFetch<unknown>(path, { method: 'PATCH', body: { needed } })
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
  })
  return {
    shown: shown(run, toggles.filter((t) => t.status === 'pending').map((t) => t.toggle)),
    failed: latest?.status === 'error' ? { toggle: latest.toggle, error: latest.error! } : null,
    toggle: (toggle) => {
      mutate(toggle)
      renderAgain()
    },
  }
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
