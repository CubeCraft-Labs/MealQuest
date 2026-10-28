import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { errorResponse, fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import type { ArchivePost } from './api.ts'
import { PAGE, SupplyArchivePage } from './SupplyArchivePage.tsx'
import styles from './SupplyArchivePage.module.css'

const ARCHIVE = (by: string) => `/api/v1/shopping/archive?by=${by}`

// post is one Supply archive post as GET /api/v1/shopping/archive sends it.
function post(weekStart: string, label: string, overrides: Partial<ArchivePost> = {}): ArchivePost {
  return {
    weekStart,
    label,
    title: null,
    dinnerCount: 7,
    itemCount: 23,
    onHandCount: 4,
    dinners: ['Beef tacos', 'Tikka masala', 'Pizza', 'Pot roast', 'Orzo', 'Chili', 'Salmon'],
    current: false,
    ...overrides,
  }
}

// The posts of mock 1m: this week's titled post, then the weeks across the
// September–October boundary and the 2025–2026 one, the last untitled.
const WEEK_41 = post('2026-10-05', 'Week 41 · Oct 5 – 11', { title: 'Tacos, tikka & a salmon finale', current: true })
const WEEK_40 = post('2026-09-28', 'Week 40 · Sep 28 – Oct 4', { title: 'Pizza, pot roast & the great orzo trial', itemCount: 26 })
const WEEK_39 = post('2026-09-21', 'Week 39 · Sep 21 – 27', { title: 'Chili season opens', dinnerCount: 1, itemCount: 1, onHandCount: 0, dinners: ['Chili'] })
const WEEK_1 = post('2025-12-29', 'Week 1 · Dec 29 – Jan 4')

// The archive as the server groups it: by week each period is one ISO week
// headed by its label; by month and year a week belongs to the period of its
// Monday, so week 40 is September's and week 1 is 2025's.
const BY = {
  week: (
    [
      ['2026-W41', WEEK_41],
      ['2026-W40', WEEK_40],
      ['2026-W39', WEEK_39],
      ['2026-W01', WEEK_1],
    ] as const
  ).map(([key, p]) => ({ key, heading: p.label, posts: [p] })),
  month: [
    { key: '2026-10', heading: 'October 2026', posts: [WEEK_41] },
    { key: '2026-09', heading: 'September 2026', posts: [WEEK_40, WEEK_39] },
    { key: '2025-12', heading: 'December 2025', posts: [WEEK_1] },
  ],
  year: [
    { key: '2026', heading: '2026', posts: [WEEK_41, WEEK_40, WEEK_39] },
    { key: '2025', heading: '2025', posts: [WEEK_1] },
  ],
}

function archive(by: keyof typeof BY) {
  return () => jsonResponse(200, { by, periods: BY[by] })
}

type Responder = (init: RequestInit | undefined) => Response | Promise<Response>

// stubServer answers "METHOD path" requests from responders, with /me and
// the household settings; any other request fails the test.
function stubServer(responders: Record<string, Responder>) {
  const all: Record<string, Responder> = {
    'GET /api/v1/me': () => jsonResponse(200, ME),
    'GET /api/v1/settings/household': () => jsonResponse(200, HOUSEHOLD_SETTINGS),
    ...responders,
  }
  const fetchMock = vi.fn<typeof fetch>((input, init) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`
    const respond = Object.hasOwn(all, key) ? all[key] : undefined
    if (respond === undefined) throw new Error(`unexpected fetch: ${key}`)
    return Promise.resolve(respond(init))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// renderPage loads /me (for the CSRF token any write would need, so a
// missing PATCH is not for want of it) and renders the Supply archive at path.
async function renderPage(path = '/plan/shopping/archive') {
  const api = createApiClient(fakeLocation(path), createNetworkStatus(window), createVerifiedSession(localStorage))
  await api.loadMe()
  return render(
    <ApiProvider client={api}>
      <QueryClientProvider client={createQueryClient()}>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/plan/shopping/archive" element={<SupplyArchivePage />} />
            <Route path="/plan/shopping" element={<p>Supply run screen</p>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </ApiProvider>,
  )
}

// postCard is the post titled title, a link.
function postCard(title: string) {
  return screen.getByText(title).closest('a')!
}

beforeEach(() => {
  mockViewport(390)
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, 'matchMedia')
})

describe('SupplyArchivePage', () => {
  it('renders mock 1m by week: one post per week with its kicker, title, counts and dinner chips', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week') })
    await renderPage()

    expect(screen.getByRole('heading', { level: 1, name: 'Supply archive' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Week' })).toBeChecked()
    const card = await screen.findByText('Pizza, pot roast & the great orzo trial')
    const post40 = card.closest('a')!
    expect(within(post40).getByText('Week 40 · Sep 28 – Oct 4')).toHaveClass(styles.kicker!)
    expect(post40).toHaveTextContent('7 dinners · 26 items · 4 already on hand')
    expect(within(post40).getByText('Beef tacos')).toHaveClass(styles.chip!)
    expect(within(post40).getByText('Tikka masala')).toHaveClass(styles.chip!)
    expect(within(post40).getByText('+5')).toHaveClass(styles.chip!)
    expect(within(post40).queryByText('Pizza')).not.toBeInTheDocument()
    expect(postCard('Chili season opens')).toHaveTextContent('1 dinner · 1 item · 0 already on hand')
    expect(within(postCard('Chili season opens')).queryByText(/^\+/)).not.toBeInTheDocument()

    expect(screen.getAllByRole('link').map((link) => link.getAttribute('href'))).toEqual([
      '/plan/shopping/archive?week=2026-10-05',
      '/plan/shopping/archive?week=2026-09-28',
      '/plan/shopping/archive?week=2026-09-21',
      '/plan/shopping/archive?week=2025-12-29',
    ])
  })

  it('renders by week the periods and headings the server sends, as sent', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week') })
    await renderPage()
    await screen.findByText('Chili season opens')

    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(BY.week.map((period) => period.heading))
    expect(screen.getByRole('heading', { level: 2, name: WEEK_40.label })).toHaveClass(styles.heading!)
    for (const period of BY.week) {
      const section = screen.getByRole('region', { name: period.heading })
      expect(within(section).getAllByRole('link').map((link) => link.getAttribute('href'))).toEqual(
        period.posts.map((p) => `/plan/shopping/archive?week=${p.weekStart}`),
      )
    }
  })

  it('capitalises the week label kicker in CSS from the label as the API writes it', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week') })
    await renderPage()
    await screen.findByText('Chili season opens')
    expect(within(postCard('Chili season opens')).getByText('Week 39 · Sep 21 – 27')).toHaveClass(styles.kicker!)
  })

  it.each([
    ['Month', 'month', ['October 2026', 'September 2026', 'December 2025']],
    ['Year', 'year', ['2026', '2025']],
  ] as const)('groups by %s under the headings the server sends, across the month and year boundaries', async (label, by, headings) => {
    const fetchMock = stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week'), [`GET ${ARCHIVE(by)}`]: archive(by) })
    await renderPage()
    await screen.findByText('Chili season opens')

    fireEvent.click(screen.getByRole('radio', { name: label }))

    await waitFor(() => expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(headings))
    expect(screen.getByRole('heading', { level: 2, name: headings[0] })).toHaveClass(styles.heading!)
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toContain(ARCHIVE(by))
    for (const period of BY[by]) {
      const section = screen.getByRole('region', { name: period.heading })
      expect(within(section).getAllByRole('link').map((link) => link.getAttribute('href'))).toEqual(
        period.posts.map((p) => `/plan/shopping/archive?week=${p.weekStart}`),
      )
    }
  })

  it('highlights the current week’s post, by the API’s current, and no other', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week') })
    await renderPage()

    const current = (await screen.findByText('Tacos, tikka & a salmon finale')).closest('a')!
    expect(current).toHaveClass(styles.current!)
    expect(within(current).getByText('Week 41 · Oct 5 – 11 · Current')).toBeInTheDocument()
    for (const title of ['Pizza, pot roast & the great orzo trial', 'Chili season opens']) {
      expect(postCard(title)).not.toHaveClass(styles.current!)
      expect(postCard(title)).not.toHaveTextContent('Current')
    }
  })

  it('follows current from the API, not today’s date', async () => {
    const past = { ...WEEK_40, current: true }
    stubServer({ [`GET ${ARCHIVE('week')}`]: () => jsonResponse(200, { by: 'week', periods: [{ key: 'w', heading: past.label, posts: [{ ...WEEK_41, current: false }, past] }] }) })
    await renderPage()

    expect(await screen.findByText('Week 40 · Sep 28 – Oct 4 · Current')).toBeInTheDocument()
    expect(postCard('Tacos, tikka & a salmon finale')).not.toHaveClass(styles.current!)
  })

  it('titles an untitled post with its week label', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week') })
    await renderPage()

    await screen.findByText('Chili season opens')
    const untitled = screen.getByRole('link', { name: /Week 1 · Dec 29 – Jan 4/ })
    const label = within(untitled).getAllByText('Week 1 · Dec 29 – Jan 4')
    expect(label.map((el) => el.className)).toEqual([styles.kicker, styles.postTitle])
  })

  it(`shows ${PAGE} posts and "Load older" shows the next ${PAGE}, none twice and none skipped`, async () => {
    const weeks = Array.from({ length: 2 * PAGE + 3 }, (_, i) => {
      const monday = new Date(Date.UTC(2026, 9, 5 - 7 * i)).toISOString().slice(0, 10)
      return post(monday, `Week of ${monday}`, { title: `Post ${i}` })
    })
    // By month, so the cut falls inside a period and its heading is shown once.
    const periods = [
      { key: 'a', heading: 'Newer', posts: weeks.slice(0, PAGE + 2) },
      { key: 'b', heading: 'Older', posts: weeks.slice(PAGE + 2) },
    ]
    stubServer({
      [`GET ${ARCHIVE('week')}`]: archive('week'),
      [`GET ${ARCHIVE('month')}`]: () => jsonResponse(200, { by: 'month', periods }),
    })
    await renderPage()
    await screen.findByText('Chili season opens')
    fireEvent.click(screen.getByRole('radio', { name: 'Month' }))

    const titles = () => screen.getAllByRole('link').map((link) => within(link).getByText(/^Post /).textContent)
    await screen.findByText('Post 0')
    expect(titles()).toEqual(weeks.slice(0, PAGE).map((p) => p.title))
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(['Newer'])

    fireEvent.click(screen.getByRole('button', { name: 'Load older' }))
    expect(titles()).toEqual(weeks.slice(0, 2 * PAGE).map((p) => p.title))
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(['Newer', 'Older'])

    fireEvent.click(screen.getByRole('button', { name: 'Load older' }))
    expect(titles()).toEqual(weeks.map((p) => p.title))
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('offers no "Load older" when every post is shown', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week') })
    await renderPage()
    await screen.findByText('Chili season opens')
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('shows skeleton posts while the archive loads', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: () => new Promise<Response>(() => {}) })
    await renderPage()
    expect(screen.getByLabelText('Supply archive loading')).toHaveAttribute('aria-busy', 'true')
    expect(screen.getAllByTestId('skeleton-post')).toHaveLength(3)
  })

  it('says so when nothing is archived yet', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: () => jsonResponse(200, { by: 'week', periods: [] }) })
    await renderPage()
    expect(await screen.findByText('Nothing archived yet')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('reports a failed load and retries it', async () => {
    let reads = 0
    stubServer({
      [`GET ${ARCHIVE('week')}`]: () => (++reads === 1 ? errorResponse(500, 'internal') : archive('week')()),
    })
    await renderPage()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load the Supply archive.")
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Chili season opens')).toBeInTheDocument()
    expect(reads).toBe(2)
  })

  it('reports a response that is not the archive by the period asked for', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: () => jsonResponse(200, { by: 'month', periods: [] }) })
    await renderPage()
    expect(await screen.findByRole('alert')).toHaveTextContent(`GET ${ARCHIVE('week')}: response is not the Supply archive by week`)
  })

  it('goes back to the Supply run', async () => {
    stubServer({ [`GET ${ARCHIVE('week')}`]: archive('week') })
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Back to the Supply run' }))
    expect(await screen.findByText('Supply run screen')).toBeInTheDocument()
  })
})

// The frozen Supply run of week 40, as GET /api/v1/shopping/{weekStart}
// sends it.
const FROZEN = {
  weekStart: '2026-09-28',
  label: 'Week 40 · Sep 28 – Oct 4',
  title: 'Pizza, pot roast & the great orzo trial',
  status: 'archived',
  sent_to_chat_at: null,
  chat_send_unconfirmed_at: null,
  needed_count: 1,
  total_count: 2,
  groups: [
    {
      dinner: { id: 'd1', name: 'Friday pizza', status: 'regular', meal_source: 'cook' },
      date: '2026-10-02',
      items: [
        { id: 'i1', name: 'Pizza dough', qty: '2 balls', needed: true },
        { id: 'i2', name: 'Mozzarella', qty: '1 lb', needed: false },
      ],
    },
  ],
}

// The current week's Supply run, week 41, still current: the API's archive
// includes it, highlighted.
const CURRENT = {
  ...FROZEN,
  weekStart: '2026-10-05',
  label: 'Week 41 · Oct 5 – 11',
  title: 'Tacos, tikka & a salmon finale',
  status: 'current',
  groups: [
    {
      dinner: { id: 'd2', name: 'Beef tacos', status: 'regular', meal_source: 'cook' },
      date: '2026-10-05',
      items: [
        { id: 'i3', name: 'Corn tortillas', qty: '12', needed: true },
        { id: 'i4', name: 'Limes', qty: '3', needed: false },
      ],
    },
  ],
}

describe('SupplyArchivePage frozen post', () => {
  it('opens a tapped post’s frozen list read-only, tagged Archived, sending no PATCH', async () => {
    const fetchMock = stubServer({
      [`GET ${ARCHIVE('week')}`]: archive('week'),
      'GET /api/v1/shopping/2026-09-28': () => jsonResponse(200, FROZEN),
    })
    await renderPage()

    fireEvent.click(await screen.findByText('Pizza, pot roast & the great orzo trial'))

    expect(await screen.findByText('Archived')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: /Shopping list/ })).toBeInTheDocument()
    expect(screen.getByText('Pizza dough')).toBeInTheDocument()
    expect(screen.queryAllByRole('checkbox')).toEqual([])
    fireEvent.click(screen.getByText('Pizza dough'))
    fireEvent.click(screen.getByText('Mozzarella'))
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method !== undefined && init.method !== 'GET')).toEqual([])

    // The list links back to the archive.
    fireEvent.click(screen.getByRole('link', { name: 'Supply archive' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Supply archive' })).toBeInTheDocument()
  })

  it('opens the current week’s post read-only too, tagged Archived, with no checkbox, nothing to send and no PATCH', async () => {
    const fetchMock = stubServer({
      [`GET ${ARCHIVE('week')}`]: archive('week'),
      'GET /api/v1/shopping/2026-10-05': () => jsonResponse(200, CURRENT),
    })
    await renderPage()

    fireEvent.click(await screen.findByText('Tacos, tikka & a salmon finale'))

    expect(await screen.findByText('Archived')).toBeInTheDocument()
    expect(screen.getByText("This week's list as it stands, read-only in the Supply archive.")).toBeInTheDocument()
    expect(screen.getByText('Corn tortillas')).toBeInTheDocument()
    expect(screen.queryAllByRole('checkbox')).toEqual([])
    fireEvent.click(screen.getByText('Corn tortillas'))
    fireEvent.click(screen.getByText('Limes'))
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method !== undefined && init.method !== 'GET')).toEqual([])
  })

  it('on desktop opens the current week’s post read-only, and with no post open shows this week’s Supply run to change', async () => {
    mockViewport(1440)
    const fetchMock = stubServer({
      [`GET ${ARCHIVE('week')}`]: archive('week'),
      'GET /api/v1/shopping/2026-10-05': () => jsonResponse(200, CURRENT),
    })
    const view = await renderPage('/plan/shopping/archive?week=2026-10-05')

    expect(await screen.findByText('Archived')).toBeInTheDocument()
    expect(screen.getByText('Corn tortillas')).toBeInTheDocument()
    expect(screen.queryAllByRole('checkbox')).toEqual([])
    expect(screen.queryByText('Send to Google Chat')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('Corn tortillas'))
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method !== undefined && init.method !== 'GET')).toEqual([])
    view.unmount()

    await renderPage()
    expect(await screen.findByRole('checkbox', { name: 'Corn tortillas' })).toBeInTheDocument()
    expect(screen.queryByText('Archived')).not.toBeInTheDocument()
  })

  it('on desktop (mock 2f) shows the archive as a panel beside the open post’s list', async () => {
    mockViewport(1440)
    stubServer({
      [`GET ${ARCHIVE('week')}`]: archive('week'),
      'GET /api/v1/shopping/2026-09-28': () => jsonResponse(200, FROZEN),
    })
    await renderPage('/plan/shopping/archive?week=2026-09-28')

    const panel = screen.getByRole('complementary', { name: 'Supply archive' })
    expect(await within(panel).findByText('Chili season opens')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Back to the Supply run' })).not.toBeInTheDocument()
    expect(await screen.findByText('Archived')).toBeInTheDocument()
    expect(panel).not.toContainElement(screen.getByText('Pizza dough'))
    expect(screen.queryAllByRole('checkbox')).toEqual([])
  })
})
