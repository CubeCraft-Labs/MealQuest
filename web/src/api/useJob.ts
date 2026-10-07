import { useCallback, useEffect, useState } from 'react'
import { useApi } from './ApiContext.tsx'
import { isRecord } from './errors.ts'

export type JobStatus = 'queued' | 'running' | 'succeeded' | 'failed'

const STATUSES: readonly string[] = ['queued', 'running', 'succeeded', 'failed'] satisfies JobStatus[]

// Job is the GET /api/v1/jobs/{id} response. result may be set while the job
// is running (a partial result); error is set only when it failed.
export type Job<R> = {
  id: string
  kind: string
  status: JobStatus
  result: R | null
  error: string | null
}

export type JobState<R> = {
  // undefined until the first poll answers.
  status: JobStatus | undefined
  // The latest result, partial while the job runs.
  result: R | null
  // The job's failure, the latest polling request's own failure (cleared
  // by the next poll that answers), or an answer that is not this job.
  error: Error | null
  // poll asks for the job now, starting the backoff again — also after
  // polling stopped on an answer that is not this job.
  poll: () => void
}

// Polling starts at FIRST_DELAY_MS and doubles up to MAX_DELAY_MS.
export const FIRST_DELAY_MS = 500
export const MAX_DELAY_MS = 2000

type JobData<R> = Omit<JobState<R>, 'poll'>

const idle = { status: undefined, result: null, error: null }

// parseJob checks a GET /api/v1/jobs/{id} answer is the kind of job asked
// for, with a known status. result is passed on unread for the caller to
// check.
function parseJob<R>(where: string, kind: string, jobId: string, body: unknown): Job<R> {
  if (
    !isRecord(body) ||
    body.id !== jobId ||
    body.kind !== kind ||
    typeof body.status !== 'string' ||
    !STATUSES.includes(body.status) ||
    !('result' in body) ||
    (body.error !== null && typeof body.error !== 'string')
  ) {
    throw new Error(`${where}: response is not the ${kind} job ${jobId}`)
  }
  return { id: jobId, kind, status: body.status as JobStatus, result: body.result as R | null, error: body.error as string | null }
}

function isTerminal(status: JobStatus): boolean {
  return status === 'succeeded' || status === 'failed'
}

// useJob polls GET /api/v1/jobs/{jobId}, a job of the given kind, until it
// succeeds or fails, backing off from 500 ms to 2 s. A failed poll is
// exposed as the error and polling continues. An answer that is not this
// job is exposed as the error and stops polling until poll is called.
// Polling also stops at a terminal status, when jobId changes and on
// unmount. A null jobId polls nothing.
export function useJob<R>(kind: string, jobId: string | null): JobState<R> {
  const { apiFetch } = useApi()
  const [state, setState] = useState<{ jobId: string | null; job: JobData<R> }>({ jobId, job: idle })
  // Each poll call bumps round, which restarts the polling effect.
  const [round, setRound] = useState(0)
  const poll = useCallback(() => setRound((r) => r + 1), [])

  useEffect(() => {
    if (jobId === null) return
    let active = true
    let timer: ReturnType<typeof setTimeout> | undefined
    let delay = FIRST_DELAY_MS

    async function fetchJob() {
      const path = `/api/v1/jobs/${encodeURIComponent(jobId!)}`
      let body: unknown
      try {
        body = await apiFetch<unknown>(path)
      } catch (error) {
        if (!active) return
        // A failed poll is not the job's end: keep the last known status and
        // result, expose the request's error, and poll again.
        fail(error instanceof Error ? error : new Error(String(error)))
        next()
        return
      }
      if (!active) return
      let job: Job<R>
      try {
        job = parseJob<R>(`GET ${path}`, kind, jobId!, body)
      } catch (error) {
        // An answer that is not this job will not become one by asking
        // again: expose it and stop.
        fail(error as Error)
        return
      }
      const error = job.status === 'failed' ? new Error(job.error ?? `job ${job.id} failed without an error message`) : null
      setState({ jobId, job: { status: job.status, result: job.result, error } })
      if (!isTerminal(job.status)) next()
    }

    // fail keeps the last known status and result, with error.
    function fail(error: Error) {
      setState((prev) => ({ jobId, job: { ...(prev.jobId === jobId ? prev.job : idle), error } }))
    }

    function next() {
      timer = setTimeout(fetchJob, delay)
      delay = Math.min(delay * 2, MAX_DELAY_MS)
    }

    void fetchJob()
    return () => {
      active = false
      clearTimeout(timer)
    }
  }, [apiFetch, kind, jobId, round])

  // State left from a previous jobId is not this job's.
  return { ...(state.jobId === jobId ? state.job : idle), poll }
}
