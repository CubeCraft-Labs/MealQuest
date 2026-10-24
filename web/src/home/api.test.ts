import { describe, expect, it } from 'vitest'
import { createQueryClient } from '../api/index.ts'
import { HOME } from '../test/ApiProviders.tsx'
import { archiveTonight, HOME_KEY, parseHome, parseUpdates, type Home } from './api.ts'

const origin = 'https://mealquest.example'
const where = 'GET /api/v1/updates'

// body is a GET /api/v1/updates body holding one update that opens url.
function body(url: string) {
  return {
    updates: [{ type: 'plan_ready', title: 'Your week is drafted', body: '', url, created_at: '2026-10-10T16:00:00.000Z' }],
  }
}

describe('parseUpdates', () => {
  it('keeps a same-origin path with its query and fragment, as parsed', () => {
    expect(parseUpdates(where, body('/plan/shopping?week=2026-10-05#tuesday'), origin)[0]!.url).toBe(
      '/plan/shopping?week=2026-10-05#tuesday',
    )
    expect(parseUpdates(where, body('/plan/../discover'), origin)[0]!.url).toBe('/discover')
  })

  // Each of these would open another site, or run script, once parsed: URL
  // parsing strips tabs and newlines and reads a backslash as a slash.
  it.each([
    ['a tab', '/\t/evil.example/x'],
    ['a newline', '/\n/evil.example/x'],
    ['a carriage return', '/\r/evil.example/x'],
    ['a backslash', '/\\evil.example/x'],
    ['dot segments', '/..//evil.example/x'],
    ['a protocol-relative URL', '//evil.example/x'],
    ['an absolute URL', 'https://evil.example/x'],
    ['javascript:', 'javascript:alert(1)'],
    ['a relative path', 'plan'],
  ])('refuses an update url with %s', (_, url) => {
    expect(() => parseUpdates(where, body(url), origin)).toThrow(
      `${where}: update url ${JSON.stringify(url)} is not a path on this app`,
    )
  })
})

describe('parseHome', () => {
  it("keeps the household's zone, and refuses one that is not an IANA zone", () => {
    expect(parseHome('GET /api/v1/home', HOME).timezone).toBe('America/New_York')
    expect(() => parseHome('GET /api/v1/home', { ...HOME, timezone: 'Mars/Olympus' })).toThrow(
      'GET /api/v1/home: timezone "Mars/Olympus" is not an IANA zone',
    )
    expect(() => parseHome('GET /api/v1/home', { ...HOME, timezone: undefined })).toThrow(
      'GET /api/v1/home: timezone undefined is not an IANA zone',
    )
  })
})

describe('archiveTonight', () => {
  it("shows tonight's dinner archived when it is the dinner removed, keeping when Home was read", () => {
    const queryClient = createQueryClient()
    queryClient.setQueryData(HOME_KEY, parseHome('GET /api/v1/home', HOME), { updatedAt: 1000 })

    archiveTonight(queryClient, 'other')
    expect(queryClient.getQueryData<Home>(HOME_KEY)?.tonight?.status).toBe('regular')

    archiveTonight(queryClient, 'd1')
    expect(queryClient.getQueryData<Home>(HOME_KEY)?.tonight).toMatchObject({ id: 'd1', name: 'Pizza night', status: 'archived' })
    expect(queryClient.getQueryState(HOME_KEY)?.dataUpdatedAt).toBe(1000)
  })
})
