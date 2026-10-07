import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { DIFFICULTIES, DINNERS_PATH, oneOf, photoId, type Difficulty } from '../dinners/api.ts'

export const SUGGESTIONS_PATH = '/api/v1/suggestions'
export const DECK_PATH = `${SUGGESTIONS_PATH}/deck`
export const BATCHES_PATH = `${SUGGESTIONS_PATH}/batches`
export const QUEST_LOG_PATH = `${DINNERS_PATH}?status=quest_log`

// DECK_KEY is the deck's query; QUEST_LOG_KEY the Quest log's.
export const DECK_KEY = ['suggestions', 'deck'] as const
export const QUEST_LOG_KEY = ['dinners', 'quest_log'] as const

// SideQuest is a pending suggestion as the deck shows it. photo_id is the
// stored photo's id, from photo_url; reason is the "Why this?".
export type SideQuest = {
  id: string
  name: string
  photo_id: string | undefined
  difficulty: Difficulty | null
  reason: string
}

// Choice is the household's decision on a Side quest: Try it or Pass.
export type Choice = 'try' | 'pass'

// Batch is a POST /api/v1/suggestions/batches answer: the batch and the job
// running it, which GET /api/v1/jobs/{id} reports on.
export type Batch = { batch_id: string; job_id: string }

// BatchResult is the suggestion batch job's result, published as it goes.
export type BatchResult = { batch_id: string; stage: string; suggestion_ids: string[]; dropped: number }

function parseSideQuest(where: string, body: unknown): SideQuest {
  if (
    !isRecord(body) ||
    typeof body.id !== 'string' ||
    typeof body.name !== 'string' ||
    typeof body.reason !== 'string'
  ) {
    throw new Error(`${where}: response is not a suggestion`)
  }
  return {
    id: body.id,
    name: body.name,
    photo_id: photoId(where, body.photo_url),
    difficulty: body.difficulty === null ? null : oneOf(where, 'difficulty', DIFFICULTIES, body.difficulty),
    reason: body.reason,
  }
}

// useDeck is the pending Side quests, oldest first (GET
// /api/v1/suggestions/deck).
export function useDeck() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: DECK_KEY,
    queryFn: async () => {
      const where = `GET ${DECK_PATH}`
      const body = await apiFetch<unknown>(DECK_PATH)
      if (!isRecord(body) || !Array.isArray(body.suggestions)) throw new Error(`${where}: response has no suggestions list`)
      return body.suggestions.map((suggestion) => parseSideQuest(where, suggestion))
    },
  })
}

// useQuestLogCount is how many dinners are in the Quest log (GET
// /api/v1/dinners?status=quest_log).
export function useQuestLogCount() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: QUEST_LOG_KEY,
    queryFn: async () => {
      const body = await apiFetch<unknown>(QUEST_LOG_PATH)
      if (!isRecord(body) || !Array.isArray(body.dinners)) throw new Error(`GET ${QUEST_LOG_PATH}: response has no dinners list`)
      return body.dinners.length
    },
  })
}

// useStartBatch asks for more Side quests now (POST
// /api/v1/suggestions/batches). The server answers with the batch already
// running when there is one.
export function useStartBatch() {
  const { apiFetch } = useApi()
  return useMutation({
    mutationFn: async (): Promise<Batch> => {
      const body = await apiFetch<unknown>(BATCHES_PATH, { method: 'POST' })
      if (!isRecord(body) || typeof body.batch_id !== 'string' || typeof body.job_id !== 'string') {
        throw new Error(`POST ${BATCHES_PATH}: response is not a batch`)
      }
      return { batch_id: body.batch_id, job_id: body.job_id }
    },
  })
}

// useDecide records Try it (POST /api/v1/suggestions/{id}/accept) or Pass
// (POST /api/v1/suggestions/{id}/decline), then reads the deck and the
// Quest log again.
export function useDecide() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, choice }: { id: string; choice: Choice }) => {
      const action = choice === 'try' ? 'accept' : 'decline'
      await apiFetch<unknown>(`${SUGGESTIONS_PATH}/${encodeURIComponent(id)}/${action}`, { method: 'POST' })
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: DECK_KEY }),
        queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY }),
      ])
    },
  })
}
