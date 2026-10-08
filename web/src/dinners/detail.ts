import { useQuery } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import {
  DIFFICULTIES,
  dinnerPath,
  DINNERS_PATH,
  GROUNDING_POLL_MS,
  GROUNDING_STATUSES,
  METHODS,
  oneOf,
  photoId,
  type Difficulty,
  type GroundingStatus,
  type Method,
} from './api.ts'

// The statuses a dinner with detail can have; an archived dinner is 404.
const STATUSES = ['suggested', 'quest_log', 'tried', 'regular'] as const

export type DinnerStatus = (typeof STATUSES)[number]

// Ingredient is one recipe line. raw_text is the line as the recipe gives
// it; name, qty, unit and note are the server's reading of it, each null
// when it has none — always name, qty and unit for a recipe typed in.
export type Ingredient = {
  name: string | null
  qty: string | null
  unit: string | null
  note: string | null
  raw_text: string
}

// DinnerDetail is a dinner with its recipe (GET /api/v1/dinners/{id}).
// siteName is the site the recipe was fetched from, or null when there is
// none to name: a recipe typed in, a page that named no site, or a name the
// server withheld because it could have come from the link. The recipe's
// link itself never reaches the client.
export type DinnerDetail = {
  id: string
  name: string
  status: DinnerStatus
  method: Method | null
  minutes_total: number | null
  difficulty: Difficulty | null
  is_favorite: boolean
  photo_id: string | undefined
  ingredients: Ingredient[]
  steps: string[]
  siteName: string | null
  grounding_status: GroundingStatus
  why_picked: string | null
}

function text(where: string, field: string, value: unknown): string {
  if (typeof value !== 'string') throw new Error(`${where}: ${field} is not a string`)
  return value
}

function nullableText(where: string, field: string, value: unknown): string | null {
  return value === null ? null : text(where, field, value)
}

function parseIngredient(where: string, body: unknown): Ingredient {
  if (!isRecord(body)) throw new Error(`${where}: ingredient is not an object`)
  return {
    name: nullableText(where, 'ingredient name', body.name),
    qty: nullableText(where, 'ingredient qty', body.qty),
    unit: nullableText(where, 'ingredient unit', body.unit),
    note: nullableText(where, 'ingredient note', body.note),
    raw_text: text(where, 'ingredient raw_text', body.raw_text),
  }
}

// detailPhotoId is the photo id of photo_urls, which the server sends as
// both cached sizes of one photo, or null.
function detailPhotoId(where: string, value: unknown): string | undefined {
  if (value === null) return undefined
  if (!isRecord(value)) throw new Error(`${where}: photo_urls is not an object`)
  const id = photoId(where, value['400'])
  if (id === undefined || value['1200'] !== `/media/photos/${encodeURIComponent(id)}-1200.jpg`) {
    throw new Error(`${where}: photo_urls is not both sizes of one stored photo`)
  }
  return id
}

function siteName(where: string, value: unknown): string | null {
  if (value === null) return null
  if (!isRecord(value)) throw new Error(`${where}: source is not an object`)
  return nullableText(where, 'source site_name', value.site_name)
}

// parseDinnerDetail checks a dinner with its recipe as the API sends it.
export function parseDinnerDetail(where: string, body: unknown): DinnerDetail {
  if (!isRecord(body) || typeof body.id !== 'string' || typeof body.name !== 'string') {
    throw new Error(`${where}: response is not a dinner`)
  }
  if (typeof body.is_favorite !== 'boolean') throw new Error(`${where}: is_favorite is not a boolean`)
  if (!Array.isArray(body.ingredients) || !Array.isArray(body.steps)) throw new Error(`${where}: response has no recipe lists`)
  const minutes = body.minutes_total
  if (minutes !== null && (typeof minutes !== 'number' || !Number.isInteger(minutes))) {
    throw new Error(`${where}: minutes_total is not a whole number`)
  }
  return {
    id: body.id,
    name: body.name,
    status: oneOf(where, 'status', STATUSES, body.status),
    method: body.method === null ? null : oneOf(where, 'method', METHODS, body.method),
    minutes_total: minutes,
    difficulty: body.difficulty === null ? null : oneOf(where, 'difficulty', DIFFICULTIES, body.difficulty),
    is_favorite: body.is_favorite,
    photo_id: detailPhotoId(where, body.photo_urls),
    ingredients: body.ingredients.map((ingredient) => parseIngredient(where, ingredient)),
    steps: body.steps.map((step) => text(where, 'step', step)),
    siteName: siteName(where, body.source),
    grounding_status: oneOf(where, 'grounding_status', GROUNDING_STATUSES, body.grounding_status),
    why_picked: nullableText(where, 'why_picked', body.why_picked),
  }
}

// useDinnerDetail is dinner id with its recipe (GET /api/v1/dinners/{id}).
// While the dinner is still finding its recipe it is read again every
// GROUNDING_POLL_MS. An unknown or archived dinner fails with the API's 404.
// It runs even while the browser reports offline, so the copy the service
// worker saved is shown, or the read fails, instead of waiting for the
// network.
export function useDinnerDetail(id: string) {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: ['dinners', 'detail', id],
    queryFn: async () => parseDinnerDetail(`GET ${DINNERS_PATH}/{id}`, await apiFetch<unknown>(dinnerPath(id))),
    networkMode: 'always',
    refetchInterval: (query) => (query.state.data?.grounding_status === 'pending' ? GROUNDING_POLL_MS : false),
  })
}
