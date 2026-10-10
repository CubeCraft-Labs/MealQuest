import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { oneOf, photoId } from '../dinners/api.ts'
import { weekLabel } from '../plan/week.ts'
import { appPath } from '../push/notification.ts'

export const HOME_PATH = '/api/v1/home'
export const HOME_KEY = ['home'] as const
export const UPDATES_PATH = '/api/v1/updates'
export const UPDATES_SEEN_PATH = '/api/v1/updates/seen'
export const UPDATES_KEY = ['updates'] as const

export const ORIGINS = ['regular', 'side_quest'] as const
export type Origin = (typeof ORIGINS)[number]

// Tonight is the dinner planned for today: photo_id is its stored photo's
// id, from photo, and minutes_total its minutes from prep to table, a
// positive whole number, or null when the recipe does not say.
export type Tonight = { id: string; name: string; photo_id: string | undefined; origin: Origin; minutes_total: number | null }

// ShoppingListStatus is the Supply run Home points at: next week's once it
// has one, otherwise this week's, ready when that week has a list.
// weekStart is the week's Monday, YYYY-MM-DD, and dates the week as the
// status row shows it, "Oct 5–11" (the API's label is the long "Week 41 ·
// Oct 5 – 11").
export type ShoppingListStatus = { weekStart: string; dates: string; ready: boolean }

// Home is GET /api/v1/home: dateLabel is today, household-local, "Friday,
// Oct 2"; tonight is null when today has no plan; sideQuestCount is how
// many Side quests wait in the deck; unread says the Recent updates hold
// something not yet seen.
export type Home = {
  dateLabel: string
  tonight: Tonight | null
  sideQuestCount: number
  shoppingList: ShoppingListStatus
  unread: boolean
}

function parseTonight(where: string, body: unknown): Tonight | null {
  if (body === null) return null
  if (!isRecord(body) || typeof body.id !== 'string' || typeof body.name !== 'string') {
    throw new Error(`${where}: tonight is not a dinner`)
  }
  if (!('minutes_total' in body)) throw new Error(`${where}: tonight has no minutes_total`)
  const minutes = body.minutes_total
  if (minutes !== null && (!Number.isInteger(minutes) || (minutes as number) <= 0)) {
    throw new Error(`${where}: tonight's minutes_total ${String(minutes)} is not a positive whole number`)
  }
  return {
    id: body.id,
    name: body.name,
    photo_id: photoId(where, body.photo),
    origin: oneOf(where, 'origin', ORIGINS, body.origin),
    minutes_total: minutes as number | null,
  }
}

// parseShoppingList checks the shoppingList of a GET /api/v1/home body. The
// list, ready or not, must carry the API's label for its week, which must be
// the label weekStart has, since the status row shows dates from weekStart.
function parseShoppingList(where: string, list: unknown): ShoppingListStatus {
  if (!isRecord(list) || typeof list.weekStart !== 'string' || typeof list.ready !== 'boolean') {
    throw new Error(`${where}: shoppingList is not a week's Supply run status`)
  }
  const week = weekLabel(list.weekStart)
  if (typeof list.label !== 'string' || list.label === '') {
    throw new Error(`${where}: the ${list.ready ? 'ready' : 'unready'} shoppingList has no label`)
  }
  if (list.label !== `${week.week} · ${week.dates}`) {
    throw new Error(`${where}: shoppingList label "${list.label}" is not the week of ${list.weekStart}`)
  }
  return { weekStart: list.weekStart, dates: week.dates.replace(' – ', '–'), ready: list.ready }
}

// parseHome checks a GET /api/v1/home body.
export function parseHome(where: string, body: unknown): Home {
  if (!isRecord(body) || typeof body.dateLabel !== 'string') throw new Error(`${where}: response is not the home`)
  if (!Number.isInteger(body.sideQuestCount) || (body.sideQuestCount as number) < 0) {
    throw new Error(`${where}: sideQuestCount is not a count`)
  }
  const updates = body.recentUpdates
  if (!isRecord(updates) || typeof updates.unread !== 'boolean') throw new Error(`${where}: recentUpdates has no unread flag`)
  return {
    dateLabel: body.dateLabel,
    tonight: parseTonight(where, body.tonight),
    sideQuestCount: body.sideQuestCount as number,
    shoppingList: parseShoppingList(where, body.shoppingList),
    unread: updates.unread,
  }
}

// useHome is the Home screen's summary (GET /api/v1/home).
export function useHome() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: HOME_KEY,
    queryFn: async () => parseHome(`GET ${HOME_PATH}`, await apiFetch<unknown>(HOME_PATH)),
  })
}

export const UPDATE_TYPES = ['plan_ready', 'list_ready', 'day_of_reminder', 'new_suggestions', 'rotation_review'] as const
export type UpdateType = (typeof UPDATE_TYPES)[number]

// Update is one of the Recent updates: a notification event, whatever
// channels it went to. url is the same-origin path it opens; created_at is
// when it was stored, the UTC RFC 3339 instant POST /api/v1/updates/seen
// takes back.
export type Update = { type: UpdateType; title: string; body: string; url: string; created_at: string }

// parseUpdate checks one update. Its url must stay on origin once parsed, as
// the server only ever stores a path on the app's own origin; url is that
// path as parsed.
function parseUpdate(where: string, body: unknown, origin: string): Update {
  if (
    !isRecord(body) ||
    typeof body.title !== 'string' ||
    typeof body.body !== 'string' ||
    typeof body.url !== 'string' ||
    typeof body.created_at !== 'string'
  ) {
    throw new Error(`${where}: an update is not a notification event`)
  }
  const url = appPath(body.url, origin)
  if (url === null) throw new Error(`${where}: update url ${JSON.stringify(body.url)} is not a path on this app`)
  if (Number.isNaN(Date.parse(body.created_at))) {
    throw new Error(`${where}: update created_at "${body.created_at}" is not a time`)
  }
  return {
    type: oneOf(where, 'type', UPDATE_TYPES, body.type),
    title: body.title,
    body: body.body,
    url,
    created_at: body.created_at,
  }
}

// parseUpdates checks a GET /api/v1/updates body, from the app at origin:
// the updates, newest first.
export function parseUpdates(where: string, body: unknown, origin: string): Update[] {
  if (!isRecord(body) || !Array.isArray(body.updates)) throw new Error(`${where}: response is not the updates`)
  return body.updates.map((update) => parseUpdate(where, update, origin))
}

// useUpdates is the Recent updates (GET /api/v1/updates): the last ten
// notification events, newest first. Each opening of the sheet reads it
// afresh (refetchOnMount 'always'), whatever the query client still holds;
// the sheet shows, and marks seen, only the list that opening read.
export function useUpdates() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: UPDATES_KEY,
    queryFn: async () =>
      parseUpdates(`GET ${UPDATES_PATH}`, await apiFetch<unknown>(UPDATES_PATH), window.location.origin),
    refetchOnMount: 'always',
  })
}

const MARK_SEEN_KEY = ['updates', 'seen'] as const

// useMarkUpdatesSeen records that the household has seen its updates
// through the created_at of the newest one shown (POST
// /api/v1/updates/seen). The bell's unread dot clears at once, and a Home
// read in flight is stopped so it cannot bring it back; if the server
// refuses, the dot is put back as it was before the first of the marks in
// flight. Once the last mark in flight settles Home is read again, so the
// dot shows only for an event stored since.
export function useMarkUpdatesSeen() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  // alone says whether this is the only mark in flight; each counts itself
  // until it has settled.
  const alone = () => queryClient.isMutating({ mutationKey: MARK_SEEN_KEY }) === 1
  return useMutation({
    mutationKey: MARK_SEEN_KEY,
    mutationFn: async (through: string) => {
      await apiFetch<unknown>(UPDATES_SEEN_PATH, { method: 'POST', body: { through } })
    },
    onMutate: async () => {
      // A mark sent while another is in flight would snapshot that one's
      // optimistic answer, so only the first snapshots.
      const unread = alone() ? queryClient.getQueryData<Home>(HOME_KEY)?.unread : undefined
      await queryClient.cancelQueries({ queryKey: HOME_KEY })
      queryClient.setQueryData<Home>(HOME_KEY, (home) => home && { ...home, unread: false })
      return { unread }
    },
    onError: (_error, _through, context) => {
      if (context?.unread === undefined || !alone()) return
      const { unread } = context
      queryClient.setQueryData<Home>(HOME_KEY, (home) => home && { ...home, unread })
    },
    onSettled: () => {
      if (alone()) void queryClient.invalidateQueries({ queryKey: HOME_KEY })
    },
  })
}
