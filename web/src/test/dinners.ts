// DinnerBody is a dinner as the dinners API sends it.
export type DinnerBody = {
  id: string
  name: string
  status: string
  origin: string
  method: string | null
  minutes_total: number | null
  difficulty: string | null
  cuisine: string | null
  is_favorite: boolean
  recipe_id: string | null
  photo_url: string | null
  grounding_status: string
  grounding_error: string | null
  photo_backfill_eligible: boolean
  updated_by: string | null
  created_at: string
  updated_at: string
}

// dinnerBody is a Regular as the server sends it: grounded with no photo,
// method or minutes unless overrides set them.
export function dinnerBody(id: string, name: string, overrides: Partial<DinnerBody> = {}): DinnerBody {
  return {
    id,
    name,
    status: 'regular',
    origin: 'regular',
    method: null,
    minutes_total: null,
    difficulty: null,
    cuisine: null,
    is_favorite: false,
    recipe_id: null,
    photo_url: null,
    grounding_status: 'grounded',
    grounding_error: null,
    photo_backfill_eligible: false,
    updated_by: null,
    created_at: '2026-10-01T15:00:00.000Z',
    updated_at: '2026-10-01T15:00:00.000Z',
    ...overrides,
  }
}

// recipeBody is dinner id with its recipe as GET /api/v1/dinners/{id} sends
// it: a Regular, typed in, with one ingredient and one step unless overrides
// set them.
export function recipeBody(id: string, name: string, overrides: Record<string, unknown> = {}) {
  return {
    ...dinnerBody(id, name),
    photo_urls: null,
    ingredients: [{ name: 'Salt', qty: null, unit: null, note: null, raw_text: 'Salt' }],
    steps: [`Cook the ${name.toLowerCase()}.`],
    source: null,
    why_picked: null,
    ...overrides,
  }
}
