import { describe, expect, it } from 'vitest'
import { ApiError, createQueryClient, shouldRetry } from './index.ts'

describe('query retries', () => {
  it('retry only an ApiError the server marked retryable, a bounded number of times', () => {
    expect(shouldRetry(0, new ApiError(503, 'unavailable', 'try later', true))).toBe(true)
    expect(shouldRetry(3, new ApiError(503, 'unavailable', 'try later', true))).toBe(false)
    expect(shouldRetry(0, new ApiError(401, 'unauthenticated', 'sign in required', false))).toBe(false)
    expect(shouldRetry(0, new ApiError(403, 'csrf', 'bad token', false))).toBe(false)
    expect(shouldRetry(0, new Error('network down'))).toBe(false)
  })

  it('are the QueryClient default for queries and mutations', () => {
    const options = createQueryClient().getDefaultOptions()
    expect(options.queries?.retry).toBe(shouldRetry)
    expect(options.mutations?.retry).toBe(shouldRetry)
  })
})
