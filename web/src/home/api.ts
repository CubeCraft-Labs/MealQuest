import { useQuery } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { oneOf, photoId } from '../dinners/api.ts'
import { weekLabel } from '../plan/week.ts'

export const HOME_PATH = '/api/v1/home'
export const HOME_KEY = ['home'] as const

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
  const minutes = body.minutes_total ?? null
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

// parseShoppingList checks the shoppingList of a GET /api/v1/home body. A
// ready list must carry the API's label for its week, which must be the
// label weekStart has, since the status row shows dates from weekStart.
function parseShoppingList(where: string, list: unknown): ShoppingListStatus {
  if (!isRecord(list) || typeof list.weekStart !== 'string' || typeof list.ready !== 'boolean') {
    throw new Error(`${where}: shoppingList is not a week's Supply run status`)
  }
  const week = weekLabel(list.weekStart)
  if (list.ready) {
    if (typeof list.label !== 'string' || list.label === '') {
      throw new Error(`${where}: the ready shoppingList has no label`)
    }
    if (list.label !== `${week.week} · ${week.dates}`) {
      throw new Error(`${where}: shoppingList label "${list.label}" is not the week of ${list.weekStart}`)
    }
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
