import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError, useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { parseDinner } from '../dinners/api.ts'
import { QUEST_LOG_KEY } from '../discover/api.ts'
import { parsePlan, planKey } from '../plan/api.ts'

export const IDEAS_PATH = '/api/v1/ideas'
export const FROM_INGREDIENTS_PATH = `${IDEAS_PATH}/from-ingredients`

// FROM_INGREDIENTS_JOB_KIND is the kind of the job finding ideas from the
// ingredients on hand.
export const FROM_INGREDIENTS_JOB_KIND = 'ideas_from_ingredients'

// IDEAS_KEPT is the most ideas a job keeps (skills.IdeasKept), so the most
// cards a search fills.
export const IDEAS_KEPT = 5

// MAX_INGREDIENTS is the most ingredients the household may list, and
// INGREDIENT_LIMIT the most characters one may have
// (skills.MaxIngredients, skills.IngredientLimit).
export const MAX_INGREDIENTS = 20
export const INGREDIENT_LIMIT = 80

// IDEA_STAGES are a job's stages: asking the AI for ideas, grounding them
// in recipes, and done.
export const IDEA_STAGES = ['suggesting', 'grounding', 'done'] as const
export type IdeaStage = (typeof IDEA_STAGES)[number]

// Idea is a grounded idea as a result card shows it. summary is the
// server's "Uses all 5 · missing: 1 lime" line, shown as it is;
// minutes_total is the grounded recipe's, null when it does not say.
export type Idea = {
  name: string
  uses: string[]
  missing: string[]
  summary: string
  minutes_total: number | null
  quick: boolean
}

// IdeasResult is an ideas_from_ingredients job's result, published as it
// goes: the ideas grounded so far, in the order the server shows them.
export type IdeasResult = {
  stage: IdeaStage
  ingredients: string[]
  quick_only: boolean
  ideas: Idea[]
  dropped: number
}

// IDEAS_JOB_FAILURES says why an ideas job failed, by its failure code.
// no_ideas_found is no fault to fix, but the search still came back empty.
const IDEAS_JOB_FAILURES: Record<string, string> = {
  no_ideas_found: 'No ideas came back with a recipe to match.',
  ai_failed: "The AI assistant couldn't come up with ideas.",
  search_not_configured: 'Recipe search is not set up. An admin can set it up in Settings.',
  search_failed: "The recipe search couldn't be reached.",
  internal_error: 'Something went wrong on the server.',
}

// ideasJobFailure says why an ideas job failed; an unknown code is shown
// as it is.
export function ideasJobFailure(code: string): string {
  return IDEAS_JOB_FAILURES[code] ?? code
}

// ideasJobLost says a poll of an ideas job failed because the server no
// longer has the job (404), so asking for it again will not find it: the
// search must be started again.
export function ideasJobLost(error: Error | null): boolean {
  return error instanceof ApiError && error.status === 404
}

// Search is what Find ideas asks for.
export type Search = { ingredients: string[]; quickOnly: boolean }

function isStrings(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((v) => typeof v === 'string')
}

function parseIdea(where: string, body: unknown): Idea {
  if (
    !isRecord(body) ||
    typeof body.name !== 'string' ||
    !isStrings(body.uses) ||
    !isStrings(body.missing) ||
    typeof body.summary !== 'string' ||
    typeof body.quick !== 'boolean' ||
    (body.minutes_total !== null && !Number.isInteger(body.minutes_total))
  ) {
    throw new Error(`${where}: result has an idea that is not a grounded idea`)
  }
  return {
    name: body.name,
    uses: body.uses,
    missing: body.missing,
    summary: body.summary,
    minutes_total: body.minutes_total as number | null,
    quick: body.quick,
  }
}

// parseIdeasResult checks an ideas_from_ingredients job's result, which
// useJob passes on unread, answers the search asked for.
export function parseIdeasResult(where: string, quickOnly: boolean, body: unknown): IdeasResult {
  if (
    !isRecord(body) ||
    typeof body.stage !== 'string' ||
    !(IDEA_STAGES as readonly string[]).includes(body.stage) ||
    !isStrings(body.ingredients) ||
    body.quick_only !== quickOnly ||
    !Array.isArray(body.ideas) ||
    typeof body.dropped !== 'number'
  ) {
    throw new Error(`${where}: result is not ideas from ingredients`)
  }
  return {
    stage: body.stage as IdeaStage,
    ingredients: body.ingredients,
    quick_only: quickOnly,
    ideas: body.ideas.map((idea) => parseIdea(where, idea)),
    dropped: body.dropped,
  }
}

// useFindIdeas starts finding ideas from the ingredients on hand (POST
// /api/v1/ideas/from-ingredients) and resolves with the id of the job,
// which GET /api/v1/jobs/{id} reports on.
export function useFindIdeas() {
  const { apiFetch } = useApi()
  return useMutation({
    mutationFn: async ({ ingredients, quickOnly }: Search): Promise<string> => {
      const body = await apiFetch<unknown>(FROM_INGREDIENTS_PATH, {
        method: 'POST',
        body: { ingredients, quick_only: quickOnly },
      })
      if (!isRecord(body) || typeof body.jobId !== 'string') throw new Error(`POST ${FROM_INGREDIENTS_PATH}: response has no jobId`)
      return body.jobId
    },
  })
}

// IdeaRef is idea index of the succeeded ideas job jobId.
export type IdeaRef = { jobId: string; index: number }

function ideaPath({ jobId, index }: IdeaRef, action: 'quest-log' | 'cook-tonight'): string {
  return `${IDEAS_PATH}/${encodeURIComponent(jobId)}/items/${index}/${action}`
}

// useAddIdeaToQuestLog adds an idea to the Quest log (POST
// /api/v1/ideas/{jobId}/items/{i}/quest-log) and resolves with the
// dinner's name. Asking again answers the same dinner. The Quest log is
// read again after it.
export function useAddIdeaToQuestLog() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (idea: IdeaRef): Promise<string> => {
      const path = ideaPath(idea, 'quest-log')
      return parseDinner(`POST ${path}`, await apiFetch<unknown>(path, { method: 'POST' })).name
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY }),
  })
}

// useCookIdeaTonight puts an idea on today's plan (POST
// /api/v1/ideas/{jobId}/items/{i}/cook-tonight) and resolves with the
// dinner's name. The plan it answers with is cached as its week's plan,
// and the Quest log is read again. With no plan for the current week the
// server answers 409 no_plan, the mutation's ApiError, and changes nothing.
export function useCookIdeaTonight() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (idea: IdeaRef): Promise<string> => {
      const path = ideaPath(idea, 'cook-tonight')
      const where = `POST ${path}`
      const body = await apiFetch<unknown>(path, { method: 'POST' })
      if (!isRecord(body) || !isRecord(body.plan) || typeof body.plan.week_start !== 'string') {
        throw new Error(`${where}: response has no plan`)
      }
      const dinner = parseDinner(where, body.dinner)
      const plan = parsePlan(where, body.plan.week_start, body.plan, false)
      queryClient.setQueryData(planKey(plan.week_start), plan)
      return dinner.name
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY }),
  })
}
