import { useMutation, useMutationState, useQuery, useQueryClient } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { DIFFICULTIES, DINNERS_PATH, dinnerPath, oneOf, parseDinner, photoId, type Difficulty, type Regular } from '../dinners/api.ts'

export const SUGGESTIONS_PATH = '/api/v1/suggestions'
export const DECK_PATH = `${SUGGESTIONS_PATH}/deck`
export const BATCHES_PATH = `${SUGGESTIONS_PATH}/batches`
export const QUEST_LOG_PATH = `${DINNERS_PATH}?status=quest_log`

// DECK_KEY is the deck's query; QUEST_LOG_KEY the Quest log's;
// DECIDE_KEY the decisions'.
export const DECK_KEY = ['suggestions', 'deck'] as const
export const QUEST_LOG_KEY = ['dinners', 'quest_log'] as const
const DECIDE_KEY = ['suggestions', 'decide'] as const

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

// QuestLogDinner is a dinner in the Quest log, as the Quest log sheet lists
// it: photo, name, difficulty and minutes.
export type QuestLogDinner = Pick<Regular, 'id' | 'name' | 'photo_id' | 'difficulty' | 'minutes_total'>

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
// /api/v1/dinners?status=quest_log). The server leaves out a dinner already
// on the plan for the current week or a later one: it is scheduled.
export function useQuestLog() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: QUEST_LOG_KEY,
    queryFn: async (): Promise<QuestLogDinner[]> => {
      const where = `GET ${QUEST_LOG_PATH}`
      const body = await apiFetch<unknown>(QUEST_LOG_PATH)
      if (!isRecord(body) || !Array.isArray(body.dinners)) throw new Error(`${where}: response has no dinners list`)
      return body.dinners.map((dinner) => {
        const { id, name, photo_id, difficulty, minutes_total } = parseDinner(where, dinner)
        return { id, name, photo_id, difficulty, minutes_total }
      })
    },
  })
}

// useRemoveFromQuestLog archives a Quest-log dinner (DELETE
// /api/v1/dinners/{id}). Once the server has archived it, it leaves the
// Quest log, and the Quest log is read again.
export function useRemoveFromQuestLog() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (dinner: QuestLogDinner) => {
      await apiFetch<unknown>(dinnerPath(dinner.id), { method: 'DELETE' })
    },
    onSuccess: async (_data, dinner) => {
      queryClient.setQueryData<QuestLogDinner[]>(QUEST_LOG_KEY, (dinners) => dinners?.filter((d) => d.id !== dinner.id))
      await queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })
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

// stillPending says whether the deck's newest read has the Side quest id
// pending, after a failed decision on it: the Side quest when it does, null
// when it does not, and undefined when that read failed, which says neither.
// deck is the deck query's state, from the cache or useDeck.
export function stillPending(deck: { status: string; data: SideQuest[] | undefined } | undefined, id: string): SideQuest | null | undefined {
  if (deck?.status !== 'success') return undefined
  return deck.data!.find((q) => q.id === id) ?? null
}

// useDeciding is the ids of the Side quests whose decisions are pending:
// sent and not yet settled. The deck shown leaves them out, whatever a read
// of the deck has said meanwhile.
export function useDeciding(): Set<string> {
  const ids = useMutationState<string>({
    filters: { mutationKey: DECIDE_KEY, status: 'pending' },
    select: (mutation) => (mutation.state.variables as { quest: SideQuest }).quest.id,
  })
  return new Set(ids)
}

// useDecide records Try it (POST /api/v1/suggestions/{id}/accept) or Pass
// (POST /api/v1/suggestions/{id}/decline) on the Side quest the household
// decided on, whether or not a read of the deck has since taken it away.
// The answer must be that suggestion, decided that way; the mutation's data
// is the id of the replenish job the decision started, or null.
//
// The decision is applied at once: the Side quest leaves the deck shown
// while it is pending (see useDeciding) and, on Try it, its dinner joins the
// Quest log unless it is there already. Nothing else is worked out here:
// the server is the record of both. However the decision ends, saved or
// failed, the deck and the Quest log are read again, and the decision is
// pending until those reads land, so the next one (which DiscoverPage locks
// out until then) cannot start under them. A read under way when the
// decision ends is replaced by the read made after it, and DiscoverPage's
// batch reads wait for that one rather than replace it.
// A failed decision waits for the deck read it makes itself. Whether its
// Side quest is still pending is then the deck query's to say, from its
// newest successful read: once the reads have landed, a Side quest the deck
// still has goes back on top of it.
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

  return useMutation<string | null, Error, { quest: SideQuest; choice: Choice }>({
    mutationKey: DECIDE_KEY,
    mutationFn: async ({ quest, choice }) => {
      try {
        return await send(quest.id, choice)
      } catch (error) {
        // The deck is read again as the decision's own fetch, replacing any
        // read already out. A failed read is the deck query's error, shown
        // with the deck; the decision's own error is the one thrown.
        await queryClient
          .getQueryCache()
          .build<SideQuest[]>(queryClient, { queryKey: DECK_KEY })
          .fetch(undefined, { cancelRefetch: true })
          .catch(() => undefined)
        throw error
      }
    },
    onMutate: ({ quest, choice }) => {
      if (choice !== 'try') return
      // A stale deck can offer a Side quest whose dinner the Quest log has.
      queryClient.setQueryData<QuestLogDinner[]>(QUEST_LOG_KEY, (dinners) =>
        dinners === undefined || dinners.some((d) => d.id === quest.dinner_id)
          ? dinners
          : // The deck does not say the minutes; the Quest log read after the
            // decision does.
            dinners.concat({
              id: quest.dinner_id,
              name: quest.name,
              photo_id: quest.photo_id,
              difficulty: quest.difficulty,
              minutes_total: null,
            }),
      )
    },
    onSettled: async (_replenishJobId, error, { quest }) => {
      if (error === null) {
        await Promise.all([queryClient.invalidateQueries({ queryKey: DECK_KEY }), queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })])
        return
      }
      // A failed decision has read the deck already; any read since is newer.
      await queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })
      const pending = stillPending(queryClient.getQueryState<SideQuest[]>(DECK_KEY), quest.id)
      if (pending === null || pending === undefined) return
      queryClient.setQueryData<SideQuest[]>(DECK_KEY, (deck) => [pending, ...deck!.filter((q) => q.id !== pending.id)])
    },
  })
}
