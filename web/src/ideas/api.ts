import { useCallback } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { DIFFICULTIES, METHODS, parseDinner, photoId, type Difficulty, type Method, type Regular } from '../dinners/api.ts'
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

// QUICK_PATH starts finding a page of Quick dinner ideas, and
// QUICK_JOB_KIND is the kind of the job that finds it.
export const QUICK_PATH = `${IDEAS_PATH}/quick`
export const QUICK_JOB_KIND = 'quick_ideas'

// QUICK_KINDS are the kinds of Quick dinner, the tabs of Quick dinners in
// order (skills.QuickKinds): cooked in a crock pot, or 30 minutes or less
// from prep to table.
export const QUICK_KINDS = ['crockpot', 'under30'] as const
export type QuickKind = (typeof QUICK_KINDS)[number]

// QUICK_IDEAS_KEPT is the most ideas one page of Quick dinners shows
// (skills.QuickIdeasKept), and MAX_EXCLUDE the most names a request may
// exclude (skills.MaxExclude).
export const QUICK_IDEAS_KEPT = 6
export const MAX_EXCLUDE = 60

// QuickIdea is a grounded Quick dinner idea as a row shows it. method,
// minutes_total and difficulty are the grounded recipe's; minutes_total is
// null when the recipe does not say, which only a crock-pot idea may be.
// photo_id is the stored photo's, from photo_url.
export type QuickIdea = {
  name: string
  method: Method
  minutes_total: number | null
  difficulty: Difficulty
  photo_id: string | undefined
}

// QuickResult is a quick_ideas job's result, published as it goes: the
// ideas grounded so far, in the order the server shows them.
export type QuickResult = {
  stage: IdeaStage
  kind: QuickKind
  ideas: QuickIdea[]
}

function parseQuickIdea(where: string, body: unknown): QuickIdea {
  if (
    !isRecord(body) ||
    typeof body.name !== 'string' ||
    !(METHODS as readonly unknown[]).includes(body.method) ||
    (body.minutes_total !== null && !Number.isInteger(body.minutes_total)) ||
    !(DIFFICULTIES as readonly unknown[]).includes(body.difficulty)
  ) {
    throw new Error(`${where}: result has an idea that is not a grounded Quick dinner`)
  }
  return {
    name: body.name,
    method: body.method as Method,
    minutes_total: body.minutes_total as number | null,
    difficulty: body.difficulty as Difficulty,
    photo_id: photoId(where, body.photo_url),
  }
}

// parseQuickResult checks a quick_ideas job's result, which useJob passes
// on unread, is a page of the kind asked for.
export function parseQuickResult(where: string, kind: QuickKind, body: unknown): QuickResult {
  if (
    !isRecord(body) ||
    typeof body.stage !== 'string' ||
    !(IDEA_STAGES as readonly string[]).includes(body.stage) ||
    body.kind !== kind ||
    !isStrings(body.exclude) ||
    !Array.isArray(body.ideas) ||
    typeof body.dropped !== 'number'
  ) {
    throw new Error(`${where}: result is not Quick dinner ideas of kind ${kind}`)
  }
  return { stage: body.stage as IdeaStage, kind, ideas: body.ideas.map((idea) => parseQuickIdea(where, idea)) }
}

// QuickPage is a page of Quick dinner ideas its job found.
export type QuickPage = { jobId: string; ideas: QuickIdea[] }

// QuickTab is what one tab of Quick dinners has found this session: the
// pages found, the job finding the next one, whether the server has said
// there are no more, why the last page asked for could not start, kept
// until Retry asks again, and the status of the dinner each idea added
// came back as, by "jobId:index".
export type QuickTab = {
  pages: QuickPage[]
  jobId: string | null
  ended: boolean
  startError: Error | null
  added: Record<string, AddedStatus>
}

const NEW_TAB: QuickTab = { pages: [], jobId: null, ended: false, startError: null, added: {} }

export function quickTabKey(kind: QuickKind) {
  return ['ideas', 'quick', kind] as const
}

// shownNames are the names of every idea a tab shows, which its next page
// excludes so that nothing repeats.
export function shownNames(tab: QuickTab): string[] {
  return tab.pages.flatMap((page) => page.ideas.map((idea) => idea.name))
}

// useQuickTab is a tab's QuickTab, held in the query cache for the session
// (never stale, never collected) so that leaving the tab or the screen
// keeps it, and update, which changes it. Nothing is fetched: a refetch
// reads back what the tab holds.
export function useQuickTab(kind: QuickKind) {
  const queryClient = useQueryClient()
  const { data } = useQuery({
    queryKey: quickTabKey(kind),
    queryFn: () => queryClient.getQueryData<QuickTab>(quickTabKey(kind)) ?? NEW_TAB,
    initialData: NEW_TAB,
    staleTime: Infinity,
    gcTime: Infinity,
  })
  const update = useCallback(
    (change: (tab: QuickTab) => QuickTab) => queryClient.setQueryData<QuickTab>(quickTabKey(kind), (tab) => change(tab ?? NEW_TAB)),
    [queryClient, kind],
  )
  return [data, update] as const
}

// useFindQuickIdeas starts finding a page of Quick dinner ideas of kind,
// none named in exclude (POST /api/v1/ideas/quick), and makes its job the
// tab's job, or its failure the tab's startError, also if the screen has
// been left meanwhile. Its mutationKey is the tab's, so a tab shown again
// knows a page is still being asked for.
export function useFindQuickIdeas(kind: QuickKind) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: quickTabKey(kind),
    mutationFn: async (exclude: string[]): Promise<string> => {
      queryClient.setQueryData<QuickTab>(quickTabKey(kind), (tab) => ({ ...(tab ?? NEW_TAB), startError: null }))
      const body = await apiFetch<unknown>(QUICK_PATH, { method: 'POST', body: { kind, exclude } })
      if (!isRecord(body) || typeof body.jobId !== 'string') throw new Error(`POST ${QUICK_PATH}: response has no jobId`)
      const jobId = body.jobId
      queryClient.setQueryData<QuickTab>(quickTabKey(kind), (tab) => ({ ...(tab ?? NEW_TAB), jobId }))
      return jobId
    },
    onError: (startError: Error) => queryClient.setQueryData<QuickTab>(quickTabKey(kind), (tab) => ({ ...(tab ?? NEW_TAB), startError })),
  })
}

// IdeaRef is idea index of the succeeded ideas job jobId.
export type IdeaRef = { jobId: string; index: number }

function ideaPath({ jobId, index }: IdeaRef, action: 'quest-log' | 'cook-tonight'): string {
  return `${IDEAS_PATH}/${encodeURIComponent(jobId)}/items/${index}/${action}`
}

// ADDED_STATUSES are the statuses a dinner an idea is added as may have:
// quest_log for a dinner made or already in the Quest log, regular for a
// Regular the idea repeats, which the server answers as it is.
export const ADDED_STATUSES = ['quest_log', 'regular'] as const
export type AddedStatus = (typeof ADDED_STATUSES)[number]

// ADDED_LABEL says where an idea added went, by its dinner's status.
export const ADDED_LABEL: Record<AddedStatus, string> = {
  quest_log: 'In the Quest log',
  regular: 'Already a Regular',
}

// AddedDinner is the dinner an idea was added as, with its status.
export type AddedDinner = Regular & { status: AddedStatus }

function parseAddedDinner(where: string, body: unknown): AddedDinner {
  const dinner = parseDinner(where, body)
  const status = isRecord(body) ? body.status : undefined
  if (!(ADDED_STATUSES as readonly unknown[]).includes(status)) {
    throw new Error(`${where}: status ${JSON.stringify(status)} is not one of ${ADDED_STATUSES.join(', ')}`)
  }
  return { ...dinner, status: status as AddedStatus }
}

// useAddIdeaToQuestLog adds an idea to the Quest log (POST
// /api/v1/ideas/{jobId}/items/{i}/quest-log) and resolves with the dinner
// it came back as: a dinner the idea repeats is answered as it is, so a
// Regular stays a Regular, and its status says which. Asking again answers
// the same dinner. The Quest log is read again after it, and onAdded told
// with the dinner, also if the caller has unmounted meanwhile.
export function useAddIdeaToQuestLog(onAdded?: (idea: IdeaRef, dinner: AddedDinner) => void) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (idea: IdeaRef): Promise<AddedDinner> => {
      const path = ideaPath(idea, 'quest-log')
      return parseAddedDinner(`POST ${path}`, await apiFetch<unknown>(path, { method: 'POST' }))
    },
    onSuccess: (dinner, idea) => {
      onAdded?.(idea, dinner)
      return queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })
    },
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
