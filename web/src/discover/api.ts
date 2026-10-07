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

// SideQuest is a pending suggestion as the deck shows it. dinner_id is the
// dinner Try it moves to the Quest log; photo_id is the stored photo's id,
// from photo_url; reason is the "Why this?".
export type SideQuest = {
  id: string
  dinner_id: string
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

// BATCH_JOB_KIND is the kind of the job running a suggestion batch.
export const BATCH_JOB_KIND = 'suggestion_batch'

// BatchResult is the suggestion batch job's result, published as it goes.
export type BatchResult = { batch_id: string; stage: string; suggestion_ids: string[]; dropped: number }

// QuestLogDinner is a dinner in the Quest log, as the Quest log pill lists it.
export type QuestLogDinner = { id: string; name: string }

// DECISION_STATUS is the status a suggestion has once each choice is saved.
const DECISION_STATUS: Record<Choice, string> = { try: 'accepted', pass: 'declined' }

// parseBatchResult checks a suggestion batch job's result, which useJob
// passes on unread.
export function parseBatchResult(where: string, body: unknown): BatchResult {
  if (
    !isRecord(body) ||
    typeof body.batch_id !== 'string' ||
    typeof body.stage !== 'string' ||
    !Array.isArray(body.suggestion_ids) ||
    !body.suggestion_ids.every((id) => typeof id === 'string') ||
    typeof body.dropped !== 'number'
  ) {
    throw new Error(`${where}: result is not a suggestion batch`)
  }
  return { batch_id: body.batch_id, stage: body.stage, suggestion_ids: body.suggestion_ids as string[], dropped: body.dropped }
}

function parseSideQuest(where: string, body: unknown): SideQuest {
  if (
    !isRecord(body) ||
    typeof body.id !== 'string' ||
    typeof body.dinner_id !== 'string' ||
    typeof body.name !== 'string' ||
    typeof body.reason !== 'string'
  ) {
    throw new Error(`${where}: response is not a suggestion`)
  }
  return {
    id: body.id,
    dinner_id: body.dinner_id,
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

// useQuestLog is the dinners in the Quest log, in the server's order (GET
// /api/v1/dinners?status=quest_log).
export function useQuestLog() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: QUEST_LOG_KEY,
    queryFn: async (): Promise<QuestLogDinner[]> => {
      const where = `GET ${QUEST_LOG_PATH}`
      const body = await apiFetch<unknown>(QUEST_LOG_PATH)
      if (!isRecord(body) || !Array.isArray(body.dinners)) throw new Error(`${where}: response has no dinners list`)
      return body.dinners.map((dinner) => {
        if (!isRecord(dinner) || typeof dinner.id !== 'string' || typeof dinner.name !== 'string') {
          throw new Error(`${where}: response is not a dinner`)
        }
        return { id: dinner.id, name: dinner.name }
      })
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

// DecisionError is a failed decision, with what the deck read after it
// found: pending is its Side quest while the server still has it pending,
// null once it is decided, and undefined when that read failed too (the
// deck query holds the read's error).
export class DecisionError extends Error {
  readonly pending: SideQuest | null | undefined

  constructor(cause: unknown, pending: SideQuest | null | undefined) {
    super(cause instanceof Error ? cause.message : String(cause), { cause })
    this.name = 'DecisionError'
    this.pending = pending
  }
}

// useDecide records Try it (POST /api/v1/suggestions/{id}/accept) or Pass
// (POST /api/v1/suggestions/{id}/decline) on the Side quest the household
// decided on, whether or not a read of the deck has since taken it away.
// The answer must be that suggestion, decided that way; the mutation's data
// is the id of the replenish job the decision started, or null.
//
// The decision is applied at once: the Side quest leaves the deck and, on
// Try it, its dinner joins the Quest log unless it is there already.
// Nothing else is worked out here: the server is the record of both. However
// the decision ends, saved or failed, the deck and the Quest log are read
// again, and the decision is pending until those reads land, so the next one
// (which DiscoverPage locks out until then) cannot start under them. A read
// under way when the decision ends is replaced by the read made after it,
// and DiscoverPage's batch reads wait for that one rather than replace it.
// A failed decision is a DecisionError, settled on the read it made and
// waited for itself: a Side quest that read still has is still pending, and
// goes back on top of the deck.
export function useDecide() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()

  // send POSTs the decision and checks the answer.
  async function send(id: string, choice: Choice): Promise<string | null> {
    const action = choice === 'try' ? 'accept' : 'decline'
    const path = `${SUGGESTIONS_PATH}/${encodeURIComponent(id)}/${action}`
    const body = await apiFetch<unknown>(path, { method: 'POST' })
    if (
      !isRecord(body) ||
      !isRecord(body.suggestion) ||
      body.suggestion.id !== id ||
      body.suggestion.status !== DECISION_STATUS[choice]
    ) {
      throw new Error(`POST ${path}: response is not the ${DECISION_STATUS[choice]} suggestion`)
    }
    if (body.replenish_job_id !== null && typeof body.replenish_job_id !== 'string') {
      throw new Error(`POST ${path}: response has no replenish_job_id`)
    }
    return body.replenish_job_id
  }

  return useMutation<string | null, DecisionError, { quest: SideQuest; choice: Choice }>({
    mutationFn: async ({ quest, choice }) => {
      try {
        return await send(quest.id, choice)
      } catch (error) {
        // The deck is read again as the decision's own fetch, replacing any
        // read already out; its answer, not the cache's, settles it.
        let read: SideQuest[]
        try {
          read = await queryClient.getQueryCache().build<SideQuest[]>(queryClient, { queryKey: DECK_KEY }).fetch(undefined, { cancelRefetch: true })
        } catch {
          // The deck query holds the read's error, shown with the deck.
          throw new DecisionError(error, undefined)
        }
        throw new DecisionError(error, read.find((q) => q.id === quest.id) ?? null)
      }
    },
    onMutate: ({ quest, choice }) => {
      queryClient.setQueryData<SideQuest[]>(DECK_KEY, (deck) => deck?.filter((q) => q.id !== quest.id))
      if (choice !== 'try') return
      // A stale deck can offer a Side quest whose dinner the Quest log has.
      queryClient.setQueryData<QuestLogDinner[]>(QUEST_LOG_KEY, (dinners) =>
        dinners === undefined || dinners.some((d) => d.id === quest.dinner_id) ? dinners : dinners.concat({ id: quest.dinner_id, name: quest.name }),
      )
    },
    onSettled: async (_replenishJobId, error) => {
      if (error === null) {
        await Promise.all([queryClient.invalidateQueries({ queryKey: DECK_KEY }), queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })])
        return
      }
      // A failed decision has read the deck already.
      await queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })
      const { pending } = error
      if (pending === null || pending === undefined) return
      queryClient.setQueryData<SideQuest[]>(DECK_KEY, (deck) => [pending, ...(deck ?? []).filter((q) => q.id !== pending.id)])
    },
  })
}
