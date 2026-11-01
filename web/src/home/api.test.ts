import { describe, expect, it } from 'vitest'
import { HOME } from '../test/ApiProviders.tsx'
import { parseHome, parseUpdates, showArchived } from './api.ts'

const origin = 'https://mealquest.example'
const where = 'GET /api/v1/updates'

// body is a GET /api/v1/updates body holding one update that opens url.
function body(url: string) {
  return {
    updates: [{ type: 'plan_ready', title: 'Your week is drafted', body: '', url, dinner: null, created_at: '2026-10-10T16:00:00.000Z' }],
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

const homeWhere = 'GET /api/v1/home'

describe('parseHome', () => {
  it("keeps the household's zone, and refuses one that is not an IANA zone", () => {
    expect(parseHome(homeWhere, HOME, false).timezone).toBe('America/New_York')
    expect(() => parseHome(homeWhere, { ...HOME, timezone: 'Mars/Olympus' }, false)).toThrow(
      'GET /api/v1/home: timezone "Mars/Olympus" is not an IANA zone',
    )
    expect(() => parseHome(homeWhere, { ...HOME, timezone: undefined }, false)).toThrow(
      'GET /api/v1/home: timezone undefined is not an IANA zone',
    )
  })

  // A Delivery or Dining-out evening is its own Tonight state. Dropping it
  // would show that night as empty.
  it.each(['delivery', 'dining_out'] as const)('keeps a %s evening, with no dinner and no manual night', (evening) => {
    const home = parseHome(homeWhere, { ...HOME, tonight: null, evening }, false)

    expect(home.evening).toBe(evening)
    expect(home.tonight).toBeNull()
    expect(home.manualTonight).toBeNull()
  })

  it('keeps a null evening when tonight is a dinner or a cleared night', () => {
    expect(parseHome(homeWhere, { ...HOME, evening: null }, false).evening).toBeNull()
    const cleared = parseHome(homeWhere, { ...HOME, tonight: null, evening: null }, false)
    expect(cleared.evening).toBeNull()
    expect(cleared.tonight).toBeNull()
    expect(cleared.manualTonight).toBeNull()
  })

  it.each([
    ['an unknown evening', 'takeout'],
    ['cook, which is not an evening', 'cook'],
    ['a missing evening', undefined],
  ])('refuses %s', (_, evening) => {
    const body = { ...HOME, tonight: null, evening }
    if (evening === undefined) delete body.evening
    expect(() => parseHome(homeWhere, body, false)).toThrow(
      `${homeWhere}: evening ${JSON.stringify(evening)} is not one of delivery, dining_out`,
    )
  })

  it('refuses a dinner and an evening together', () => {
    expect(() => parseHome(homeWhere, { ...HOME, evening: 'delivery' }, false)).toThrow(
      `${homeWhere}: tonight is both a dinner and a delivery evening`,
    )
  })

  it('refuses a manual night and an evening together', () => {
    expect(() =>
      parseHome(homeWhere, { ...HOME, tonight: null, manualTonight: 'mummy dogs', evening: 'dining_out' }, false),
    ).toThrow(`${homeWhere}: tonight is both a manual night and a dining_out evening`)
  })
})

describe('showArchived', () => {
  it("shows tonight's dinner archived when the server archived it, whatever the read said", () => {
    const home = parseHome('GET /api/v1/home', HOME, false)

    expect(showArchived(home, new Set(['other']))).toBe(home)
    expect(showArchived(home, new Set(['d1'])).tonight).toMatchObject({ id: 'd1', name: 'Pizza night', status: 'archived' })
    expect(home.tonight?.status).toBe('regular')
  })
})
