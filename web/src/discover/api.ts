import { hashKey, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, useApi } from '../api/index.ts'
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
// adding marks the entry a Try it put in before it was saved, so its
// rollback takes out that entry and no other.
export type QuestLogDinner = { id: string; name: string; adding?: symbol }

// REFILL_BELOW is the deck size, the depths the deck shows, under which a
// saved decision reads the deck again.
export const REFILL_BELOW = 3

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

// Decided is the context a decision's optimistic update leaves for its
// rollback: whether the Side quest was taken off the deck, the mark of the
// Quest log entry it added, if it added one, and what the reads of the deck
// that landed since have said of the Side quest.
type Decided = { taken: boolean; adding: symbol | null; reads: DeckReads }

// DeckReads follows the reads of the deck that land while a decision is in
// flight: gone is whether the latest left the Side quest out.
type DeckReads = { gone: boolean; stop: () => void }

// useDecide records Try it (POST /api/v1/suggestions/{id}/accept) or Pass
// (POST /api/v1/suggestions/{id}/decline) on the Side quest the household
// decided on, whether or not a read of the deck has since taken it away.
// The answer must be that suggestion, decided that way; the mutation's data
// is the id of the replenish job the decision started, or null.
//
// The decision is applied at once: the Side quest leaves the deck and, on
// Try it, its dinner joins the Quest log unless it is there already. A
// refused decision puts the Side quest back on top of the deck if it took
// it off and no read of the deck since has left it out, takes out of the
// Quest log only the entry it added, and reads the Quest log again, since
// the decision may have been saved all the same. A failure without an
// answer from the server, which may have saved it, reads the deck again
// too. A saved one reads the Quest log again, and the deck too once fewer
// than REFILL_BELOW Side quests are left in it or a read of the deck
// started before the decision was saved.
export function useDecide() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ quest: { id }, choice }: { quest: SideQuest; choice: Choice }): Promise<string | null> => {
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
    },
    onMutate: async ({ quest, choice }): Promise<Decided> => {
      // A read already under way would answer with the Side quest still
      // pending, undoing the update.
      await Promise.all([queryClient.cancelQueries({ queryKey: DECK_KEY }), queryClient.cancelQueries({ queryKey: QUEST_LOG_KEY })])
      const deck = queryClient.getQueryData<SideQuest[]>(DECK_KEY)
      const taken = deck !== undefined && deck.some((q) => q.id === quest.id)
      if (taken) queryClient.setQueryData<SideQuest[]>(DECK_KEY, deck.filter((q) => q.id !== quest.id))
      const reads: DeckReads = { gone: false, stop: () => {} }
      const deckHash = hashKey(DECK_KEY)
      reads.stop = queryClient.getQueryCache().subscribe((event) => {
        if (event.type !== 'updated' || event.query.queryHash !== deckHash || event.action.type !== 'success' || event.action.manual) return
        reads.gone = !(event.action.data as SideQuest[]).some((q) => q.id === quest.id)
      })
      // A stale deck can offer a Side quest whose dinner a read of the Quest
      // log already has.
      const dinners = queryClient.getQueryData<QuestLogDinner[]>(QUEST_LOG_KEY)
      if (choice !== 'try' || dinners === undefined || dinners.some((d) => d.id === quest.dinner_id)) return { taken, adding: null, reads }
      const adding = Symbol(quest.dinner_id)
      queryClient.setQueryData<QuestLogDinner[]>(QUEST_LOG_KEY, dinners.concat({ id: quest.dinner_id, name: quest.name, adding }))
      return { taken, adding, reads }
    },
    onError: (error, { quest }, context) => {
      const { taken, adding, reads } = context!
      if (taken && !reads.gone) queryClient.setQueryData<SideQuest[]>(DECK_KEY, (deck) => deck && [quest, ...deck.filter((q) => q.id !== quest.id)])
      // An API error answer is the server refusing the decision; anything
      // else may have come after it saved it.
      if (!(error instanceof ApiError)) void queryClient.invalidateQueries({ queryKey: DECK_KEY })
      if (adding === null) return
      // A read of the Quest log that landed meanwhile has replaced the entry.
      queryClient.setQueryData<QuestLogDinner[]>(QUEST_LOG_KEY, (dinners) => dinners?.filter((d) => d.adding !== adding))
      void queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })
    },
    onSuccess: async (_replenishJobId, { quest: { id } }) => {
      // A read that landed while the decision was in flight may have put
      // the Side quest back, and one still under way may yet: invalidating
      // cancels it for a read made after the decision was saved.
      const reading = queryClient.isFetching({ queryKey: DECK_KEY }) > 0
      const deck = queryClient.setQueryData<SideQuest[]>(DECK_KEY, (current) => current?.filter((q) => q.id !== id))
      await Promise.all([
        reading || (deck !== undefined && deck.length < REFILL_BELOW) ? queryClient.invalidateQueries({ queryKey: DECK_KEY }) : undefined,
        queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY }),
      ])
    },
    onSettled: (_data, _error, _variables, context) => context?.reads.stop(),
  })
}
