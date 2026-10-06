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
  recipe_url: string | null
  photo_url: string | null
  grounding_status: string
  grounding_error: string | null
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
    recipe_url: null,
    photo_url: null,
    grounding_status: 'grounded',
    grounding_error: null,
    updated_by: null,
    created_at: '2026-10-01T15:00:00.000Z',
    updated_at: '2026-10-01T15:00:00.000Z',
    ...overrides,
  }
}
