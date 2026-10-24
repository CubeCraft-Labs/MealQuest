import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock, type MockInstance } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus, type NetworkWindow } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { RETRY_FIRST, supplyRunKey, SupplyRunSync, TOGGLE_DEADLINE } from './api.ts'
import { resetSavedProblems, SAVED_KEY } from './offline.ts'
import { SupplyRunPage } from './SupplyRunPage.tsx'

const THIS_WEEK = '/api/v1/shopping/2026-10-05'
const ITEM = (id: string) => `/api/v1/shopping/items/${id}`

// Monday's tacos: two items to buy and one the household has.
const ITEMS = [
  { id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true },
  { id: 'i2', name: 'Corn tortillas', qty: '12', needed: true },
  { id: 'i3', name: 'Taco seasoning', qty: '1 packet', needed: false },
]

// Next week's curry: one item to buy.
const NEXT_ITEMS = [{ id: 'n1', name: 'Chicken thighs', qty: '2 lb', needed: true }]

// The items of each week, by its Monday.
const WEEKS: Record<string, typeof ITEMS> = { '2026-10-05': ITEMS, '2026-10-12': NEXT_ITEMS }

function itemsOf(week: string) {
  const items = WEEKS[week]
  if (items === undefined) throw new Error(`no week ${week}`)
  return items
}

// The browser's online flag; goOffline and goOnline change it and send the
// page's window the event a browser sends.
let onLine: boolean
// The server can be reached: false, every request fails as fetch does when
// the network fails, whatever onLine says.
let reachable: boolean
// The window of the page now open: a reload opens a new one, and the old
// page hears nothing more.
let win: NetworkWindow & EventTarget

function openWindow() {
  win = Object.assign(new EventTarget(), {
    navigator: {
      get onLine() {
        return onLine
      },
    },
  }) as unknown as NetworkWindow & EventTarget
  return win
}

function goOffline() {
  onLine = false
  act(() => void win.dispatchEvent(new Event('offline')))
}

function goOnline() {
  onLine = true
  reachable = true
  act(() => void win.dispatchEvent(new Event('online')))
}

// stubServer is the server's Supply runs of this week and next: a PATCH
// marks its item and answers with its week's counts, unless refuse answers
// it instead; when hold is given, the item is marked at once but the answer
// waits for the promise hold returns, or until the request is aborted; holdMe
// holds GET /api/v1/me's answer the same way. This week's counts as archived
// once archive is called.
function stubServer(
  refuse: (id: string) => Response | undefined = () => undefined,
  hold?: () => Promise<void>,
  holdMe?: () => Promise<void>,
) {
  const needed = new Map(Object.values(WEEKS).flatMap((items) => items.map((item) => [item.id, item.needed] as const)))
  let status = 'current'
  // How many PATCHes are unanswered now, and the most there have been at once.
  let inFlight = 0
  let mostInFlight = 0
  // How many GET /api/v1/me requests have been answered.
  let meAnswered = 0
  const counts = (week: string) => {
    const marks = itemsOf(week).map((item) => needed.get(item.id)!)
    return { needed_count: marks.filter(Boolean).length, total_count: marks.length }
  }
  const run = (week: string) => ({
    weekStart: week,
    label: week === '2026-10-05' ? 'Week 41 · Oct 5 – 11' : 'Week 42 · Oct 12 – 18',
    title: null,
    status: week === '2026-10-05' ? status : 'current',
    sent_to_chat_at: null,
    chat_send_unconfirmed_at: null,
    ...counts(week),
    groups: [
      {
        dinner: { id: week === '2026-10-05' ? 'd0' : 'd1', name: week === '2026-10-05' ? 'Beef tacos' : 'Chicken curry', status: 'regular' },
        date: week,
        items: itemsOf(week).map((item) => ({ ...item, needed: needed.get(item.id)! })),
      },
    ],
  })
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    if (!onLine || !reachable) throw new TypeError('Failed to fetch')
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    if (key === 'GET /api/v1/me') {
      await answerAfter(init?.signal, holdMe)
      meAnswered++
      return jsonResponse(200, ME)
    }
    if (key === 'GET /api/v1/settings/household') return jsonResponse(200, HOUSEHOLD_SETTINGS)
    if (key === `GET ${THIS_WEEK}`) return jsonResponse(200, run('2026-10-05'))
    const week = Object.keys(WEEKS).find((w) => itemsOf(w).some((i) => key === `PATCH ${ITEM(i.id)}`))
    if (week === undefined) throw new Error(`unexpected fetch: ${key}`)
    const id = itemsOf(week).find((i) => key === `PATCH ${ITEM(i.id)}`)!.id
    const refusal = refuse(id)
    if (refusal !== undefined) return refusal
    const body = JSON.parse(String(init?.body)) as { needed: boolean }
    needed.set(id, body.needed)
    const answer = jsonResponse(200, { id, needed: body.needed, ...counts(week) })
    inFlight++
    mostInFlight = Math.max(mostInFlight, inFlight)
    try {
      await answerAfter(init?.signal, hold)
    } finally {
      inFlight--
    }
    return answer
  })
  vi.stubGlobal('fetch', fetchMock)
  return {
    fetchMock,
    run,
    mostInFlight: () => mostInFlight,
    meAnswered: () => meAnswered,
    archive: () => {
      status = 'archived'
    },
  }
}

// answerAfter resolves once the promise hold returns does, at once without
// hold, or rejects with signal's reason once it aborts.
function answerAfter(signal: AbortSignal | null | undefined, hold?: () => Promise<void>) {
  return new Promise<void>((resolve, reject) => {
    signal?.addEventListener('abort', () => reject(signal.reason as Error))
    void (hold?.() ?? Promise.resolve()).then(resolve)
  })
}

// held is a hold for stubServer whose answers wait until release is called,
// each one released in turn.
function held() {
  const waiting: (() => void)[] = []
  return {
    hold: () => new Promise<void>((resolve) => waiting.push(resolve)),
    waiting: () => waiting.length,
    release: () => waiting.shift()!(),
  }
}

// mes counts the GET /api/v1/me requests sent.
function mes(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([input, init]) => String(input) === '/api/v1/me' && (init?.method ?? 'GET') === 'GET').length
}

// patches lists the PATCHes sent, as "path body".
function patches(fetchMock: Mock<typeof fetch>) {
  return fetchMock.mock.calls.filter(([, init]) => init?.method === 'PATCH').map(([input, init]) => `${String(input)} ${String(init?.body)}`)
}

// queue is the toggles saved on this device, as "id needed".
function queue(): string[] {
  const saved = localStorage.getItem(SAVED_KEY)
  if (saved === null) return []
  return (JSON.parse(saved) as { queue: { id: string; needed: boolean }[] }).queue.map((t) => `${t.id} ${t.needed}`)
}

// openPage opens the Supply run screen in a new window, as the app does:
// with SupplyRunSync, and with /me loaded unless the server cannot be
// reached. leave goes to another screen of the same page, and reopen comes
// back to the Supply run screen; queryClient is the page's.
async function openPage() {
  const api = createApiClient(fakeLocation('/plan/shopping'), createNetworkStatus(openWindow()), createVerifiedSession(localStorage))
  if (onLine && reachable) await api.loadMe()
  const queryClient = createQueryClient()
  const app = (onScreen: boolean) => (
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/plan/shopping']}>
          <SupplyRunSync />
          {onScreen && <SupplyRunPage />}
        </MemoryRouter>
      </QueryClientProvider>
    </ApiProvider>
  )
  const view = render(app(true))
  return { ...view, queryClient, leave: () => view.rerender(app(false)), reopen: () => view.rerender(app(true)) }
}

// reload ends the page open and its timers, as a reload does: a toggle it
// was waiting to try again is never tried, by it, again. The clock reads on
// as it did: clearAllTimers also puts it back to the real time the fake
// timers were installed at, which would open the new page on whatever week
// the real date falls in.
function reload(page: { unmount: () => void }) {
  page.unmount()
  const now = Date.now()
  vi.clearAllTimers()
  vi.setSystemTime(now)
}

async function advance(ms: number) {
  await act(() => vi.advanceTimersByTimeAsync(ms))
}

// The page's setTimeout, watched: retrySecondTry waits for a timer to be set.
let timers: MockInstance<typeof setTimeout>

// retrySecondTry jumps past the wait before a toggle's second try — RETRY_FIRST
// doubled by its failed first try — once that wait's timer is set, so the jump
// never lands before the timer it is meant to fire.
async function retrySecondTry() {
  await waitFor(() => expect(timers).toHaveBeenCalledWith(expect.any(Function), 2 * RETRY_FIRST))
  await advance(2 * RETRY_FIRST)
}

function checkbox(name: string) {
  return screen.getByRole('checkbox', { name: new RegExp(`^${name}`) })
}

function row(name: string) {
  return screen.getByText(name).closest('li')!
}

beforeEach(() => {
  // Each test is a new page: no saved-data failure is reported yet.
  resetSavedProblems()
  onLine = true
  reachable = true
  mockViewport(390)
  // setTimeout moves with real time, and a test can jump past a deadline or
  // a wait (advance).
  vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'], shouldAdvanceTime: true })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
  timers = vi.spyOn(globalThis, 'setTimeout')
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, 'matchMedia')
})

describe('offline toggle queue', () => {
  it('applies toggles made offline at once, saves them in order and sends them in that order once online', async () => {
    const { fetchMock } = stubServer()
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()

    goOffline()
    fireEvent.click(checkbox('Taco seasoning'))
    fireEvent.click(checkbox('Ground beef'))
    fireEvent.click(checkbox('Taco seasoning'))

    // Applied here, counted, marked to sync and saved in order; nothing sent.
    expect(checkbox('Taco seasoning')).not.toBeChecked()
    expect(checkbox('Ground beef')).not.toBeChecked()
    expect(screen.getByText('1 / 3')).toBeInTheDocument()
    expect(within(row('Taco seasoning')).getByText('will sync')).toBeInTheDocument()
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()
    expect(within(row('Corn tortillas')).queryByText('will sync')).not.toBeInTheDocument()
    expect(queue()).toEqual(['i3 true', 'i1 false', 'i3 false'])
    expect(patches(fetchMock)).toEqual([])

    goOnline()
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(fetchMock)).toEqual([
      `${ITEM('i3')} {"needed":true}`,
      `${ITEM('i1')} {"needed":false}`,
      `${ITEM('i3')} {"needed":false}`,
    ])
    expect(screen.getByText('1 / 3')).toBeInTheDocument()
    expect(checkbox('Taco seasoning')).not.toBeChecked()
    expect(checkbox('Ground beef')).not.toBeChecked()
    expect(screen.queryByText('will sync')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('queues a toggle whose PATCH fails with a network error and sends it again once back online', async () => {
    const { fetchMock } = stubServer()
    await openPage()
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Taco seasoning' }))
    await waitFor(() => expect(queue()).toEqual([]))
    expect(screen.getByText('3 / 3')).toBeInTheDocument()

    // The browser still says online, but the server cannot be reached.
    reachable = false
    fireEvent.click(checkbox('Corn tortillas'))
    expect(await within(row('Corn tortillas')).findByText('will sync')).toBeInTheDocument()
    expect(patches(fetchMock)).toEqual([`${ITEM('i3')} {"needed":true}`, `${ITEM('i2')} {"needed":false}`])
    expect(checkbox('Corn tortillas')).not.toBeChecked()
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(queue()).toEqual(['i2 false'])
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    goOnline()
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(fetchMock)).toEqual([`${ITEM('i3')} {"needed":true}`, `${ITEM('i2')} {"needed":false}`, `${ITEM('i2')} {"needed":false}`])
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(screen.queryByText('will sync')).not.toBeInTheDocument()
  })

  it('opens the saved list after a reload offline and replays the saved toggles in order once online', async () => {
    const { fetchMock } = stubServer()
    const first = await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()
    goOffline()
    fireEvent.click(checkbox('Taco seasoning'))
    fireEvent.click(checkbox('Ground beef'))
    expect(queue()).toEqual(['i3 true', 'i1 false'])

    // Reload while offline: the settings, /me and the list cannot be read.
    reload(first)
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(checkbox('Ground beef')).not.toBeChecked()
    expect(within(row('Taco seasoning')).getByText('will sync')).toBeInTheDocument()
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()
    expect(patches(fetchMock)).toEqual([])

    goOnline()
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(fetchMock)).toEqual([`${ITEM('i3')} {"needed":true}`, `${ITEM('i1')} {"needed":false}`])
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(checkbox('Ground beef')).not.toBeChecked()
  })

  it('discards a queued toggle the server refuses because the list was archived, puts the count back and says so', async () => {
    const server = stubServer(() => errorResponse(409, 'archived'))
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()

    goOffline()
    fireEvent.click(checkbox('Taco seasoning'))
    expect(screen.getByText('3 / 3')).toBeInTheDocument()

    server.archive()
    goOnline()
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't mark Taco seasoning to buy. archived message")
    expect(await screen.findByText('Archived')).toBeInTheDocument()
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(within(row('Taco seasoning')).getByText('have it')).toBeInTheDocument()
    expect(screen.queryByText('will sync')).not.toBeInTheDocument()
    expect(queue()).toEqual([])
    // Sent once, never again.
    goOnline()
    await waitFor(() => expect(patches(server.fetchMock)).toEqual([`${ITEM('i3')} {"needed":true}`]))
  })

  it('does not queue a toggle the server refuses: it is put back and says why', async () => {
    const server = stubServer(() => errorResponse(400, 'invalid'))
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()

    fireEvent.click(checkbox('Ground beef'))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't mark Ground beef “have it”. invalid message")
    expect(checkbox('Ground beef')).toBeChecked()
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(screen.queryByText('will sync')).not.toBeInTheDocument()
    expect(queue()).toEqual([])

    goOffline()
    goOnline()
    await waitFor(() => expect(patches(server.fetchMock)).toEqual([`${ITEM('i1')} {"needed":false}`]))
  })
})

describe('Supply run saved by an earlier build', () => {
  // saveLegacyRun saves this week's Supply run as a build from before
  // dinners carried their status saved it: its dinners have no status.
  function saveLegacyRun(run: ReturnType<typeof stubServer>['run']) {
    const { groups, ...rest } = run('2026-10-05')
    const legacy = { ...rest, groups: groups.map(({ dinner: { id, name }, ...group }) => ({ ...group, dinner: { id, name } })) }
    const { timezone, plan_draft_dow, plan_draft_time } = HOUSEHOLD_SETTINGS
    localStorage.setItem(
      SAVED_KEY,
      JSON.stringify({ schedule: { timezone, plan_draft_dow, plan_draft_time }, runs: { '2026-10-05': legacy }, queue: [] }),
    )
  }

  function savedDinnerStatuses(): unknown[] {
    const saved = JSON.parse(localStorage.getItem(SAVED_KEY)!) as { runs: Record<string, { groups: { dinner: { status?: unknown } }[] }> }
    return saved.runs['2026-10-05']!.groups.map((group) => group.dinner.status)
  }

  it('opens it offline with its items and toggles, the dinner name plain text, and links it once read again', async () => {
    const { fetchMock, run } = stubServer()
    saveLegacyRun(run)
    onLine = false
    const first = await openPage()

    const tacos = await screen.findByRole('region', { name: 'Beef tacos' })
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(within(tacos).getByRole('heading', { level: 2, name: 'Beef tacos' })).toBeInTheDocument()
    expect(within(tacos).queryByRole('link')).not.toBeInTheDocument()
    expect(screen.queryByText(/can't be kept on this device/)).not.toBeInTheDocument()

    fireEvent.click(checkbox('Taco seasoning'))
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(screen.getByText('3 / 3')).toBeInTheDocument()
    expect(await within(row('Taco seasoning')).findByText('will sync')).toBeInTheDocument()
    expect(queue()).toEqual(['i3 true'])
    expect(screen.queryByText(/can't be kept on this device/)).not.toBeInTheDocument()

    // Online, the toggle is sent and its answer saved; the status is still
    // not known, so the name stays plain text.
    goOnline()
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(fetchMock)).toEqual([`${ITEM('i3')} {"needed":true}`])
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(savedDinnerStatuses()).toEqual([null])

    // Opened again online, the Supply run read gives the dinner's status: its
    // name opens its recipe, and the saved run has the status too.
    reload(first)
    await openPage()
    const read = await screen.findByRole('link', { name: 'Beef tacos' })
    expect(read).toHaveAttribute('href', '/dinner/d0')
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(screen.getByText('3 / 3')).toBeInTheDocument()
    expect(screen.queryByText(/can't be kept on this device/)).not.toBeInTheDocument()
    await waitFor(() => expect(savedDinnerStatuses()).toEqual(['regular']))
  })

  it('opens it online as the Supply run read, with the dinner name its recipe link and no saved-data warning', async () => {
    const { run } = stubServer()
    saveLegacyRun(run)
    await openPage()

    expect(await screen.findByRole('link', { name: 'Beef tacos' })).toHaveAttribute('href', '/dinner/d0')
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByText(/can't be kept on this device/)).not.toBeInTheDocument()
    await waitFor(() => expect(savedDinnerStatuses()).toEqual(['regular']))
  })
})

describe('offline toggle queue, replay', () => {
  it('replays a queue spanning two weeks strictly in order, one PATCH at a time, a toggle made meanwhile after it', async () => {
    const answers = held()
    const server = stubServer(undefined, answers.hold)
    const queued = [
      { key: 'k1', weekStart: '2026-10-05', id: 'i3', needed: true },
      { key: 'k2', weekStart: '2026-10-12', id: 'n1', needed: false },
      { key: 'k3', weekStart: '2026-10-05', id: 'i1', needed: false },
    ]
    const runs = { '2026-10-05': server.run('2026-10-05'), '2026-10-12': server.run('2026-10-12') }
    localStorage.setItem(SAVED_KEY, JSON.stringify({ schedule: null, runs, queue: queued }))

    await openPage()
    await waitFor(() => expect(answers.waiting()).toBe(1))
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Corn tortillas' }))
    for (let sent = 1; sent <= 4; sent++) {
      await waitFor(() => expect(patches(server.fetchMock)).toHaveLength(sent))
      // The next one is not sent before this one is answered.
      await new Promise((resolve) => setTimeout(resolve, 20))
      expect(patches(server.fetchMock)).toHaveLength(sent)
      act(() => answers.release())
    }
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(server.fetchMock)).toEqual([
      `${ITEM('i3')} {"needed":true}`,
      `${ITEM('n1')} {"needed":false}`,
      `${ITEM('i1')} {"needed":false}`,
      `${ITEM('i2')} {"needed":false}`,
    ])
    expect(server.mostInFlight()).toBe(1)
    await waitFor(() => expect(screen.getByText('1 / 3')).toBeInTheDocument())
  })

  it('replays at least once: a toggle the server applied before a reload is sent again and leaves the item and count the same', async () => {
    const answers = held()
    const server = stubServer(undefined, answers.hold)
    const first = await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()
    fireEvent.click(checkbox('Taco seasoning'))
    // The server applied it; the page reloads before its answer arrives.
    await waitFor(() => expect(answers.waiting()).toBe(1))
    expect(queue()).toEqual(['i3 true'])
    reload(first)
    act(() => answers.release())

    await openPage()
    await waitFor(() => expect(answers.waiting()).toBe(1))
    act(() => answers.release())
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(server.fetchMock)).toEqual([`${ITEM('i3')} {"needed":true}`, `${ITEM('i3')} {"needed":true}`])
    await waitFor(() => expect(screen.getByText('3 / 3')).toBeInTheDocument())
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(server.run('2026-10-05')).toMatchObject({ needed_count: 3, total_count: 3 })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('keeps "will sync" on a queued toggle until its delayed replay is answered', async () => {
    const answers = held()
    stubServer(undefined, answers.hold)
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()
    goOffline()
    fireEvent.click(checkbox('Ground beef'))
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()

    goOnline()
    await waitFor(() => expect(answers.waiting()).toBe(1))
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()
    expect(queue()).toEqual(['i1 false'])

    act(() => answers.release())
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(queue()).toEqual([])
    expect(screen.getByText('1 / 3')).toBeInTheDocument()
  })

  it('aborts a PATCH that never answers at its deadline, keeps it pending and tries it again before the toggles after it', async () => {
    let tries = 0
    // The first PATCH never answers; every later one answers at once.
    const server = stubServer(undefined, () => (++tries === 1 ? new Promise<void>(() => {}) : Promise.resolve()))
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()

    fireEvent.click(checkbox('Taco seasoning'))
    await waitFor(() => expect(patches(server.fetchMock)).toHaveLength(1))
    fireEvent.click(checkbox('Ground beef'))
    expect(queue()).toEqual(['i3 true', 'i1 false'])

    // Past the deadline the hung PATCH is aborted; both still wait, in order.
    await advance(TOGGLE_DEADLINE)
    expect(await within(row('Taco seasoning')).findByText('will sync')).toBeInTheDocument()
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()
    expect(patches(server.fetchMock)).toHaveLength(1)
    expect(queue()).toEqual(['i3 true', 'i1 false'])

    await retrySecondTry()
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(server.fetchMock)).toEqual([
      `${ITEM('i3')} {"needed":true}`,
      `${ITEM('i3')} {"needed":true}`,
      `${ITEM('i1')} {"needed":false}`,
    ])
    expect(server.mostInFlight()).toBe(1)
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('abandons a GET /api/v1/me that never answers at its deadline and loads it afresh before the toggles after it', async () => {
    let tries = 0
    // The page loaded /me; the first toggle loads it again, and that /me never
    // answers. Every later /me answers at once.
    const server = stubServer(undefined, undefined, () => (++tries === 2 ? new Promise<void>(() => {}) : Promise.resolve()))
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()

    fireEvent.click(checkbox('Taco seasoning'))
    await waitFor(() => expect(tries).toBe(2))
    fireEvent.click(checkbox('Ground beef'))
    expect(queue()).toEqual(['i3 true', 'i1 false'])

    // Past the deadline the hung /me is abandoned; both still wait, in order.
    await advance(TOGGLE_DEADLINE)
    expect(await within(row('Taco seasoning')).findByText('will sync')).toBeInTheDocument()
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()
    expect(patches(server.fetchMock)).toEqual([])

    // The next try sends a fresh /me, which the server answers.
    await retrySecondTry()
    await waitFor(() => expect(queue()).toEqual([]))
    expect(mes(server.fetchMock)).toBe(3)
    expect(patches(server.fetchMock)).toEqual([`${ITEM('i3')} {"needed":true}`, `${ITEM('i1')} {"needed":false}`])
    expect(server.mostInFlight()).toBe(1)
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('abandons the /me refresh of a csrf 403 that never answers at its deadline and refreshes afresh on the next try', async () => {
    let tries = 0
    // The session's token changed: each PATCH is refused for it until a /me
    // after the first refusal has answered. The first such refresh never
    // answers.
    let refusedAt: number | null = null
    const server = stubServer(
      () => {
        if (refusedAt !== null && server.meAnswered() > refusedAt) return undefined
        refusedAt ??= server.meAnswered()
        return errorResponse(403, 'csrf')
      },
      undefined,
      () => (refusedAt !== null && ++tries === 1 ? new Promise<void>(() => {}) : Promise.resolve()),
    )
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()

    fireEvent.click(checkbox('Taco seasoning'))
    await waitFor(() => expect(tries).toBe(1))
    fireEvent.click(checkbox('Ground beef'))
    expect(patches(server.fetchMock)).toHaveLength(1)

    // Past the deadline the hung refresh is abandoned; both still wait, in order.
    await advance(TOGGLE_DEADLINE)
    expect(await within(row('Taco seasoning')).findByText('will sync')).toBeInTheDocument()
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()
    expect(queue()).toEqual(['i3 true', 'i1 false'])

    // The next try is refused again and sends a fresh refresh, which answers.
    await retrySecondTry()
    await waitFor(() => expect(queue()).toEqual([]))
    expect(patches(server.fetchMock)).toEqual([
      `${ITEM('i3')} {"needed":true}`,
      `${ITEM('i3')} {"needed":true}`,
      `${ITEM('i3')} {"needed":true}`,
      `${ITEM('i1')} {"needed":false}`,
    ])
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('tries a toggle again once the server is back, with no online event from the browser', async () => {
    const { fetchMock } = stubServer()
    await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()

    // The browser says online throughout; the server cannot be reached, then can.
    reachable = false
    fireEvent.click(checkbox('Corn tortillas'))
    expect(await within(row('Corn tortillas')).findByText('will sync')).toBeInTheDocument()
    reachable = true
    expect(patches(fetchMock)).toEqual([])

    // Nothing says the server is back: only the wait's timer tries it again.
    await retrySecondTry()
    await waitFor(() => expect(queue()).toEqual([]))
    // The first try failed at GET /api/v1/me, before its PATCH.
    expect(patches(fetchMock)).toEqual([`${ITEM('i2')} {"needed":false}`])
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(screen.getByText('1 / 3')).toBeInTheDocument()
  })

  it('keeps "will sync" when the Supply run screen is left and opened again while the replay is pending', async () => {
    const answers = held()
    stubServer(undefined, answers.hold)
    const page = await openPage()
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()
    goOffline()
    fireEvent.click(checkbox('Ground beef'))
    expect(within(row('Ground beef')).getByText('will sync')).toBeInTheDocument()

    page.leave()
    goOnline()
    await waitFor(() => expect(answers.waiting()).toBe(1))
    page.reopen()
    expect(await within(row('Ground beef')).findByText('will sync')).toBeInTheDocument()
    expect(queue()).toEqual(['i1 false'])

    act(() => answers.release())
    await waitFor(() => expect(screen.queryByText('will sync')).not.toBeInTheDocument())
    expect(queue()).toEqual([])
    expect(screen.getByText('1 / 3')).toBeInTheDocument()
  })

  it('says the saved list is unreadable when it is corrupt offline, without rendering again and again', async () => {
    stubServer()
    onLine = false
    localStorage.setItem(SAVED_KEY, '{corrupt')
    const getItem = vi.spyOn(Storage.prototype, 'getItem')
    await openPage()

    expect(await screen.findByText(/Couldn't load the household settings/)).toBeInTheDocument()
    expect(screen.getByText(/can't be kept on this device while offline/)).toBeInTheDocument()
    const reads = getItem.mock.calls.filter(([key]) => key === SAVED_KEY).length
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(getItem.mock.calls.filter(([key]) => key === SAVED_KEY)).toHaveLength(reads)
  })
})

describe('Supply run saved on this device, refused', () => {
  // saveRefusedRun saves this week's Supply run with a dinner status
  // MealQuest does not know, and the schedule.
  function saveRefusedRun(run: ReturnType<typeof stubServer>['run']) {
    const unknown = run('2026-10-05')
    unknown.groups[0]!.dinner.status = 'retired'
    const { timezone, plan_draft_dow, plan_draft_time } = HOUSEHOLD_SETTINGS
    localStorage.setItem(
      SAVED_KEY,
      JSON.stringify({ schedule: { timezone, plan_draft_dow, plan_draft_time }, runs: { '2026-10-05': unknown }, queue: [] }),
    )
  }

  function savedWeeks(): string[] {
    return Object.keys((JSON.parse(localStorage.getItem(SAVED_KEY)!) as { runs: Record<string, unknown> }).runs).sort()
  }

  it('keeps saying so when another week is saved while the refused week is still saved', async () => {
    const { run } = stubServer()
    saveRefusedRun(run)
    onLine = false
    const page = await openPage()
    expect(await screen.findByText(/can't be kept on this device while offline.*dinner status "retired"/)).toBeInTheDocument()

    // Next week's Supply run is read, and saved beside the refused one.
    act(() => void page.queryClient.setQueryData(supplyRunKey('2026-10-12'), run('2026-10-12')))
    await waitFor(() => expect(savedWeeks()).toEqual(['2026-10-05', '2026-10-12']))
    await act(() => Promise.resolve())
    expect(screen.getByText(/can't be kept on this device while offline.*dinner status "retired"/)).toBeInTheDocument()
  })

  it('still refuses a saved Supply run whose dinner status is not one MealQuest knows', async () => {
    const { run } = stubServer()
    saveRefusedRun(run)
    onLine = false
    const first = await openPage()

    expect(await screen.findByText(/can't be kept on this device while offline.*dinner status "retired"/)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Beef tacos' })).not.toBeInTheDocument()

    // Opened again online, the Supply run read is saved over it: the page is
    // clean.
    reload(first)
    onLine = true
    await openPage()
    expect(await screen.findByRole('link', { name: 'Beef tacos' })).toHaveAttribute('href', '/dinner/d0')
    await waitFor(() => expect(screen.queryByText(/can't be kept on this device/)).not.toBeInTheDocument())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

describe('toggle not kept on this device', () => {
  it('says so until the toggle settles, even when a Supply run is saved meanwhile', async () => {
    const { hold, release, waiting } = held()
    const { run } = stubServer(undefined, hold)
    const page = await openPage()
    await screen.findByRole('region', { name: 'Beef tacos' })

    // The saved queue cannot be written as the toggle is made: it is
    // pending only on this page.
    const setItem = Storage.prototype.setItem
    let refuse = true
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (this: Storage, key: string, value: string) {
      if (key === SAVED_KEY && refuse) {
        refuse = false
        throw new DOMException('The quota has been exceeded.', 'QuotaExceededError')
      }
      setItem.call(this, key, value)
    })
    fireEvent.click(checkbox('Taco seasoning'))
    expect(await screen.findByText(/can't be kept on this device while offline.*quota has been exceeded/)).toBeInTheDocument()
    expect(queue()).toEqual([])
    await waitFor(() => expect(waiting()).toBe(1))

    // Next week's Supply run is read and saved: the toggle is still not.
    act(() => void page.queryClient.setQueryData(supplyRunKey('2026-10-12'), run('2026-10-12')))
    await act(() => Promise.resolve())
    expect(screen.getByText(/can't be kept on this device while offline.*quota has been exceeded/)).toBeInTheDocument()

    // Answered, the toggle has nothing left to lose.
    release()
    await waitFor(() => expect(screen.queryByText(/can't be kept on this device/)).not.toBeInTheDocument())
    expect(checkbox('Taco seasoning')).toBeChecked()
    expect(queue()).toEqual([])
  })
})
