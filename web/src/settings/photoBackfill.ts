import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { useApi, useJob, type JobStatus } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import { oneOf } from '../dinners/api.ts'
import { HOME_KEY } from '../home/api.ts'

export const PHOTO_BACKFILL_PATH = '/api/v1/settings/photo-backfill'

// BACKFILL_JOB_KIND is the kind of the job a photo backfill runs as.
export const BACKFILL_JOB_KIND = 'backfill_dinner_photos'

export const BACKFILL_OUTCOMES = ['grounded', 'failed', 'skipped'] as const
export type BackfillOutcome = (typeof BACKFILL_OUTCOMES)[number]

// BackfillDinner is what a photo backfill did for one dinner. reason is
// set only when it failed: the reason code now its grounding_error.
export type BackfillDinner = { dinner_id: string; outcome: BackfillOutcome; reason: string | null }

// BackfillResult is the photo backfill job's result: each dinner it tried,
// in the order it tried them.
export type BackfillResult = { dinners: BackfillDinner[] }

// Every query showing a dinner's photo, read again once a backfill ends:
// the dinners lists and details, the plans and the home dashboard.
const PHOTO_KEYS = [['dinners'], ['plans'], HOME_KEY] as const

// parseBackfillResult checks a photo backfill job's result, which useJob
// passes on unread.
export function parseBackfillResult(where: string, body: unknown): BackfillResult {
  if (!isRecord(body) || !Array.isArray(body.dinners)) throw new Error(`${where}: result is not a photo backfill`)
  return {
    dinners: body.dinners.map((dinner): BackfillDinner => {
      if (!isRecord(dinner) || typeof dinner.dinner_id !== 'string' || (dinner.reason !== null && typeof dinner.reason !== 'string')) {
        throw new Error(`${where}: result has a dinner that is not a backfill outcome`)
      }
      return {
        dinner_id: dinner.dinner_id,
        outcome: oneOf(where, 'outcome', BACKFILL_OUTCOMES, dinner.outcome),
        reason: dinner.reason,
      }
    }),
  }
}

// PhotoBackfill is a photo backfill started from this screen, as it goes.
export type PhotoBackfill = {
  // start asks the server for a backfill over every dinner without a
  // photo. While one is already queued or running the server answers with
  // that job, so starting never runs two at once.
  start: () => void
  // active is true from start until the job ends, except while a poll's
  // failure leaves its progress unknown: starting again then asks the
  // server for the job it is running and polls it anew.
  active: boolean
  // Why the backfill could not be started.
  startError: Error | null
  // The job's status, undefined until it is started and first polled.
  status: JobStatus | undefined
  // The job's result, once it has one.
  result: BackfillResult | null
  // The job's failure, a poll's failure, or a result that is not a backfill.
  error: Error | null
}

// usePhotoBackfill starts a photo backfill (POST
// /api/v1/settings/photo-backfill, 202 {id}) and follows its job with
// useJob. When the job ends, every query showing a dinner's photo is read
// again, so dinners it grounded show their photo everywhere.
export function usePhotoBackfill(): PhotoBackfill {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob<unknown>(BACKFILL_JOB_KIND, jobId)
  const begin = useMutation({
    mutationFn: async (): Promise<string> => {
      const body = await apiFetch<unknown>(PHOTO_BACKFILL_PATH, { method: 'POST' })
      if (!isRecord(body) || typeof body.id !== 'string') throw new Error(`POST ${PHOTO_BACKFILL_PATH}: response has no job id`)
      return body.id
    },
    onSuccess: (id) => {
      setJobId(id)
      job.poll()
    },
  })

  const ended = job.status === 'succeeded' || job.status === 'failed'
  useEffect(() => {
    if (!ended) return
    for (const queryKey of PHOTO_KEYS) void queryClient.invalidateQueries({ queryKey })
  }, [ended, jobId, queryClient])

  const parsed = useMemo(() => {
    if (job.result === null) return { result: null, error: null }
    try {
      return { result: parseBackfillResult(`GET /api/v1/jobs/${jobId}`, job.result), error: null }
    } catch (error) {
      return { result: null, error: error as Error }
    }
  }, [job.result, jobId])

  return {
    start: () => begin.mutate(),
    active: begin.isPending || (jobId !== null && !ended && job.error === null),
    startError: begin.error,
    status: job.status,
    result: parsed.result,
    error: job.error ?? parsed.error,
  }
}
