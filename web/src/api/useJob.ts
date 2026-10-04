import { useEffect, useState } from 'react'
import { useApi } from './ApiContext.tsx'

export type JobStatus = 'queued' | 'running' | 'succeeded' | 'failed'

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
  // The job's failure, or the latest polling request's own failure (cleared
  // by the next poll that answers).
  error: Error | null
}

// Polling starts at FIRST_DELAY_MS and doubles up to MAX_DELAY_MS.
export const FIRST_DELAY_MS = 500
export const MAX_DELAY_MS = 2000

const idle = { status: undefined, result: null, error: null }

function isTerminal(status: JobStatus): boolean {
  return status === 'succeeded' || status === 'failed'
}

// useJob polls GET /api/v1/jobs/{jobId} until the job succeeds or fails,
// backing off from 500 ms to 2 s. A failed poll is exposed as the error and
// polling continues. Polling stops only at a terminal status, when jobId
// changes and on unmount. A null jobId polls nothing.
export function useJob<R>(jobId: string | null): JobState<R> {
  const { apiFetch } = useApi()
  const [state, setState] = useState<{ jobId: string | null; job: JobState<R> }>({ jobId, job: idle })

  useEffect(() => {
    if (jobId === null) return
    let active = true
    let timer: ReturnType<typeof setTimeout> | undefined
    let delay = FIRST_DELAY_MS

    function next() {
      timer = setTimeout(poll, delay)
      delay = Math.min(delay * 2, MAX_DELAY_MS)
    }

    async function poll() {
      try {
        const job = await apiFetch<Job<R>>(`/api/v1/jobs/${encodeURIComponent(jobId!)}`)
        if (!active) return
        const error = job.status === 'failed' ? new Error(job.error ?? `job ${job.id} failed without an error message`) : null
        setState({ jobId, job: { status: job.status, result: job.result, error } })
        if (isTerminal(job.status)) return
      } catch (error) {
        if (!active) return
        // A failed poll is not the job's end: keep the last known status and
        // result, expose the request's error, and poll again.
        setState((prev) => ({
          jobId,
          job: { ...(prev.jobId === jobId ? prev.job : idle), error: error instanceof Error ? error : new Error(String(error)) },
        }))
      }
      next()
    }

    void poll()
    return () => {
      active = false
      clearTimeout(timer)
    }
  }, [apiFetch, jobId])

  // State left from a previous jobId is not this job's.
  return state.jobId === jobId ? state.job : idle
}
