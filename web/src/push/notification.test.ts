import { describe, expect, it, vi } from 'vitest'
import {
  APP_BADGE,
  APP_ICON,
  GENERIC_NOTICE,
  appPath,
  clickPath,
  openNotification,
  parsePushPayload,
  planClick,
  showPush,
  type ClickWindow,
} from './notification.ts'

const origin = 'https://mealquest.example'

describe('appPath', () => {
  it('keeps a same-origin path with its query and fragment', () => {
    expect(appPath('/plan/shopping?week=2026-10-05#tuesday', origin)).toBe('/plan/shopping?week=2026-10-05#tuesday')
  })

  it.each([
    'https://evil.example/plan',
    '//evil.example/plan',
    '/\\evil.example/plan',
    'javascript:alert(1)',
    'plan',
    '',
  ])('refuses %s', (raw) => {
    expect(appPath(raw, origin)).toBeNull()
  })

  it('resolves dot segments and keeps the result on origin', () => {
    expect(appPath('/plan/../discover', origin)).toBe('/discover')
  })

  // Each of these starts with a single / but resolves to a path starting with
  // //, which a browser reads as another host.
  it.each([
    '/..//evil.example/path',
    '/plan/..//evil.example/path',
    '/..\\\\evil.example/path',
    '/./\\evil.example',
    '/.\\/evil.example',
    '/%2e%2e//evil.example/path',
    '/.\t.//evil.example/path',
    '/..\n//evil.example',
  ])('refuses %j, which resolves to a protocol-relative path', (raw) => {
    expect(appPath(raw, origin)).toBeNull()
  })
})

describe('parsePushPayload', () => {
  it('reads title, body, tag and url', () => {
    const text = JSON.stringify({ title: 'Plan ready', body: 'Your Draft board is ready.', tag: 'plan_ready', url: '/plan' })
    expect(parsePushPayload(text, origin)).toEqual({
      notice: { title: 'Plan ready', body: 'Your Draft board is ready.', tag: 'plan_ready', url: '/plan' },
      problem: null,
    })
  })

  it('opens home with an empty body and no tag when only a title is sent', () => {
    expect(parsePushPayload(JSON.stringify({ title: 'Hi' }), origin)).toEqual({
      notice: { title: 'Hi', body: '', url: '/' },
      problem: null,
    })
  })

  it('ignores fields it does not know', () => {
    const parsed = parsePushPayload(JSON.stringify({ title: 'Hi', event: 'plan_ready' }), origin)
    expect(parsed.problem).toBeNull()
    expect(parsed.notice).toEqual({ title: 'Hi', body: '', url: '/' })
  })

  it.each([
    [null, 'push message has no payload'],
    ['', 'push message has no payload'],
    ['\u0000ÿ not json', 'push payload is not JSON'],
    ['[1,2]', 'push payload is not a JSON object'],
    ['null', 'push payload is not a JSON object'],
    ['{"body":"no title"}', 'push payload has no title'],
    ['{"title":"   "}', 'push payload has no title'],
    ['{"title":42}', 'push payload has no title'],
    ['{"title":"Hi","body":7}', 'push payload body is not a string'],
    ['{"title":"Hi","tag":""}', 'push payload tag is not a non-empty string'],
    ['{"title":"Hi","url":"https://evil.example/"}', 'push payload url is not a same-origin path'],
    ['{"title":"Hi","url":5}', 'push payload url is not a same-origin path'],
  ])('shows the generic notice for %j', (text, problem) => {
    expect(parsePushPayload(text, origin)).toEqual({ notice: GENERIC_NOTICE, problem })
  })
})

describe('showPush', () => {
  it('shows the payload with the app icon and badge, its tag and its url as data', async () => {
    const registration = { showNotification: vi.fn(async () => {}) }
    const warn = vi.fn()
    await showPush(
      JSON.stringify({ title: 'Supply run ready', body: 'Time to shop.', tag: 'list_ready', url: '/plan/shopping' }),
      registration,
      origin,
      warn,
    )
    expect(registration.showNotification).toHaveBeenCalledWith('Supply run ready', {
      body: 'Time to shop.',
      tag: 'list_ready',
      icon: APP_ICON,
      badge: APP_BADGE,
      data: { url: '/plan/shopping' },
    })
    expect(warn).not.toHaveBeenCalled()
  })

  it('shows the generic notification for a malformed payload and logs why, without throwing', async () => {
    const registration = { showNotification: vi.fn(async () => {}) }
    const warn = vi.fn()
    await expect(showPush('{not json', registration, origin, warn)).resolves.toBeUndefined()
    expect(registration.showNotification).toHaveBeenCalledWith('MealQuest', {
      body: GENERIC_NOTICE.body,
      icon: APP_ICON,
      badge: APP_BADGE,
      data: { url: '/' },
    })
    expect(warn).toHaveBeenCalledWith('MealQuest: push payload is not JSON; showing the generic notification')
  })
})

describe('clickPath', () => {
  it('opens the url the notification carries', () => {
    expect(clickPath({ url: '/discover' }, origin)).toBe('/discover')
  })

  it.each([undefined, null, 'x', {}, { url: 'https://evil.example/' }])('opens home for data %j', (data) => {
    expect(clickPath(data, origin)).toBe('/')
  })
})

describe('planClick', () => {
  it('refuses a path that resolves off origin', () => {
    expect(() => planClick([], '//evil.example/path', origin)).toThrow(/resolves off origin/)
  })

  const at = (path: string, focused = false) => ({ url: origin + path, focused })

  it('opens a new window when no MealQuest window is open', () => {
    expect(planClick([], '/plan', origin)).toEqual({ action: 'open', url: `${origin}/plan` })
  })

  it('ignores windows on another origin', () => {
    const other = { url: 'https://other.example/plan', focused: true }
    expect(planClick([other], '/plan', origin)).toEqual({ action: 'open', url: `${origin}/plan` })
  })

  it('focuses the focused MealQuest window and navigates it', () => {
    const first = at('/')
    const focused = at('/discover', true)
    expect(planClick([first, focused], '/plan', origin)).toEqual({
      action: 'focus',
      window: focused,
      url: `${origin}/plan`,
      navigate: true,
    })
  })

  it('focuses the first MealQuest window when none is focused', () => {
    const first = at('/')
    expect(planClick([first, at('/discover')], '/plan', origin)).toMatchObject({ action: 'focus', window: first })
  })

  it('does not reload a window already at the url', () => {
    expect(planClick([at('/plan')], '/plan', origin)).toMatchObject({ action: 'focus', navigate: false })
  })
})

describe('openNotification', () => {
  function window(path: string, focused = false) {
    return {
      url: origin + path,
      focused,
      focus: vi.fn(async () => {}),
      navigate: vi.fn(async () => {}),
    } satisfies ClickWindow
  }

  function clients(windows: ClickWindow[]) {
    return { matchAll: vi.fn(async () => windows), openWindow: vi.fn(async () => null) }
  }

  it('focuses an open window, then navigates it to the url', async () => {
    const win = window('/', true)
    const all = clients([win])
    await openNotification({ url: '/plan/shopping' }, all, origin)
    expect(all.matchAll).toHaveBeenCalledWith({ type: 'window' })
    expect(win.focus).toHaveBeenCalled()
    expect(win.navigate).toHaveBeenCalledWith(`${origin}/plan/shopping`)
    expect(win.focus.mock.invocationCallOrder[0]).toBeLessThan(win.navigate.mock.invocationCallOrder[0]!)
    expect(all.openWindow).not.toHaveBeenCalled()
  })

  it('opens a new window at the url when none is open', async () => {
    const all = clients([])
    await openNotification({ url: '/discover' }, all, origin)
    expect(all.openWindow).toHaveBeenCalledWith(`${origin}/discover`)
  })

  it('never opens a url off origin, even from tampered data', async () => {
    const all = clients([])
    await openNotification({ url: 'https://evil.example/' }, all, origin)
    expect(all.openWindow).toHaveBeenCalledWith(`${origin}/`)
  })

  it.each(['/..//evil.example/path', '/..\\\\evil.example/path', '/%2e%2e//evil.example/path', '/.\t.//evil.example/path'])(
    'never leaves origin for %j, which resolves to a protocol-relative path',
    async (url) => {
      const win = window('/plan', true)
      const all = clients([win])
      await openNotification({ url }, all, origin)
      expect(win.navigate).toHaveBeenCalledWith(`${origin}/`)
      expect(all.openWindow).not.toHaveBeenCalled()

      const none = clients([])
      await openNotification({ url }, none, origin)
      expect(none.openWindow).toHaveBeenCalledWith(`${origin}/`)
    },
  )

  it('still opens an internal path that uses dot segments', async () => {
    const all = clients([])
    await openNotification({ url: '/plan/../discover' }, all, origin)
    expect(all.openWindow).toHaveBeenCalledWith(`${origin}/discover`)
  })
})
