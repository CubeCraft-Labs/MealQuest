import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { photoId } from '../dinners/api.ts'
import { isNotificationOpened } from '../push/notification.ts'

export const ROTATION_REVIEWS_PATH = '/api/v1/rotation-reviews'
export const ROTATION_REVIEWS_KEY = ['rotation-reviews'] as const
const DINNERS_KEY = ['dinners'] as const

// rotationReviewPath is where the household answers "Keep it in rotation?"
// for a tried dinner.
export function rotationReviewPath(dinnerId: string): string {
  return `/api/v1/dinners/${encodeURIComponent(dinnerId)}/rotation-review`
}

// Rotation check answers: keep makes the tried dinner a Regular, drop ("Not
// for us") archives it so it is never suggested again, and not_made ("We
// didn't make it") returns it to the Quest log. Keep is the only way a tried
// dinner becomes a Regular; nothing decides one automatically.
export const DECISIONS = ['keep', 'drop', 'not_made'] as const
export type Decision = (typeof DECISIONS)[number]

// RotationReview is a pending Rotation check: the dinner tried, photo_id its
// stored photo's id, and the household-local plan date, YYYY-MM-DD, it was
// planned on.
export type RotationReview = { dinner: { id: string; name: string; photo_id: string | undefined }; plan_date: string }

function parseReview(where: string, body: unknown): RotationReview {
  if (!isRecord(body) || typeof body.plan_date !== 'string') throw new Error(`${where}: a review is not a Rotation check`)
  const dinner = body.dinner
  if (!isRecord(dinner) || typeof dinner.id !== 'string' || typeof dinner.name !== 'string') {
    throw new Error(`${where}: a Rotation check has no dinner`)
  }
  return { dinner: { id: dinner.id, name: dinner.name, photo_id: photoId(where, dinner.photo) }, plan_date: body.plan_date }
}

// parseRotationReviews checks a GET /api/v1/rotation-reviews body: the
// pending Rotation checks, oldest plan date first.
export function parseRotationReviews(where: string, body: unknown): RotationReview[] {
  if (!isRecord(body) || !Array.isArray(body.reviews)) throw new Error(`${where}: response is not the Rotation checks`)
  return body.reviews.map((review) => parseReview(where, review))
}

// useRotationReviews is the pending Rotation checks (GET
// /api/v1/rotation-reviews), oldest plan date first. Each mount of Home
// reads them afresh, and so does a notification tap that focuses Home
// already open (the service worker's NotificationOpened), so Home opened
// from a rotation_review notification shows the check it is about.
export function useRotationReviews() {
  const { apiFetch } = useApi()
  const query = useQuery({
    queryKey: ROTATION_REVIEWS_KEY,
    queryFn: async () =>
      parseRotationReviews(`GET ${ROTATION_REVIEWS_PATH}`, await apiFetch<unknown>(ROTATION_REVIEWS_PATH)),
    refetchOnMount: 'always',
  })
  const { refetch } = query
  useEffect(() => {
    // Without a service worker there are no notifications to tap.
    if (!('serviceWorker' in navigator)) return
    const container = navigator.serviceWorker
    const onMessage = (event: MessageEvent<unknown>) => {
      if (isNotificationOpened(event.data)) void refetch()
    }
    container.addEventListener('message', onMessage)
    return () => container.removeEventListener('message', onMessage)
  }, [refetch])
  return query
}

// useDecideRotationReview answers "Keep it in rotation?" for a pending
// Rotation check (POST /api/v1/dinners/{id}/rotation-review). The answer
// must be that dinner, decided that way.
//
// Home leaves the check out while its answer is pending (the mutation's
// variables say which), so the next one shows at once; a read in flight is
// stopped so it cannot bring the check back meanwhile. Once saved, the check
// leaves the cached list; once failed, it is shown again as the list has it.
// Either way the Rotation checks and the dinners lists (the Regulars and the
// Quest log the answer moved it to) are read again.
export function useDecideRotationReview() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation<void, Error, { review: RotationReview; decision: Decision }>({
    mutationFn: async ({ review, decision }) => {
      const path = rotationReviewPath(review.dinner.id)
      const body = await apiFetch<unknown>(path, { method: 'POST', body: { decision } })
      if (!isRecord(body) || !isRecord(body.dinner) || body.dinner.id !== review.dinner.id || body.decision !== decision) {
        throw new Error(`POST ${path}: response is not the Rotation check decided ${decision}`)
      }
    },
    onMutate: async () => {
      await queryClient.cancelQueries({ queryKey: ROTATION_REVIEWS_KEY })
    },
    onSuccess: (_data, { review }) => {
      queryClient.setQueryData<RotationReview[]>(ROTATION_REVIEWS_KEY, (reviews) =>
        reviews?.filter((r) => r.dinner.id !== review.dinner.id),
      )
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ROTATION_REVIEWS_KEY })
      void queryClient.invalidateQueries({ queryKey: DINNERS_KEY })
    },
  })
}
