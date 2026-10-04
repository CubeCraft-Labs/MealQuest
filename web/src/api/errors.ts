// ApiError is a failed API response carrying the server's
// {"error":{"code","message","retryable"}} body.
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly retryable: boolean

  constructor(status: number, code: string, message: string, retryable: boolean) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryable = retryable
  }
}

// parseApiError reads an error response body. A body that is not the API
// error shape is itself an error: the server always sends one.
export async function parseApiError(method: string, path: string, response: Response): Promise<ApiError> {
  const text = await response.text()
  let body: unknown
  try {
    body = JSON.parse(text)
  } catch {
    throw new Error(`${method} ${path}: ${response.status} response is not JSON: ${text.slice(0, 200)}`)
  }
  const error = isRecord(body) ? body.error : undefined
  if (
    !isRecord(error) ||
    typeof error.code !== 'string' ||
    typeof error.message !== 'string' ||
    typeof error.retryable !== 'boolean'
  ) {
    throw new Error(`${method} ${path}: ${response.status} response has no API error body`)
  }
  return new ApiError(response.status, error.code, error.message, error.retryable)
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
