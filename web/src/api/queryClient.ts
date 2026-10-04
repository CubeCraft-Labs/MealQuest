import { QueryClient } from '@tanstack/react-query'
import { ApiError } from './errors.ts'

// MAX_RETRIES bounds retries of a failure the server marked retryable.
export const MAX_RETRIES = 3

// shouldRetry retries only an ApiError the server marked retryable — never a
// 401, a 403 or a malformed response.
export function shouldRetry(failureCount: number, error: unknown): boolean {
  return error instanceof ApiError && error.retryable && failureCount < MAX_RETRIES
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: shouldRetry, staleTime: 30_000 },
      mutations: { retry: shouldRetry },
    },
  })
}
