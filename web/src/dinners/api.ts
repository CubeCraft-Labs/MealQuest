import { useIsMutating, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'

export const DINNERS_PATH = '/api/v1/dinners'
export const REGULARS_PATH = `${DINNERS_PATH}?status=regular`

// GROUNDING_POLL_MS is how often the Regulars are read again while any of
// them is still finding its recipe.
export const GROUNDING_POLL_MS = 3000

// Methods and Difficulties are the values the dinners API allows.
export const METHODS = ['stovetop', 'oven', 'crockpot', 'grill', 'other'] as const
export const DIFFICULTIES = ['easy', 'medium', 'hard'] as const
const GROUNDING_STATUSES = ['pending', 'grounded', 'failed', 'manual'] as const

export type Method = (typeof METHODS)[number]
export type Difficulty = (typeof DIFFICULTIES)[number]
export type GroundingStatus = (typeof GROUNDING_STATUSES)[number]

// Regular is the part of a dinner the Regulars screens read. photo_id is
// the stored photo's id, from photo_url. pending is true while an
// optimistic add waits for the server; its id is not the server's.
export type Regular = {
  id: string
  name: string
  method: Method | null
  minutes_total: number | null
  difficulty: Difficulty | null
  is_favorite: boolean
  photo_id: string | undefined
  grounding_status: GroundingStatus
  pending?: true
}

// NewRegular is a POST /api/v1/dinners body: the household's name for the
// dinner, with the recipe link to ground it in when one was pasted. A
// dinner is never named from its link, which may carry private details.
export type NewRegular = { name: string; recipe_url?: string }

// RegularPatch is a PATCH /api/v1/dinners/{id} body; every field is optional.
export type RegularPatch = Partial<{ name: string; method: Method; minutes_total: number; difficulty: Difficulty }>

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

// CachedRegular is a Regular as the cache holds it. removing is true while
// an optimistic removal waits for the server: the entry keeps its place, so
// a refused removal returns exactly there whatever else changed meanwhile.
type CachedRegular = Regular & { removing?: true }

function shown(list: CachedRegular[]): Regular[] {
  return list.filter((regular) => !regular.removing)
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
  return {
    id: body.id,
    name: body.name,
    method: nullable(body.method, (v) => oneOf(where, 'method', METHODS, v)),
    minutes_total: nullable(body.minutes_total, (v) => wholeNumber(where, 'minutes_total', v)),
    difficulty: nullable(body.difficulty, (v) => oneOf(where, 'difficulty', DIFFICULTIES, v)),
    is_favorite: body.is_favorite,
    photo_id: photoId(where, body.photo_url),
    grounding_status: oneOf(where, 'grounding_status', GROUNDING_STATUSES, body.grounding_status),
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

// useRegulars is the household's Regulars, in the server's order. While any
// of them is still finding its recipe it is read again every
// GROUNDING_POLL_MS, except while any change is in flight (whose settling
// reads it again anyway), so a poll never overwrites a change being saved.
// Watching the changes in flight re-renders the hook as each one starts and
// settles, which pauses and resumes the poll.
export function useRegulars() {
  const { apiFetch } = useApi()
  const changing = useIsMutating({ mutationKey: REGULARS_MUTATION_KEY }) > 0
  return useQuery({
    queryKey: REGULARS_LIST_KEY,
    queryFn: async () => {
      const where = `GET ${REGULARS_PATH}`
      const body = await apiFetch<unknown>(REGULARS_PATH)
      if (!isRecord(body) || !Array.isArray(body.dinners)) throw new Error(`${where}: response has no dinners list`)
      return body.dinners.map((dinner): CachedRegular => parseDinner(where, dinner))
    },
    select: shown,
    refetchInterval: (query) => {
      const list = query.state.data
      if (list === undefined || changing) return false
      return list.some((regular) => regular.grounding_status === 'pending') ? GROUNDING_POLL_MS : false
    },
  })
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
  setRegulars(queryClient, (list) => list.map((regular) => (regular.id === dinner.id ? dinner : regular)))
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
        pending: true,
      }
      await changeOptimistically(queryClient, (list) => [...list, optimistic])
      return { id }
    },
    onSuccess: (dinner, _add, context) => {
      setRegulars(queryClient, (list) => list.map((regular) => (regular.id === context.id ? dinner : regular)))
    },
    onError: (_error, _add, context) => {
      if (context === undefined) return
      setRegulars(queryClient, (list) => list.filter((regular) => regular.id !== context.id))
    },
    onSettled: () => settle(queryClient),
  })
}

// useRemoveRegular archives a Regular (DELETE /api/v1/dinners/{id}). It
// leaves the list at once and is put back in its place if the server
// refuses.
export function useRemoveRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async (regular: Regular) => {
      await apiFetch<unknown>(dinnerPath(regular.id), { method: 'DELETE' })
    },
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

// useEditRegular saves changes to a Regular (PATCH /api/v1/dinners/{id})
// and shows the server's answer once it is saved.
export function useEditRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async ({ id, patch }: { id: string; patch: RegularPatch }) =>
      parseDinner(`PATCH ${DINNERS_PATH}/{id}`, await apiFetch<unknown>(dinnerPath(id), { method: 'PATCH', body: patch })),
    onSuccess: (dinner) => replace(queryClient, dinner),
    onSettled: () => settle(queryClient),
  })
}

// useToggleFavorite toggles a Regular's favorite heart (POST
// /api/v1/dinners/{id}/favorite). The heart changes at once and changes
// back if the server refuses.
export function useToggleFavorite() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  const set = (id: string, is_favorite: boolean) =>
    setRegulars(queryClient, (list) => list.map((r) => (r.id === id ? { ...r, is_favorite } : r)))
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async (regular: Regular) =>
      parseDinner(
        `POST ${DINNERS_PATH}/{id}/favorite`,
        await apiFetch<unknown>(`${dinnerPath(regular.id)}/favorite`, { method: 'POST' }),
      ),
    onMutate: async (regular) => {
      await changeOptimistically(queryClient, (list) =>
        list.map((r) => (r.id === regular.id ? { ...r, is_favorite: !regular.is_favorite } : r)),
      )
    },
    onSuccess: (dinner) => replace(queryClient, dinner),
    onError: (_error, regular) => set(regular.id, regular.is_favorite),
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
