import { useIsMutating, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useCallback, useEffect } from 'react'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import type { Home } from '../home/api.ts'
import { HOME_KEY } from '../home/api.ts'
import type { Plan, PlanDay } from '../plan/api.ts'
import type { SupplyRun } from '../shopping/api.ts'
import { reconcileSavedMealSource } from '../shopping/offline.ts'
import { archiveDinner, checkRemovals, knownArchived, useArchived, type SendRemoval } from './archived.ts'
import type { DinnerDetail } from './detail.ts'

export const DINNERS_PATH = '/api/v1/dinners'
export const REGULARS_PATH = `${DINNERS_PATH}?status=regular`

// GROUNDING_POLL_MS is how often the Regulars are read again while any of
// them is still finding its recipe.
export const GROUNDING_POLL_MS = 3000

// Methods and Difficulties are the values the dinners API allows.
export const METHODS = ['stovetop', 'oven', 'crockpot', 'grill', 'other'] as const
export const DIFFICULTIES = ['easy', 'medium', 'hard'] as const
export const GROUNDING_STATUSES = ['pending', 'grounded', 'failed', 'manual'] as const
export const MEAL_SOURCES = ['cook', 'delivery', 'dining_out'] as const

// MEAL_SOURCE_LABEL is how the household reads a dinner's meal source.
export const MEAL_SOURCE_LABEL = {
  cook: 'Cook at home',
  delivery: 'Delivery',
  dining_out: 'Dining out',
} as const satisfies Record<MealSource, string>

// PLANNED_DINNER_STATUSES is every status a dinner on a plan day or a Supply
// run can have now: archived once it was removed from Regulars after the
// plan or Supply run was made, when it has no detail to open.
export const PLANNED_DINNER_STATUSES = ['suggested', 'quest_log', 'tried', 'regular', 'archived'] as const

export type Method = (typeof METHODS)[number]
export type Difficulty = (typeof DIFFICULTIES)[number]
export type GroundingStatus = (typeof GROUNDING_STATUSES)[number]
export type MealSource = (typeof MEAL_SOURCES)[number]
export type PlannedDinnerStatus = (typeof PLANNED_DINNER_STATUSES)[number]

// opensRecipe says whether a dinner on a plan day, Home, a Supply run, the
// Regulars or the Quest log offers its recipe: only when the server itself just said it is not
// archived and it is cooked at home. A status from a read the service worker answered from its cache,
// or one saved on this device (cached), may be stale — another device may
// have archived the dinner since — so it offers no recipe, whose detail
// could be gone; nor does a status not known (null), nor a meal source not
// known. Delivery and dining out have no recipe. A read from the server
// offers a cooked dinner again.
export function opensRecipe(status: PlannedDinnerStatus | null, cached: boolean, mealSource: MealSource | null): boolean {
  return !cached && status !== null && status !== 'archived' && mealSource === 'cook'
}

// Regular is the part of a dinner the Regulars screens read. photo_id is
// the stored photo's id, from photo_url. grounding_error is why its
// grounding failed, a reason code, set only while grounding_status is
// failed. photo_backfill_eligible is whether a photo backfill (Search
// again) searches for it: never for a dinner with a pasted recipe link.
// pending is true while an optimistic add waits for the server;
// its id is not the server's.
export type Regular = {
  id: string
  name: string
  method: Method | null
  minutes_total: number | null
  difficulty: Difficulty | null
  is_favorite: boolean
  photo_id: string | undefined
  grounding_status: GroundingStatus
  grounding_error: string | null
  photo_backfill_eligible: boolean
  meal_source: MealSource
  pending?: true
}

// NewRegular is a POST /api/v1/dinners body: the household's name for the
// dinner, with the recipe link to ground it in when one was pasted. A
// dinner is never named from its link, which may carry private details.
export type NewRegular = { name: string; recipe_url?: string }

// RegularPatch is a PATCH /api/v1/dinners/{id} body; every field is optional.
export type RegularPatch = Partial<{
  name: string
  method: Method
  minutes_total: number
  difficulty: Difficulty
  meal_source: MealSource
}>

// ManualRecipe is a recipe the household types in; the server needs its
// minutes and method.
export type ManualRecipe = { ingredients: string[]; steps: string[]; minutes: number; method: Method }

// RecipeFix is a POST /api/v1/dinners/{id}/ground body: exactly one way to
// ground the dinner.
export type RecipeFix = { candidate_id: string } | { recipe_url: string } | { manual: ManualRecipe }

// RecipeCandidate is one recipe page a dinner could be grounded in.
export type RecipeCandidate = {
  candidate_id: string
  title: string
  site_name: string
  photo_id: string | undefined
  minutes: number | null
}

// ListedRegular is a Regular in the Regulars list. cached says its state is
// the service worker's cached copy of the list, not the server's answer: it
// offers no recipe (opensRecipe).
export type ListedRegular = Regular & { cached: boolean }

// CachedRegular is a Regular as the cache holds it. removing is true while
// an optimistic removal waits for the server: the entry keeps its place, so
// a removal never sent — the device could not save it — returns exactly
// there whatever else changed meanwhile.
type CachedRegular = ListedRegular & { removing?: true }

// answered is a dinner as the server answered a change to it: a non-GET
// never reaches the service worker's cache.
function answered(dinner: Regular): CachedRegular {
  return { ...dinner, cached: false }
}

// shown is the Regulars to show: none being removed, and none the server
// archived (archived), whatever the read said.
function shown(list: CachedRegular[], archived: ReadonlySet<string>): ListedRegular[] {
  return list.filter((regular) => !regular.removing && !archived.has(regular.id))
}

function restored(regular: CachedRegular): CachedRegular {
  const copy = { ...regular }
  delete copy.removing
  return copy
}

// REGULARS_KEY prefixes every Regulars query, so a change refreshes them all
// (the settings home's count included).
export const REGULARS_KEY = ['dinners', 'regular'] as const
const REGULARS_LIST_KEY = [...REGULARS_KEY, 'list'] as const
const REGULARS_MUTATION_KEY = ['dinners', 'regular', 'change'] as const

export function oneOf<T extends string>(where: string, field: string, values: readonly T[], value: unknown): T {
  if (typeof value !== 'string' || !(values as readonly string[]).includes(value)) {
    throw new Error(`${where}: ${field} ${JSON.stringify(value)} is not one of ${values.join(', ')}`)
  }
  return value as T
}

function nullable<T>(value: unknown, parse: (value: unknown) => T): T | null {
  return value === null ? null : parse(value)
}

function wholeNumber(where: string, field: string, value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) throw new Error(`${where}: ${field} is not a whole number`)
  return value
}

const PHOTO_URL = /^\/media\/photos\/([^/]+)-400\.jpg$/

// photoId reads the photo id out of a photo_url, which the server always
// sends as /media/photos/{id}-400.jpg or null.
export function photoId(where: string, value: unknown): string | undefined {
  if (value === null) return undefined
  const match = typeof value === 'string' ? PHOTO_URL.exec(value) : null
  if (!match?.[1]) throw new Error(`${where}: photo_url ${JSON.stringify(value)} is not a stored photo`)
  return decodeURIComponent(match[1])
}

// parseDinner checks a dinner as the dinners API sends it.
export function parseDinner(where: string, body: unknown): Regular {
  if (!isRecord(body) || typeof body.id !== 'string' || typeof body.name !== 'string') {
    throw new Error(`${where}: response is not a dinner`)
  }
  if (typeof body.is_favorite !== 'boolean') throw new Error(`${where}: is_favorite is not a boolean`)
  if (typeof body.photo_backfill_eligible !== 'boolean') {
    throw new Error(`${where}: photo_backfill_eligible is not a boolean`)
  }
  return {
    id: body.id,
    name: body.name,
    method: nullable(body.method, (v) => oneOf(where, 'method', METHODS, v)),
    minutes_total: nullable(body.minutes_total, (v) => wholeNumber(where, 'minutes_total', v)),
    difficulty: nullable(body.difficulty, (v) => oneOf(where, 'difficulty', DIFFICULTIES, v)),
    is_favorite: body.is_favorite,
    photo_id: photoId(where, body.photo_url),
    grounding_status: oneOf(where, 'grounding_status', GROUNDING_STATUSES, body.grounding_status),
    grounding_error: nullable(body.grounding_error, (v) => {
      if (typeof v !== 'string') throw new Error(`${where}: grounding_error is not a string`)
      return v
    }),
    photo_backfill_eligible: body.photo_backfill_eligible,
    meal_source: oneOf(where, 'meal_source', MEAL_SOURCES, body.meal_source),
  }
}

function parseCandidate(where: string, body: unknown): RecipeCandidate {
  if (
    !isRecord(body) ||
    typeof body.candidate_id !== 'string' ||
    typeof body.title !== 'string' ||
    typeof body.site_name !== 'string'
  ) {
    throw new Error(`${where}: response is not a recipe candidate`)
  }
  return {
    candidate_id: body.candidate_id,
    title: body.title,
    site_name: body.site_name,
    photo_id: photoId(where, body.photo_url),
    minutes: nullable(body.minutes, (v) => wholeNumber(where, 'minutes', v)),
  }
}

export function dinnerPath(id: string): string {
  return `${DINNERS_PATH}/${encodeURIComponent(id)}`
}

// dinnerDetailKey is the query of dinner id with its recipe (useDinnerDetail).
export function dinnerDetailKey(id: string) {
  return ['dinners', 'detail', id] as const
}

// useRegulars is the household's Regulars, in the server's order. While any
// of them is still finding its recipe it is read again every
// GROUNDING_POLL_MS, except while any change is in flight (whose settling
// reads it again anyway), so a poll never overwrites a change being saved.
// Watching the changes in flight re-renders the hook as each one starts and
// settles, which pauses and resumes the poll. Each Regular is cached when
// the service worker answered the read from its cache.
export function useRegulars() {
  const { apiRead } = useApi()
  const changing = useIsMutating({ mutationKey: REGULARS_MUTATION_KEY }) > 0
  const archived = useArchived()
  const select = useCallback((list: CachedRegular[]) => shown(list, knownArchived(archived)), [archived])
  return useQuery({
    queryKey: REGULARS_LIST_KEY,
    queryFn: async () => {
      const where = `GET ${REGULARS_PATH}`
      const { body, cached } = await apiRead<unknown>(REGULARS_PATH)
      if (!isRecord(body) || !Array.isArray(body.dinners)) throw new Error(`${where}: response has no dinners list`)
      return body.dinners.map((dinner): CachedRegular => ({ ...parseDinner(where, dinner), cached }))
    },
    select,
    refetchInterval: (query) => {
      const list = query.state.data
      if (list === undefined || changing) return false
      return list.some((regular) => regular.grounding_status === 'pending') ? GROUNDING_POLL_MS : false
    },
  })
}

// dropRegular takes dinner id out of the Regulars read here, at once: the
// server just said it has no detail (noDetail in detail.ts), so its row
// offers no recipe even while the Regulars are being read again, or when
// that read fails.
export function dropRegular(queryClient: QueryClient, id: string) {
  queryClient.setQueryData<CachedRegular[]>(REGULARS_LIST_KEY, (list) => list?.filter((regular) => regular.id !== id))
}

// changeOptimistically stops Regulars reads in flight, so they cannot
// overwrite the optimistic list, then applies change to it.
async function changeOptimistically(queryClient: QueryClient, change: (list: CachedRegular[]) => CachedRegular[]) {
  await queryClient.cancelQueries({ queryKey: REGULARS_LIST_KEY })
  setRegulars(queryClient, change)
}

function setRegulars(queryClient: QueryClient, change: (list: CachedRegular[]) => CachedRegular[]) {
  queryClient.setQueryData<CachedRegular[]>(REGULARS_LIST_KEY, (list) => change(list ?? []))
}

// replace puts the server's answer for a dinner in its place in the list.
function replace(queryClient: QueryClient, dinner: Regular) {
  setRegulars(queryClient, (list) => list.map((regular) => (regular.id === dinner.id ? answered(dinner) : regular)))
}

// settle refetches the Regulars once the last change in flight is done, so
// an earlier change's refetch never drops a later change's optimistic entry.
function settle(queryClient: QueryClient) {
  if (queryClient.isMutating({ mutationKey: REGULARS_MUTATION_KEY }) === 1) {
    void queryClient.invalidateQueries({ queryKey: REGULARS_KEY })
  }
}

let pendingIds = 0

// useAddRegular adds a Regular by name, with or without a recipe link (POST
// /api/v1/dinners). It shows in the list at once under its name and is
// taken out again if the server refuses it.
export function useAddRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async (add: NewRegular) =>
      parseDinner(`POST ${DINNERS_PATH}`, await apiFetch<unknown>(DINNERS_PATH, { method: 'POST', body: add })),
    onMutate: async (add) => {
      const id = `pending-${++pendingIds}`
      const optimistic: CachedRegular = {
        id,
        name: add.name,
        method: null,
        minutes_total: null,
        difficulty: null,
        is_favorite: false,
        photo_id: undefined,
        grounding_status: 'pending',
        grounding_error: null,
        photo_backfill_eligible: false,
        meal_source: 'cook',
        pending: true,
        cached: false,
      }
      await changeOptimistically(queryClient, (list) => [...list, optimistic])
      return { id }
    },
    onSuccess: (dinner, _add, context) => {
      setRegulars(queryClient, (list) => list.map((regular) => (regular.id === context.id ? answered(dinner) : regular)))
    },
    onError: (_error, _add, context) => {
      if (context === undefined) return
      setRegulars(queryClient, (list) => list.filter((regular) => regular.id !== context.id))
    },
    onSettled: () => settle(queryClient),
  })
}

// useSendRemoval sends a dinner's removal (DELETE /api/v1/dinners/{id}),
// which the server archives. A non-GET never reaches the service worker's
// cache, so only the server answers it.
function useSendRemoval(): SendRemoval {
  const { apiFetch } = useApi()
  return useCallback((id: string) => apiFetch<unknown>(dinnerPath(id), { method: 'DELETE' }), [apiFetch])
}

// useArchiveDinner removes a dinner — from Regulars or the Quest log — which
// the server archives, recording it on this device first so no read
// answered before — in flight, cached or saved — shows it with a recipe to
// open (archiveDinner).
export function useArchiveDinner() {
  const send = useSendRemoval()
  return useCallback((id: string) => archiveDinner(id, send), [send])
}

// useRemovalCheck sends again each removal the server has not archived
// (checkRemovals) when the app starts and each time the server can be
// reached again.
export function useRemovalCheck() {
  const { network } = useApi()
  const send = useSendRemoval()
  useEffect(() => {
    let offline = network.offline()
    if (!offline) void checkRemovals(send)
    return network.subscribe(() => {
      const now = network.offline()
      if (offline && !now) void checkRemovals(send)
      offline = now
    })
  }, [network, send])
}

// RemovalCheck runs useRemovalCheck for the whole app.
export function RemovalCheck(): null {
  useRemovalCheck()
  return null
}

// useRemoveRegular removes a Regular, which the server archives
// (useArchiveDinner). It leaves the list at once and is put back in its
// place only if the removal was never sent; one sent and refused or not
// answered stays hidden, marked removing on this device, until the server
// archives it (RefusedRemovalError, UnconfirmedRemovalError).
export function useRemoveRegular() {
  const queryClient = useQueryClient()
  const archive = useArchiveDinner()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: (regular: Regular) => archive(regular.id),
    onMutate: async (regular) => {
      await changeOptimistically(queryClient, (list) =>
        list.map((r): CachedRegular => (r.id === regular.id ? { ...r, removing: true } : r)),
      )
    },
    onSuccess: (_data, regular) => {
      setRegulars(queryClient, (list) => list.filter((r) => r.id !== regular.id))
    },
    onError: (_error, regular) => {
      setRegulars(queryClient, (list) => list.map((r) => (r.id === regular.id ? restored(r) : r)))
    },
    onSettled: () => settle(queryClient),
  })
}

// clearedNight is day with dinner id removed: an unplanned night, which is
// what the server stores when that dinner becomes Delivery or Dining Out.
function clearedNight(day: PlanDay, id: string, mealSource: MealSource): PlanDay {
  if (day.dinner === null || day.dinner.id !== id) return day
  if (mealSource === 'cook') return { ...day, dinner: { ...day.dinner, meal_source: mealSource } }
  return { ...day, dinner: null, is_new: false, start_by: null }
}

// showMealSource shows dinner id's meal source on every plan, Home, Supply
// run and dinner detail already read here, and on a current Supply run saved
// on this device. A source other than cook takes the dinner off those plan
// days and off Home, and drops that dinner's Supply-run group, which the
// server has removed. A manual or cleared night has no dinner, so it is left
// as it is. An archived Supply run keeps the group it was shopped with. A
// read not yet made is left unread. The screens are read again afterwards.
function showMealSource(queryClient: QueryClient, id: string, mealSource: MealSource) {
  queryClient.setQueriesData<Plan | null>({ queryKey: ['plans'] }, (plan) => {
    if (plan == null || !plan.days.some((day) => day.dinner !== null && day.dinner.id === id)) return plan
    return { ...plan, days: plan.days.map((day) => clearedNight(day, id, mealSource)) }
  })
  queryClient.setQueryData<Home>(HOME_KEY, (home) => {
    if (home === undefined || home.tonight === null || home.tonight.id !== id) return home
    if (mealSource === 'cook') {
      return home.tonight.meal_source === mealSource ? home : { ...home, tonight: { ...home.tonight, meal_source: mealSource } }
    }
    return { ...home, tonight: null }
  })
  reconcileSavedMealSource(id, mealSource)
  queryClient.setQueriesData<SupplyRun | null>({ queryKey: ['shopping'] }, (run) => {
    if (run == null || !run.groups.some((group) => group.dinner.id === id)) return run
    // An archived run keeps the group it was shopped with. A current one
    // loses it, matching the server, which removes that group.
    if (mealSource === 'cook' || run.status === 'archived') {
      return {
        ...run,
        groups: run.groups.map((group) =>
          group.dinner.id === id ? { ...group, dinner: { ...group.dinner, meal_source: mealSource } } : group,
        ),
      }
    }
    const groups = run.groups.filter((group) => group.dinner.id !== id)
    let needed = 0
    let total = 0
    for (const group of groups) {
      for (const item of group.items) {
        total += 1
        if (item.needed) needed += 1
      }
    }
    return { ...run, groups, needed_count: needed, total_count: total }
  })
  queryClient.setQueryData<DinnerDetail>(dinnerDetailKey(id), (dinner) =>
    dinner === undefined || dinner.meal_source === mealSource ? dinner : { ...dinner, meal_source: mealSource },
  )
}

// useEditRegular saves changes to a Regular (PATCH /api/v1/dinners/{id})
// and shows the server's answer once it is saved. A meal-source edit also
// shows that source on the plan, Home, Supply run and dinner detail already
// read here (showMealSource), then reads them again.
export function useEditRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async ({ id, patch }: { id: string; patch: RegularPatch }) =>
      parseDinner(`PATCH ${DINNERS_PATH}/{id}`, await apiFetch<unknown>(dinnerPath(id), { method: 'PATCH', body: patch })),
    onSuccess: (dinner, { patch }) => {
      replace(queryClient, dinner)
      if (patch.meal_source !== undefined) showMealSource(queryClient, dinner.id, dinner.meal_source)
    },
    onSettled: (_dinner, _error, { id, patch }) => {
      settle(queryClient)
      if (patch.meal_source === undefined) return
      void queryClient.invalidateQueries({ queryKey: ['plans'] })
      void queryClient.invalidateQueries({ queryKey: HOME_KEY })
      void queryClient.invalidateQueries({ queryKey: ['shopping'] })
      void queryClient.invalidateQueries({ queryKey: dinnerDetailKey(id) })
    },
  })
}

// Favorite is the dinner whose heart is toggled, as it was before the toggle.
export type Favorite = { id: string; is_favorite: boolean }

// setFavorite shows dinner id's heart as is_favorite wherever it is cached:
// in the Regulars and in its detail. A cache not yet read is left unread.
function setFavorite(queryClient: QueryClient, id: string, is_favorite: boolean) {
  queryClient.setQueryData<CachedRegular[]>(REGULARS_LIST_KEY, (list) =>
    list?.map((r) => (r.id === id ? { ...r, is_favorite } : r)),
  )
  queryClient.setQueryData<DinnerDetail>(dinnerDetailKey(id), (dinner) => dinner && { ...dinner, is_favorite })
}

// useToggleFavorite toggles a dinner's favorite heart (POST
// /api/v1/dinners/{id}/favorite), a Regular's or any dinner's on its detail
// screen. The heart changes at once and changes back if the server refuses.
// The server answers with the dinner as the dinners list sends it, which
// replaces the Regular; the detail keeps its recipe and takes only the heart.
export function useToggleFavorite() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async (dinner: Favorite) =>
      parseDinner(
        `POST ${DINNERS_PATH}/{id}/favorite`,
        await apiFetch<unknown>(`${dinnerPath(dinner.id)}/favorite`, { method: 'POST' }),
      ),
    onMutate: async (dinner) => {
      await queryClient.cancelQueries({ queryKey: dinnerDetailKey(dinner.id) })
      await queryClient.cancelQueries({ queryKey: REGULARS_LIST_KEY })
      setFavorite(queryClient, dinner.id, !dinner.is_favorite)
    },
    onSuccess: (dinner) => {
      queryClient.setQueryData<CachedRegular[]>(REGULARS_LIST_KEY, (list) => list?.map((r) => (r.id === dinner.id ? answered(dinner) : r)))
      queryClient.setQueryData<DinnerDetail>(dinnerDetailKey(dinner.id), (detail) => detail && { ...detail, is_favorite: dinner.is_favorite })
    },
    onError: (_error, dinner) => setFavorite(queryClient, dinner.id, dinner.is_favorite),
    onSettled: () => settle(queryClient),
  })
}

// useGroundRegular fixes a Regular's recipe (POST /api/v1/dinners/{id}/ground)
// with a candidate, a pasted link or a recipe typed in. A candidate or link
// is read in the request, which can take up to a minute and a half.
export function useGroundRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async ({ id, fix }: { id: string; fix: RecipeFix }) =>
      parseDinner(
        `POST ${DINNERS_PATH}/{id}/ground`,
        await apiFetch<unknown>(`${dinnerPath(id)}/ground`, { method: 'POST', body: fix }),
      ),
    onSuccess: (dinner) => replace(queryClient, dinner),
    onSettled: () => settle(queryClient),
  })
}

// useRecipeCandidates is up to three recipe pages the dinner could be
// grounded in, best first (GET /api/v1/dinners/{id}/recipe-candidates).
export function useRecipeCandidates(id: string) {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: ['dinners', 'candidates', id],
    queryFn: async () => {
      const path = `${dinnerPath(id)}/recipe-candidates`
      const where = `GET ${DINNERS_PATH}/{id}/recipe-candidates`
      const body = await apiFetch<unknown>(path)
      if (!isRecord(body) || !Array.isArray(body.candidates)) throw new Error(`${where}: response has no candidates list`)
      return body.candidates.map((candidate) => parseCandidate(where, candidate))
    },
  })
}
