// A Regular removed after the service worker saved reads that have it: the
// pages read through the worker's real API strategy (src/test/serviceWorker.ts),
// so what they show offline is what the worker kept. jsdom has no service
// worker, so the strategy runs directly against an in-memory cache.
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Link, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, createApiClient, createQueryClient, createVerifiedSession } from '../api/index.ts'
import { HOME_KEY } from '../home/api.ts'
import { HomePage } from '../home/HomePage.tsx'
import { planKey } from '../plan/api.ts'
import { PlanPage } from '../plan/PlanPage.tsx'
import { createNetworkStatus } from '../pwa/networkStatus.ts'
import { OfflinePill } from '../pwa/OfflinePill.tsx'
import { RegularsSettingsPage } from '../settings/RegularsSettingsPage.tsx'
import { supplyRunKey, SupplyRunSync } from '../shopping/api.ts'
import { SAVED_KEY } from '../shopping/offline.ts'
import { SupplyRunPage } from '../shopping/SupplyRunPage.tsx'
import { HOME, HOUSEHOLD_SETTINGS } from '../test/ApiProviders.tsx'
import { InDataRouter } from '../test/dataRouter.tsx'
import { dinnerBody } from '../test/dinners.ts'
import { fakeLocation, jsonResponse, ME } from '../test/fetchStub.ts'
import { mockViewport } from '../test/matchMedia.ts'
import { origin, stubFetchThroughWorker, stubWorkerCaches, type FakeCache } from '../test/serviceWorker.ts'

const WEEK = '2026-10-05'
const PLAN_SCREEN = '/plan'
const RUN_SCREEN = `/plan/shopping?week=${WEEK}`
const REGULARS_SCREEN = '/settings/regulars'

const TACOS = dinnerBody('d1', 'Beef tacos')
const ROAST = dinnerBody('d4', 'Pot roast')

const DAYS = ['2026-10-05', '2026-10-06', '2026-10-07', '2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11']
const NAMES = ['Pot roast', 'Chili', 'Beef tacos', 'Chicken stir-fry', 'Pizza night', 'Korean beef bowls', 'Sheet-pan salmon']
const IDS = ['d4', 'd5', 'd1', 'd6', 'd7', 'd8', 'd9']

// The week's plan, Home and Supply run as the server sent them while Beef
// tacos (d1), tonight's dinner, was a Regular.
const plan = {
  id: 'plan-1',
  week_start: WEEK,
  week_label: 'Week 41 · Oct 5 – 11',
  status: 'synced',
  ai_note: null,
  calendar_synced_at: '2026-10-04T15:00:00Z',
  days: DAYS.map((date, i) => ({
    date,
    dinner: { id: IDS[i], name: NAMES[i], status: 'regular', photo: null, minutes: 30, method: 'stovetop', origin: 'regular' },
    is_new: false,
    start_by: null,
  })),
  updated_at: '2026-10-04T15:00:00Z',
}
const home = { ...HOME, tonight: { ...HOME.tonight, id: 'd1', name: 'Beef tacos', status: 'regular' } }
const run = {
  weekStart: WEEK,
  label: 'Week 41 · Oct 5 – 11',
  title: null,
  status: 'current',
  sent_to_chat_at: null,
  chat_send_unconfirmed_at: null,
  needed_count: 2,
  total_count: 2,
  groups: [
    { dinner: { id: 'd4', name: 'Pot roast', status: 'regular' }, date: '2026-10-05', day: 'Monday', items: [{ id: 'i2', name: 'Chuck roast', qty: '3 lb', needed: true }] },
    { dinner: { id: 'd1', name: 'Beef tacos', status: 'regular' }, date: '2026-10-07', day: 'Wednesday', items: [{ id: 'i1', name: 'Ground beef', qty: '1 lb', needed: true }] },
  ],
}

let cache: FakeCache
let reachable: boolean
// networkReads is every "METHOD path?search" the network was asked for,
// reached or not.
let networkReads: string[]
let regulars = [TACOS, ROAST]

// server answers "METHOD path?search" while it can be reached.
const server: Record<string, () => unknown> = {
  'GET /api/v1/me': () => ME,
  'GET /api/v1/settings/household': () => HOUSEHOLD_SETTINGS,
  'GET /api/v1/dinners?status=regular': () => ({ dinners: regulars }),
  'GET /api/v1/dinners?status=quest_log': () => ({ dinners: [] }),
  [`GET /api/v1/plans/${WEEK}`]: () => plan,
  'GET /api/v1/home': () => home,
  'GET /api/v1/rotation-reviews': () => ({ reviews: [] }),
  [`GET /api/v1/shopping/${WEEK}`]: () => run,
  'DELETE /api/v1/dinners/d1': () => {
    regulars = [ROAST]
    return { ...TACOS, status: 'archived' }
  },
}

beforeEach(() => {
  cache = stubWorkerCaches()
  reachable = true
  networkReads = []
  regulars = [TACOS, ROAST]
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-07T16:00:00Z'))
  vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => reachable)
  stubFetchThroughWorker(async (request) => {
    const url = new URL(request.url)
    const key = `${request.method} ${url.pathname}${url.search}`
    networkReads.push(key)
    if (!reachable) throw new TypeError('Failed to fetch')
    const respond = Object.hasOwn(server, key) ? server[key] : undefined
    if (respond === undefined) throw new Error(`unexpected network fetch: ${key}`)
    return jsonResponse(200, respond())
  })
})

afterEach(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

// renderApp loads the app at path as a fresh page load would — a new API
// client, network status and query cache — with the Plan, Home, Supply run
// and Regulars screens, SupplyRunSync and the offline indicator, as App
// does; each screen links to the others.
async function renderApp(path: string): Promise<{ queryClient: QueryClient; unmount: () => void }> {
  const api = createApiClient(fakeLocation(path), createNetworkStatus(window), createVerifiedSession(localStorage))
  if (reachable) await api.loadMe()
  const queryClient = createQueryClient()
  const { unmount } = render(
    <ApiProvider client={api}>
      <QueryClientProvider client={queryClient}>
        <SupplyRunSync />
        <InDataRouter path={path}>
          <Routes>
            <Route path="/" element={<HomePage />} />
            <Route path="/plan" element={<PlanPage />} />
            <Route path="/plan/shopping" element={<SupplyRunPage />} />
            <Route path="/settings/regulars" element={<RegularsSettingsPage />} />
          </Routes>
          <nav>
            <Link to="/">To Home</Link>
            <Link to={PLAN_SCREEN}>To Plan</Link>
            <Link to={RUN_SCREEN}>To Supply run</Link>
            <Link to={REGULARS_SCREEN}>To Regulars</Link>
          </nav>
        </InDataRouter>
        <OfflinePill />
      </QueryClientProvider>
    </ApiProvider>,
  )
  return { queryClient, unmount }
}

// recipeLinks lists every link on the page to dinner id's recipe.
function recipeLinks(id: string) {
  return screen.queryAllByRole('link').filter((link) => link.getAttribute('href') === `/dinner/${id}`)
}

function go(name: string) {
  fireEvent.click(screen.getByRole('link', { name }))
}

// cachedHolding lists the reads the worker saved that have dinner id.
async function cachedHolding(id: string) {
  const holding: string[] = []
  for (const [url, response] of cache.entries) {
    if ((await response.clone().text()).includes(JSON.stringify(id))) holding.push(url.slice(origin.length))
  }
  return holding.sort()
}

// read waits until path has been read again and every read of the page is
// done, so what shows is what the read left.
async function read(queryClient: QueryClient, path: string) {
  await waitFor(() => expect(networkReads).toContain(`GET ${path}`))
  await waitFor(() => expect(queryClient.isFetching()).toBe(0))
}

function savedStatus(id: string) {
  const saved = JSON.parse(localStorage.getItem(SAVED_KEY)!) as { runs: Record<string, typeof run> }
  return saved.runs[WEEK]!.groups.find((group) => group.dinner.id === id)!.dinner.status
}

describe('a removed Regular read offline from what the worker saved before', () => {
  it('links to its recipe on no plan, Home or Supply run', async () => {
    // From tablet up the plan manager's day cards link to their recipes.
    mockViewport(768)

    // Online, the plan, Home and Supply run each link to Beef tacos' recipe,
    // and the worker saves each read.
    const online = await renderApp(PLAN_SCREEN)
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
    go('To Home')
    await waitFor(() => expect(recipeLinks('d1')).toHaveLength(1))
    go('To Supply run')
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Beef tacos' })).getByRole('link')).toHaveAttribute('href', '/dinner/d1'))
    await waitFor(() => expect(savedStatus('d1')).toBe('regular'))
    go('To Regulars')
    await screen.findByRole('heading', { level: 2, name: /^On rotation/ })
    expect(await cachedHolding('d1')).toEqual([
      '/api/v1/dinners?status=regular',
      '/api/v1/home',
      `/api/v1/plans/${WEEK}`,
      `/api/v1/shopping/${WEEK}`,
    ])

    // Beef tacos leaves the Regulars.
    fireEvent.click(screen.getByRole('button', { name: 'Edit Beef tacos' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove from Regulars' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(online.queryClient.isMutating()).toBe(0))
    expect(savedStatus('d1')).toBe('archived')

    // The server can no longer be reached and the plan, Home and Supply run
    // are read again, as when the app comes back to the front. The worker
    // kept no read that has Beef tacos, so none answers with it a Regular
    // again: no screen links to its recipe.
    reachable = false
    networkReads = []
    await Promise.all(
      [planKey(WEEK), HOME_KEY, supplyRunKey(WEEK)].map((queryKey) =>
        online.queryClient.refetchQueries({ queryKey, exact: true, type: 'all' }),
      ),
    )
    expect(networkReads).toEqual(
      expect.arrayContaining([`GET /api/v1/plans/${WEEK}`, 'GET /api/v1/home', `GET /api/v1/shopping/${WEEK}`]),
    )

    go('To Plan')
    await screen.findByRole('heading', { level: 1, name: 'Oct 5 – 11' })
    expect(recipeLinks('d1')).toEqual([])
    expect(screen.getByText(/Couldn't load this week's plan/)).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')

    // Home keeps what it showed, Beef tacos archived, with no Recipe pill.
    go('To Home')
    const tonight = await screen.findByRole('region', { name: 'Tonight' })
    expect(within(tonight).getByRole('heading', { name: 'Beef tacos' })).toBeInTheDocument()
    expect(recipeLinks('d1')).toEqual([])
    expect(screen.getByText(/Couldn't load tonight's quest/)).toBeInTheDocument()

    go('To Supply run')
    const card = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(within(card).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
    expect(recipeLinks('d1')).toEqual([])
    await read(online.queryClient, `/api/v1/shopping/${WEEK}`)
    expect(savedStatus('d1')).toBe('archived')
    online.unmount()

    // A new page offline opens the Supply run saved on this device, archived,
    // its items still toggling, and its read offline does not undo that.
    networkReads = []
    const runPage = await renderApp(RUN_SCREEN)
    await read(runPage.queryClient, `/api/v1/shopping/${WEEK}`)
    const reopened = await waitFor(() => screen.getByRole('region', { name: 'Beef tacos' }))
    expect(within(reopened).getByRole('checkbox', { name: /Ground beef/ })).toBeEnabled()
    expect(recipeLinks('d1')).toEqual([])
    expect(within(screen.getByRole('region', { name: 'Pot roast' })).getByRole('link')).toHaveAttribute('href', '/dinner/d4')
    expect(screen.getByRole('status')).toHaveTextContent('Offline — showing saved data')
    expect(savedStatus('d1')).toBe('archived')
  })
})
