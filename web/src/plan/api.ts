import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, useApi, type ApiRead } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { oneOf } from '../dinners/api.ts'
import { QUEST_LOG_KEY } from '../discover/api.ts'

export const PLANS_PATH = '/api/v1/plans'

// planPath is the plan of the week starting on Monday weekStart.
export function planPath(weekStart: string): string {
  return `${PLANS_PATH}/${encodeURIComponent(weekStart)}`
}

// planKey is the query of the plan of the week starting on weekStart.
export function planKey(weekStart: string) {
  return ['plans', weekStart] as const
}

export const PLAN_STATUSES = ['draft', 'synced', 'changed'] as const

// PlanStatus is draft until Push to Calendar syncs the plan; an edit of a
// synced plan makes it changed.
export type PlanStatus = (typeof PLAN_STATUSES)[number]

// PlanDay is one day of a plan: its household-local date, the dinner
// planned, whether that dinner is a Quest-log pick, and, for a crock-pot
// dinner of known minutes, the instant it must start by.
export type PlanDay = {
  date: string
  dinner: { id: string; name: string }
  is_new: boolean
  start_by: string | null
}

// Plan is GET /api/v1/plans/{weekStart}: the week's seven days, Monday to
// Sunday, and the AI's note on the draft, if any. cached says the service
// worker answered from its cache: the server may have changed the plan since.
export type Plan = {
  id: string
  week_start: string
  status: PlanStatus
  ai_note: string | null
  days: PlanDay[]
  cached: boolean
}

// DRAFT_JOB_KIND is the kind of the job drafting a week's plan.
export const DRAFT_JOB_KIND = 'draft_week'

// DraftResult is the draft_week job's result: the week drafted and its plan.
export type DraftResult = { week_start: string; plan_id: string }

function parseDay(where: string, body: unknown): PlanDay {
  if (
    !isRecord(body) ||
    typeof body.date !== 'string' ||
    !isRecord(body.dinner) ||
    typeof body.dinner.id !== 'string' ||
    typeof body.dinner.name !== 'string' ||
    typeof body.is_new !== 'boolean' ||
    (body.start_by !== null && typeof body.start_by !== 'string')
  ) {
    throw new Error(`${where}: response has a day that is not a plan day`)
  }
  return { date: body.date, dinner: { id: body.dinner.id, name: body.dinner.name }, is_new: body.is_new, start_by: body.start_by }
}

function parsePlan(where: string, weekStart: string, body: unknown, cached: boolean): Plan {
  if (
    !isRecord(body) ||
    typeof body.id !== 'string' ||
    body.week_start !== weekStart ||
    (body.ai_note !== null && typeof body.ai_note !== 'string') ||
    !Array.isArray(body.days) ||
    body.days.length !== 7
  ) {
    throw new Error(`${where}: response is not the plan of the week of ${weekStart}`)
  }
  return {
    id: body.id,
    week_start: weekStart,
    status: oneOf(where, 'status', PLAN_STATUSES, body.status),
    ai_note: body.ai_note,
    days: body.days.map((day) => parseDay(where, day)),
    cached,
  }
}

// parseDraftResult checks a draft_week job's result, which useJob passes on
// unread, is the draft of the week starting on weekStart.
export function parseDraftResult(where: string, weekStart: string, body: unknown): DraftResult {
  if (!isRecord(body) || body.week_start !== weekStart || typeof body.plan_id !== 'string') {
    throw new Error(`${where}: result is not the draft of the week of ${weekStart}`)
  }
  return { week_start: weekStart, plan_id: body.plan_id }
}

// usePlan is the plan of the week starting on Monday weekStart (GET
// /api/v1/plans/{weekStart}), or null when the week has none (404).
export function usePlan(weekStart: string) {
  const { apiRead } = useApi()
  return useQuery({
    queryKey: planKey(weekStart),
    queryFn: async (): Promise<Plan | null> => {
      const path = planPath(weekStart)
      let read: ApiRead<unknown>
      try {
        read = await apiRead<unknown>(path)
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null
        throw error
      }
      return parsePlan(`GET ${path}`, weekStart, read.body, read.cached)
    },
  })
}

// useStartDraft asks the AI to draft the week starting on weekStart (POST
// /api/v1/plans/{weekStart}/draft) and resolves with the id of the
// draft_week job, which the server answers with the draft already running
// when there is one. Drafting over a plan pushed to Calendar (synced or
// changed) needs confirm.
export function useStartDraft() {
  const { apiFetch } = useApi()
  return useMutation({
    mutationFn: async ({ weekStart, confirm }: { weekStart: string; confirm: boolean }): Promise<string> => {
      const path = `${planPath(weekStart)}/draft`
      const body = await apiFetch<unknown>(path, { method: 'POST', body: confirm ? { confirm: true } : {} })
      if (!isRecord(body) || typeof body.jobId !== 'string') throw new Error(`POST ${path}: response has no jobId`)
      return body.jobId
    },
  })
}

// Swap is one day's swap: the day, and the dinner chosen for it with whether
// it was chosen from the Quest log.
export type Swap = { date: string; dinner: { id: string; name: string }; fromQuestLog: boolean }

// useSwapDay swaps the dinner of one day of the week starting on weekStart
// (PUT /api/v1/plans/{weekStart}/days/{date}). The day shows the dinner at
// once, NEW when it came from the Quest log, with no start-by time until the
// server answers with the plan as swapped, which then replaces it. A refused
// swap puts the day back as it was, unless it has changed again meanwhile.
// However the swap ends, the Quest log is read again: the server leaves a
// dinner on the plan out of it.
export function useSwapDay(weekStart: string) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  const key = planKey(weekStart)
  return useMutation({
    mutationFn: async ({ date, dinner }: Swap): Promise<Plan> => {
      const path = `${planPath(weekStart)}/days/${encodeURIComponent(date)}`
      const body = await apiFetch<unknown>(path, { method: 'PUT', body: { dinner_id: dinner.id } })
      return parsePlan(`PUT ${path}`, weekStart, body, false)
    },
    onMutate: async ({ date, dinner, fromQuestLog }) => {
      await queryClient.cancelQueries({ queryKey: key })
      const plan = queryClient.getQueryData<Plan | null>(key)
      const previous = plan?.days.find((day) => day.date === date)
      if (!plan || previous === undefined) throw new Error(`The week of ${weekStart} has no plan day ${date} to swap.`)
      const swapped: PlanDay = { date, dinner, is_new: fromQuestLog, start_by: null }
      queryClient.setQueryData<Plan>(key, { ...plan, days: plan.days.map((day) => (day.date === date ? swapped : day)) })
      return { previous, swapped }
    },
    onSuccess: (plan) => {
      queryClient.setQueryData<Plan>(key, plan)
    },
    onError: (_error, { date }, context) => {
      if (context === undefined) return
      queryClient.setQueryData<Plan | null>(key, (plan) =>
        plan && {
          ...plan,
          days: plan.days.map((day) => (day.date === date && day.dinner.id === context.swapped.dinner.id ? context.previous : day)),
        },
      )
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY }),
  })
}
